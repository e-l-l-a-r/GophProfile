// Package worker реализует асинхронную обработку событий аватарок: создание
// миниатюр после загрузки и удаление файлов после мягкого удаления.
//
// Обработка идемпотентна: идентификаторы обработанных сообщений
// запоминаются, перед работой проверяется состояние аватарки, а ключи
// миниатюр детерминированы, поэтому повторная доставка сообщения не портит
// данные. Временные сбои повторяются с экспоненциальной задержкой; сообщение,
// которое обработать не удалось, уходит в очередь недоставленных.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"

	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/broker"
	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/retry"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
)

// Repository - хранилище метаданных аватарок и обработанных сообщений.
type Repository interface {
	GetAvatar(ctx context.Context, id string) (domain.Avatar, error)
	UpdateProcessingStatus(ctx context.Context, id string, status domain.ProcessingStatus) error
	CompleteProcessing(ctx context.Context, id string, thumbnails map[string]string) error
	IsMessageProcessed(ctx context.Context, messageID string) (bool, error)
	MarkMessageProcessed(ctx context.Context, messageID string) error
}

// FileStorage - хранилище файлов аватарок.
type FileStorage interface {
	Download(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Delete(ctx context.Context, keys ...string) error
}

// Consumer получает сообщения из очереди брокера.
type Consumer interface {
	Consume(ctx context.Context, queue string, concurrency int, handler broker.Handler) error
}

// Worker - обработчик событий аватарок.
type Worker struct {
	repo        Repository
	files       FileStorage
	log         *logger.Logger
	retry       retry.Policy
	concurrency int
}

// New создаёт воркер, обрабатывающий до concurrency сообщений каждой очереди
// одновременно и повторяющий временные сбои по политике policy.
func New(repo Repository, files FileStorage, policy retry.Policy, concurrency int, log *logger.Logger) *Worker {
	return &Worker{repo: repo, files: files, retry: policy, concurrency: max(concurrency, 1), log: log}
}

// Run потребляет события загрузки и удаления, пока не будет отменён ctx.
// Возвращает ошибку, если потребление из какой-либо очереди прекратилось
// аварийно.
func (w *Worker) Run(ctx context.Context, consumer Consumer) error {
	consumers := map[string]broker.Handler{
		broker.QueueUploaded: w.HandleUpload,
		broker.QueueDeleted:  w.HandleDelete,
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for queue, handler := range consumers {
		wg.Go(func() {
			if err := consumer.Consume(ctx, queue, w.concurrency, handler); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("очередь %q: %w", queue, err))
				mu.Unlock()
				// Без одной из очередей воркер неполноценен: останавливаем и вторую.
				cancel()
			}
		})
	}
	wg.Wait()

	return errors.Join(errs...)
}

// HandleUpload обрабатывает событие загрузки: создаёт миниатюры оригинала и
// отмечает обработку завершённой.
func (w *Worker) HandleUpload(ctx context.Context, msg broker.Message) error {
	var event domain.AvatarUploadEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil || event.AvatarID == "" {
		return fmt.Errorf("некорректное событие загрузки: %w", errors.Join(err, errEmptyAvatarID(event.AvatarID)))
	}

	return w.once(ctx, msg, func(ctx context.Context) error {
		err := retry.Do(ctx, w.retry, func(ctx context.Context) error {
			return w.createThumbnails(ctx, event.AvatarID)
		})
		if err != nil && ctx.Err() == nil {
			// Состояние failed видно клиенту в метаданных; оригинал при этом доступен.
			if statusErr := w.repo.UpdateProcessingStatus(ctx, event.AvatarID, domain.ProcessingStatusFailed); statusErr != nil &&
				!errors.Is(statusErr, domain.ErrAvatarNotFound) {
				err = errors.Join(err, statusErr)
			}
		}

		return err
	})
}

// HandleDelete обрабатывает событие удаления: удаляет файлы аватарки из
// хранилища. Удаление отсутствующих файлов ошибкой не считается.
func (w *Worker) HandleDelete(ctx context.Context, msg broker.Message) error {
	var event domain.AvatarDeleteEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil || event.AvatarID == "" {
		return fmt.Errorf("некорректное событие удаления: %w", errors.Join(err, errEmptyAvatarID(event.AvatarID)))
	}

	return w.once(ctx, msg, func(ctx context.Context) error {
		return retry.Do(ctx, w.retry, func(ctx context.Context) error {
			return w.files.Delete(ctx, event.S3Keys...)
		})
	})
}

// once выполняет обработку сообщения msg не более одного раза: уже
// обработанное сообщение пропускается, успешно обработанное запоминается.
// Сообщение без идентификатора обрабатывается всегда - операции и сами по себе
// идемпотентны.
func (w *Worker) once(ctx context.Context, msg broker.Message, process func(ctx context.Context) error) error {
	if msg.ID != "" {
		processed, err := retry.DoValue(ctx, w.retry, func(ctx context.Context) (bool, error) {
			return w.repo.IsMessageProcessed(ctx, msg.ID)
		})
		if err != nil {
			return err
		}
		if processed {
			w.log.Info("сообщение уже обработано, пропускаю", zap.String("сообщение", msg.ID))
			return nil
		}
	}

	if err := process(ctx); err != nil {
		return err
	}

	if msg.ID == "" {
		return nil
	}

	return retry.Do(ctx, w.retry, func(ctx context.Context) error {
		return w.repo.MarkMessageProcessed(ctx, msg.ID)
	})
}

// createThumbnails создаёт миниатюры аватарки id. Аватарка, которая уже
// обработана или удалена, пропускается. Ошибки, которые не исправятся
// повтором (нет оригинала, битое изображение), помечаются постоянными.
func (w *Worker) createThumbnails(ctx context.Context, id string) error {
	avatar, err := w.repo.GetAvatar(ctx, id)
	if errors.Is(err, domain.ErrAvatarNotFound) {
		w.log.Info("аватарка удалена до обработки, пропускаю", zap.String("аватарка", id))
		return nil
	}
	if err != nil {
		return err
	}
	if avatar.ProcessingStatus == domain.ProcessingStatusCompleted {
		return nil
	}

	format, err := imageproc.FormatFromMimeType(avatar.MimeType)
	if err != nil {
		return retry.Permanent(err)
	}

	if err = w.repo.UpdateProcessingStatus(ctx, id, domain.ProcessingStatusProcessing); err != nil {
		return skipDeleted(err)
	}

	original, err := w.files.Download(ctx, avatar.S3Key)
	if errors.Is(err, storage.ErrObjectNotFound) {
		return retry.Permanent(err)
	}
	if err != nil {
		return err
	}

	thumbnails := make(map[string]string, len(imageproc.ThumbnailSizes))
	for _, size := range imageproc.ThumbnailSizes {
		data, thumbErr := imageproc.Thumbnail(original, size, format)
		if thumbErr != nil {
			return retry.Permanent(fmt.Errorf("миниатюра %s: %w", size.Name, thumbErr))
		}

		key := domain.ThumbnailKey(id, size.Name, format.Extension())
		if err = w.files.Put(ctx, key, bytes.NewReader(data), int64(len(data)), format.MimeType()); err != nil {
			return err
		}
		thumbnails[size.Name] = key
	}

	err = w.repo.CompleteProcessing(ctx, id, thumbnails)
	if errors.Is(err, domain.ErrAvatarNotFound) {
		// Аватарку удалили во время обработки: событие удаления не знало о
		// новых миниатюрах, поэтому удаляем их сами.
		w.log.Info("аватарка удалена во время обработки, удаляю миниатюры", zap.String("аватарка", id))
		return w.files.Delete(ctx, slices.Sorted(maps.Values(thumbnails))...)
	}

	return err
}

// skipDeleted превращает ErrAvatarNotFound в успех: удалённую аватарку
// обрабатывать не нужно.
func skipDeleted(err error) error {
	if errors.Is(err, domain.ErrAvatarNotFound) {
		return nil
	}

	return err
}

// errEmptyAvatarID возвращает ошибку для пустого идентификатора аватарки.
func errEmptyAvatarID(id string) error {
	if id == "" {
		return errors.New("не задан avatar_id")
	}

	return nil
}
