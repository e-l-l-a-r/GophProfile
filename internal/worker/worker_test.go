package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/e-l-l-a-r/gophprofile/internal/broker"
	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/retry"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
)

const (
	testID  = "11111111-1111-1111-1111-111111111111"
	testMsg = "msg-1"
)

var errBoom = errors.New("сбой")

var (
	thumbKeys = map[string]string{
		"100x100": domain.ThumbnailKey(testID, "100x100", "png"),
		"300x300": domain.ThumbnailKey(testID, "300x300", "png"),
	}
	originalKey = domain.OriginalKey(testID, "png")
)

func newWorker(t *testing.T) (*Worker, *MockRepository, *MockFileStorage) {
	t.Helper()

	repo, files := NewMockRepository(t), NewMockFileStorage(t)
	policy := retry.Policy{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 2, MaxAttempts: 3}

	return New(repo, files, policy, 2, logger.Nop()), repo, files
}

func pngImage(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 400, 200))))

	return buf.Bytes()
}

func pendingAvatar() domain.Avatar {
	return domain.Avatar{
		ID:               testID,
		UserID:           "user-1",
		MimeType:         "image/png",
		S3Key:            originalKey,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
}

func uploadMsg(t *testing.T, id string) broker.Message {
	t.Helper()

	body, err := json.Marshal(domain.AvatarUploadEvent{AvatarID: testID, UserID: "user-1", S3Key: originalKey})
	require.NoError(t, err)

	return broker.Message{ID: id, RoutingKey: domain.RoutingKeyAvatarUploaded, Body: body}
}

func deleteMsg(t *testing.T, id string, keys ...string) broker.Message {
	t.Helper()

	body, err := json.Marshal(domain.AvatarDeleteEvent{AvatarID: testID, S3Keys: keys})
	require.NoError(t, err)

	return broker.Message{ID: id, RoutingKey: domain.RoutingKeyAvatarDeleted, Body: body}
}

// expectThumbnailPuts ожидает сохранения обеих миниатюр и проверяет их размеры.
func expectThumbnailPuts(t *testing.T, files *MockFileStorage) {
	t.Helper()

	for _, size := range imageproc.ThumbnailSizes {
		files.EXPECT().Put(mock.Anything, thumbKeys[size.Name], mock.Anything, mock.Anything, "image/png").
			RunAndReturn(func(_ context.Context, _ string, body io.Reader, n int64, _ string) error {
				data, err := io.ReadAll(body)
				require.NoError(t, err)
				assert.Equal(t, n, int64(len(data)))
				info, err := imageproc.Inspect(data)
				require.NoError(t, err)
				assert.Equal(t, size.Width, info.Width)
				return nil
			}).Once()
	}
}

func TestHandleUpload_Success(t *testing.T) {
	w, repo, files := newWorker(t)
	ctx := context.Background()

	repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
	repo.EXPECT().GetAvatar(mock.Anything, testID).Return(pendingAvatar(), nil).Once()
	repo.EXPECT().UpdateProcessingStatus(mock.Anything, testID, domain.ProcessingStatusProcessing).Return(nil).Once()
	files.EXPECT().Download(mock.Anything, originalKey).Return(pngImage(t), nil).Once()
	expectThumbnailPuts(t, files)
	repo.EXPECT().CompleteProcessing(mock.Anything, testID, thumbKeys).Return(nil).Once()
	repo.EXPECT().MarkMessageProcessed(ctx, testMsg).Return(nil).Once()

	require.NoError(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)))
}

func TestHandleUpload_Idempotency(t *testing.T) {
	ctx := context.Background()

	t.Run("сообщение уже обработано", func(t *testing.T) {
		w, repo, _ := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(true, nil).Once()

		require.NoError(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)))
	})

	t.Run("аватарка уже обработана", func(t *testing.T) {
		w, repo, _ := newWorker(t)
		done := pendingAvatar()
		done.ProcessingStatus = domain.ProcessingStatusCompleted
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		repo.EXPECT().GetAvatar(mock.Anything, testID).Return(done, nil).Once()
		repo.EXPECT().MarkMessageProcessed(ctx, testMsg).Return(nil).Once()

		require.NoError(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)))
	})

	t.Run("аватарка удалена до обработки", func(t *testing.T) {
		w, repo, _ := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		repo.EXPECT().GetAvatar(mock.Anything, testID).Return(domain.Avatar{}, domain.ErrAvatarNotFound).Once()
		repo.EXPECT().MarkMessageProcessed(ctx, testMsg).Return(nil).Once()

		require.NoError(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)))
	})

	t.Run("удалена во время обработки - миниатюры удаляются", func(t *testing.T) {
		w, repo, files := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, "").Return(false, nil).Maybe()
		repo.EXPECT().GetAvatar(mock.Anything, testID).Return(pendingAvatar(), nil).Once()
		repo.EXPECT().UpdateProcessingStatus(mock.Anything, testID, domain.ProcessingStatusProcessing).Return(nil).Once()
		files.EXPECT().Download(mock.Anything, originalKey).Return(pngImage(t), nil).Once()
		expectThumbnailPuts(t, files)
		repo.EXPECT().CompleteProcessing(mock.Anything, testID, thumbKeys).Return(domain.ErrAvatarNotFound).Once()
		files.EXPECT().Delete(mock.Anything, thumbKeys["100x100"], thumbKeys["300x300"]).Return(nil).Once()

		require.NoError(t, w.HandleUpload(ctx, uploadMsg(t, "")), "сообщение без идентификатора тоже обрабатывается")
	})
}

func TestHandleUpload_Retry(t *testing.T) {
	w, repo, files := newWorker(t)
	ctx := context.Background()

	repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, errBoom).Once()
	repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
	repo.EXPECT().GetAvatar(mock.Anything, testID).Return(pendingAvatar(), nil).Twice()
	repo.EXPECT().UpdateProcessingStatus(mock.Anything, testID, domain.ProcessingStatusProcessing).Return(nil).Twice()
	files.EXPECT().Download(mock.Anything, originalKey).Return(nil, errBoom).Once()
	files.EXPECT().Download(mock.Anything, originalKey).Return(pngImage(t), nil).Once()
	expectThumbnailPuts(t, files)
	repo.EXPECT().CompleteProcessing(mock.Anything, testID, thumbKeys).Return(nil).Once()
	repo.EXPECT().MarkMessageProcessed(ctx, testMsg).Return(errBoom).Once()
	repo.EXPECT().MarkMessageProcessed(ctx, testMsg).Return(nil).Once()

	require.NoError(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)), "временные сбои повторяются")
}

func TestHandleUpload_Failures(t *testing.T) {
	ctx := context.Background()

	t.Run("некорректное сообщение", func(t *testing.T) {
		w, _, _ := newWorker(t)

		require.Error(t, w.HandleUpload(ctx, broker.Message{ID: testMsg, Body: []byte("{")}))
		require.Error(t, w.HandleUpload(ctx, broker.Message{ID: testMsg, Body: []byte("{}")}))
	})

	t.Run("попытки исчерпаны - состояние failed", func(t *testing.T) {
		w, repo, files := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		repo.EXPECT().GetAvatar(mock.Anything, testID).Return(pendingAvatar(), nil).Times(3)
		repo.EXPECT().UpdateProcessingStatus(mock.Anything, testID, domain.ProcessingStatusProcessing).Return(nil).Times(3)
		files.EXPECT().Download(mock.Anything, originalKey).Return(nil, errBoom).Times(3)
		repo.EXPECT().UpdateProcessingStatus(ctx, testID, domain.ProcessingStatusFailed).Return(nil).Once()

		require.ErrorIs(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)), errBoom)
	})

	t.Run("нет оригинала - без повторов", func(t *testing.T) {
		w, repo, files := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		repo.EXPECT().GetAvatar(mock.Anything, testID).Return(pendingAvatar(), nil).Once()
		repo.EXPECT().UpdateProcessingStatus(mock.Anything, testID, domain.ProcessingStatusProcessing).Return(nil).Once()
		files.EXPECT().Download(mock.Anything, originalKey).Return(nil, storage.ErrObjectNotFound).Once()
		repo.EXPECT().UpdateProcessingStatus(ctx, testID, domain.ProcessingStatusFailed).Return(nil).Once()

		require.ErrorIs(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)), storage.ErrObjectNotFound)
	})

	t.Run("битое изображение - без повторов", func(t *testing.T) {
		w, repo, files := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		repo.EXPECT().GetAvatar(mock.Anything, testID).Return(pendingAvatar(), nil).Once()
		repo.EXPECT().UpdateProcessingStatus(mock.Anything, testID, domain.ProcessingStatusProcessing).Return(nil).Once()
		files.EXPECT().Download(mock.Anything, originalKey).Return([]byte("not an image"), nil).Once()
		repo.EXPECT().UpdateProcessingStatus(ctx, testID, domain.ProcessingStatusFailed).Return(domain.ErrAvatarNotFound).Once()

		require.ErrorIs(t, w.HandleUpload(ctx, uploadMsg(t, testMsg)), imageproc.ErrUnsupportedFormat)
	})

	t.Run("неизвестный MIME-тип", func(t *testing.T) {
		w, repo, _ := newWorker(t)
		avatar := pendingAvatar()
		avatar.MimeType = "image/gif"
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		repo.EXPECT().GetAvatar(mock.Anything, testID).Return(avatar, nil).Once()
		repo.EXPECT().UpdateProcessingStatus(ctx, testID, domain.ProcessingStatusFailed).Return(errBoom).Once()

		err := w.HandleUpload(ctx, uploadMsg(t, testMsg))
		require.ErrorIs(t, err, imageproc.ErrUnsupportedFormat)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("остановка - статус не меняется", func(t *testing.T) {
		w, repo, _ := newWorker(t)
		canceled, cancel := context.WithCancel(ctx)
		defer cancel()
		propagatesCancellation := mock.MatchedBy(func(got context.Context) bool {
			return got != nil && got.Done() == canceled.Done()
		})
		repo.EXPECT().IsMessageProcessed(propagatesCancellation, testMsg).Return(false, nil).Once()
		repo.EXPECT().GetAvatar(propagatesCancellation, testID).RunAndReturn(func(got context.Context, _ string) (domain.Avatar, error) {
			cancel()
			require.ErrorIs(t, got.Err(), context.Canceled)
			return domain.Avatar{}, got.Err()
		}).Once()

		require.ErrorIs(t, w.HandleUpload(canceled, uploadMsg(t, testMsg)), context.Canceled)
	})
}

func TestHandleDelete(t *testing.T) {
	ctx := context.Background()
	keys := []string{originalKey, thumbKeys["100x100"]}

	t.Run("успех", func(t *testing.T) {
		w, repo, files := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		files.EXPECT().Delete(ctx, keys[0], keys[1]).Return(errBoom).Once()
		files.EXPECT().Delete(ctx, keys[0], keys[1]).Return(nil).Once()
		repo.EXPECT().MarkMessageProcessed(ctx, testMsg).Return(nil).Once()

		require.NoError(t, w.HandleDelete(ctx, deleteMsg(t, testMsg, keys...)))
	})

	t.Run("уже обработано", func(t *testing.T) {
		w, repo, _ := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(true, nil).Once()

		require.NoError(t, w.HandleDelete(ctx, deleteMsg(t, testMsg, keys...)))
	})

	t.Run("попытки исчерпаны", func(t *testing.T) {
		w, repo, files := newWorker(t)
		repo.EXPECT().IsMessageProcessed(ctx, testMsg).Return(false, nil).Once()
		files.EXPECT().Delete(ctx, keys[0], keys[1]).Return(errBoom).Times(3)

		require.ErrorIs(t, w.HandleDelete(ctx, deleteMsg(t, testMsg, keys...)), errBoom)
	})

	t.Run("некорректное сообщение", func(t *testing.T) {
		w, _, _ := newWorker(t)

		require.Error(t, w.HandleDelete(ctx, broker.Message{Body: []byte("[]")}))
	})
}

func TestRun(t *testing.T) {
	t.Run("подписка на обе очереди до отмены", func(t *testing.T) {
		w, _, _ := newWorker(t)
		consumer := NewMockConsumer(t)
		for _, queue := range []string{broker.QueueUploaded, broker.QueueDeleted} {
			consumer.EXPECT().Consume(mock.Anything, queue, 2, mock.Anything).
				RunAndReturn(func(ctx context.Context, _ string, _ int, _ broker.Handler) error {
					<-ctx.Done()
					return nil
				}).Once()
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- w.Run(ctx, consumer) }()
		cancel()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Run не завершился")
		}
	})

	t.Run("авария одной очереди останавливает другую", func(t *testing.T) {
		w, _, _ := newWorker(t)
		consumer := NewMockConsumer(t)
		consumer.EXPECT().Consume(mock.Anything, broker.QueueUploaded, 2, mock.Anything).Return(errBoom).Once()
		consumer.EXPECT().Consume(mock.Anything, broker.QueueDeleted, 2, mock.Anything).
			RunAndReturn(func(ctx context.Context, _ string, _ int, _ broker.Handler) error {
				<-ctx.Done()
				return nil
			}).Once()

		require.ErrorIs(t, w.Run(context.Background(), consumer), errBoom)
	})
}
