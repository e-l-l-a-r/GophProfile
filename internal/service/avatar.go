// Package service содержит бизнес-логику сервиса аватарок: загрузку,
// получение, удаление и выдачу изображений нужного размера и формата.
// Файлы хранятся в хранилище файлов, метаданные - в репозитории, а тяжёлая
// работа (миниатюры, удаление файлов) передаётся воркеру через брокер.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
	"github.com/e-l-l-a-r/gophprofile/internal/telemetry"
)

// Repository - хранилище метаданных аватарок.
type Repository interface {
	CreateAvatar(ctx context.Context, avatar domain.Avatar) (domain.Avatar, error)
	GetAvatar(ctx context.Context, id string) (domain.Avatar, error)
	LatestAvatar(ctx context.Context, userID string) (domain.Avatar, error)
	ListAvatars(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDeleteAvatar(ctx context.Context, id string) (domain.Avatar, error)
	UpdateUploadStatus(ctx context.Context, id string, status domain.UploadStatus) error
	UpdateProcessingStatus(ctx context.Context, id string, status domain.ProcessingStatus) error
}

// FileStorage - хранилище файлов аватарок.
type FileStorage interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (storage.Object, error)
	Download(ctx context.Context, key string) ([]byte, error)
}

// Publisher публикует события для воркера.
type Publisher interface {
	Publish(ctx context.Context, routingKey string, event any) error
}

// UploadInput - загружаемая аватарка.
type UploadInput struct {
	// UserID - идентификатор владельца.
	UserID string
	// FileName - исходное имя файла.
	FileName string
	// Data - содержимое файла.
	Data []byte
}

// ImageRequest - параметры выдачи изображения.
type ImageRequest struct {
	// Size - "100x100", "300x300" или пусто/"original" для оригинала.
	Size string
	// Format - "jpeg", "png", "webp" или пусто для формата оригинала.
	Format string
}

// Image - изображение для выдачи клиенту. Body нужно закрыть.
type Image struct {
	// Body - содержимое изображения.
	Body io.ReadCloser
	// ContentType - MIME-тип изображения.
	ContentType string
	// ETag - метка содержимого в кавычках. Содержимое аватарки неизменно,
	// поэтому метка определяется идентификатором, размером и форматом.
	ETag string
	// Size - размер в байтах.
	Size int64
}

// AvatarService - сервис аватарок.
type AvatarService struct {
	repo      Repository
	files     FileStorage
	publisher Publisher
	log       *logger.Logger
}

// NewAvatarService создаёт сервис аватарок.
func NewAvatarService(repo Repository, files FileStorage, publisher Publisher, log *logger.Logger) *AvatarService {
	return &AvatarService{repo: repo, files: files, publisher: publisher, log: log}
}

// Upload проверяет изображение, сохраняет оригинал и метаданные и публикует
// событие загрузки, по которому воркер создаст миниатюры. Возвращает
// domain.ErrInvalidImage для недопустимого изображения.
//
// Запись в репозитории создаётся до загрузки файла в состоянии uploading:
// так файл в хранилище никогда не остаётся без метаданных, а сбой загрузки
// отражается в состоянии записи.
func (s *AvatarService) Upload(ctx context.Context, in UploadInput) (_ domain.Avatar, err error) {
	ctx, span := otel.Tracer("gophprofile/avatar").Start(ctx, "avatar.upload")
	defer telemetry.EndSpan(span, &err)
	start := time.Now()
	defer func() { telemetry.RecordUpload(ctx, start, err) }()
	span.SetAttributes(
		attribute.String("user_id", in.UserID),
		attribute.String("file_name", in.FileName),
		attribute.Int("file_size", len(in.Data)),
	)

	info, err := imageproc.Inspect(in.Data)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("%w: %w", domain.ErrInvalidImage, err)
	}
	span.SetAttributes(attribute.String("mime_type", info.Format.MimeType()))

	id := uuid.NewString()
	avatar, err := s.repo.CreateAvatar(ctx, domain.Avatar{
		ID:               id,
		UserID:           in.UserID,
		FileName:         in.FileName,
		MimeType:         info.Format.MimeType(),
		S3Key:            domain.OriginalKey(id, info.Format.Extension()),
		UploadStatus:     domain.UploadStatusUploading,
		ProcessingStatus: domain.ProcessingStatusPending,
		SizeBytes:        int64(len(in.Data)),
		Width:            info.Width,
		Height:           info.Height,
	})
	if err != nil {
		return domain.Avatar{}, err
	}

	err = s.files.Put(ctx, avatar.S3Key, bytes.NewReader(in.Data), avatar.SizeBytes, avatar.MimeType)
	if err != nil {
		return domain.Avatar{}, errors.Join(err, s.repo.UpdateUploadStatus(ctx, id, domain.UploadStatusFailed))
	}
	if err := s.repo.UpdateUploadStatus(ctx, id, domain.UploadStatusUploaded); err != nil {
		return domain.Avatar{}, err
	}
	avatar.UploadStatus = domain.UploadStatusUploaded

	event := domain.AvatarUploadEvent{AvatarID: id, UserID: avatar.UserID, S3Key: avatar.S3Key}
	if err := s.publisher.Publish(ctx, domain.RoutingKeyAvatarUploaded, event); err != nil {
		s.log.Ctx(ctx).Error("не удалось опубликовать событие загрузки", zap.String("avatar", id), zap.Error(err))
		if statusErr := s.repo.UpdateProcessingStatus(ctx, id, domain.ProcessingStatusFailed); statusErr != nil {
			s.log.Ctx(ctx).Error("не удалось обновить состояние обработки", zap.String("avatar", id), zap.Error(statusErr))
		} else {
			avatar.ProcessingStatus = domain.ProcessingStatusFailed
		}
	}

	return avatar, nil
}

// Get возвращает неудалённую аватарку или domain.ErrAvatarNotFound.
func (s *AvatarService) Get(ctx context.Context, id string) (domain.Avatar, error) {
	if uuid.Validate(id) != nil {
		return domain.Avatar{}, domain.ErrAvatarNotFound
	}

	return s.repo.GetAvatar(ctx, id)
}

// Latest возвращает самую новую аватарку пользователя или
// domain.ErrAvatarNotFound.
func (s *AvatarService) Latest(ctx context.Context, userID string) (domain.Avatar, error) {
	return s.repo.LatestAvatar(ctx, userID)
}

// List возвращает аватарки пользователя от новых к старым.
func (s *AvatarService) List(ctx context.Context, userID string) ([]domain.Avatar, error) {
	return s.repo.ListAvatars(ctx, userID)
}

// Delete мягко удаляет аватарку id по запросу пользователя requesterID и
// публикует событие удаления файлов. Возвращает domain.ErrAvatarNotFound,
// если аватарки нет, и domain.ErrForbidden, если она чужая.
func (s *AvatarService) Delete(ctx context.Context, id, requesterID string) error {
	avatar, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	return s.delete(ctx, avatar, requesterID)
}

// DeleteLatest удаляет самую новую аватарку пользователя userID по запросу
// пользователя requesterID. Ошибки те же, что у Delete.
func (s *AvatarService) DeleteLatest(ctx context.Context, userID, requesterID string) error {
	if userID != requesterID {
		return domain.ErrForbidden
	}

	avatar, err := s.repo.LatestAvatar(ctx, userID)
	if err != nil {
		return err
	}

	return s.delete(ctx, avatar, requesterID)
}

// delete проверяет владельца и удаляет аватарку.
func (s *AvatarService) delete(ctx context.Context, avatar domain.Avatar, requesterID string) (err error) {
	ctx, span := otel.Tracer("gophprofile/avatar").Start(ctx, "avatar.delete")
	defer telemetry.EndSpan(span, &err)
	defer func() { telemetry.RecordDelete(ctx, err) }()

	if avatar.UserID != requesterID {
		return domain.ErrForbidden
	}

	deleted, err := s.repo.SoftDeleteAvatar(ctx, avatar.ID)
	if err != nil {
		return err
	}

	event := domain.AvatarDeleteEvent{AvatarID: deleted.ID, S3Keys: deleted.Keys()}
	if err := s.publisher.Publish(ctx, domain.RoutingKeyAvatarDeleted, event); err != nil {
		// Для клиента аватарка уже удалена; без события в хранилище останутся
		// файлы, которые нужно удалить вручную по ключам из лога.
		s.log.Ctx(ctx).Error("не удалось опубликовать событие удаления, файлы остались в хранилище",
			zap.String("avatar", deleted.ID), zap.Strings("keys", event.S3Keys), zap.Error(err))
	}

	return nil
}

// Image возвращает изображение аватарки id нужного размера и формата.
// Миниатюра, которую воркер ещё не создал, строится на лету. Возвращает
// domain.ErrInvalidParameter для некорректных размера или формата и
// domain.ErrAvatarNotFound, если аватарки нет.
func (s *AvatarService) Image(ctx context.Context, id string, req ImageRequest) (_ Image, err error) {
	ctx, span := otel.Tracer("gophprofile/avatar").Start(ctx, "avatar.image")
	defer telemetry.EndSpan(span, &err)

	size, format, err := parseImageRequest(req)
	if err != nil {
		return Image{}, err
	}

	avatar, err := s.Get(ctx, id)
	if err != nil {
		return Image{}, err
	}
	// Пока оригинал не сохранён (или его сохранить не удалось), изображения нет.
	if avatar.UploadStatus != domain.UploadStatusUploaded {
		return Image{}, domain.ErrAvatarNotFound
	}

	stored, err := imageproc.FormatFromMimeType(avatar.MimeType)
	if err != nil {
		return Image{}, fmt.Errorf("аватарка %s: %w", avatar.ID, err)
	}
	if format == "" {
		format = stored
	}

	sizeName := imageproc.SizeOriginal
	if size != nil {
		sizeName = size.Name
	}
	etag := fmt.Sprintf(`"%s-%s-%s"`, avatar.ID, sizeName, format)

	key, ready := avatar.S3Key, true
	if size != nil {
		key, ready = avatar.Thumbnails[size.Name]
	}

	// Готовый файл нужного формата отдаётся из хранилища потоком.
	if ready && format == stored {
		obj, getErr := s.files.Get(ctx, key)
		if getErr != nil {
			return Image{}, s.missingFile(avatar, key, getErr)
		}

		return Image{Body: obj.Body, ContentType: format.MimeType(), ETag: etag, Size: obj.Size}, nil
	}

	data, err := s.transform(ctx, avatar, key, ready, size, format)
	if err != nil {
		return Image{}, err
	}

	return Image{
		Body:        io.NopCloser(bytes.NewReader(data)),
		ContentType: format.MimeType(),
		ETag:        etag,
		Size:        int64(len(data)),
	}, nil
}

// transform строит изображение нужного размера и формата: из готовой
// миниатюры (ready) - перекодированием, иначе - из оригинала.
func (s *AvatarService) transform(
	ctx context.Context, avatar domain.Avatar, key string, ready bool, size *imageproc.Size, format imageproc.Format,
) ([]byte, error) {
	if !ready {
		key = avatar.S3Key
	}

	data, err := s.files.Download(ctx, key)
	if err != nil {
		return nil, s.missingFile(avatar, key, err)
	}

	if ready || size == nil {
		return imageproc.Convert(data, format)
	}

	return imageproc.Thumbnail(data, *size, format)
}

// missingFile поясняет ошибку чтения файла аватарки. Отсутствие файла у
// существующей записи - нарушение целостности, а не «аватарка не найдена».
func (s *AvatarService) missingFile(avatar domain.Avatar, key string, err error) error {
	if errors.Is(err, storage.ErrObjectNotFound) {
		return fmt.Errorf("файл %q аватарки %s отсутствует в хранилище (состояние загрузки %s): %w",
			key, avatar.ID, avatar.UploadStatus, err)
	}

	return err
}

// parseImageRequest разбирает размер и формат; nil-размер означает оригинал,
// пустой формат - формат хранимого файла.
func parseImageRequest(req ImageRequest) (*imageproc.Size, imageproc.Format, error) {
	var size *imageproc.Size
	if req.Size != "" && req.Size != imageproc.SizeOriginal {
		parsed, err := imageproc.ParseSize(req.Size)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", domain.ErrInvalidParameter, err)
		}
		size = &parsed
	}

	var format imageproc.Format
	if req.Format != "" {
		parsed, err := imageproc.ParseFormat(req.Format)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", domain.ErrInvalidParameter, err)
		}
		format = parsed
	}

	return size, format, nil
}
