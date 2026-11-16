package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
)

const (
	testID   = "11111111-1111-1111-1111-111111111111"
	testUser = "user-1"
)

var errBoom = errors.New("сбой")

// mocks - зависимости сервиса.
type mocks struct {
	repo  *MockRepository
	files *MockFileStorage
	pub   *MockPublisher
}

func newService(t *testing.T) (*AvatarService, mocks) {
	t.Helper()

	m := mocks{repo: NewMockRepository(t), files: NewMockFileStorage(t), pub: NewMockPublisher(t)}

	return NewAvatarService(m.repo, m.files, m.pub, logger.Nop()), m
}

// pngImage возвращает PNG-изображение width x height.
func pngImage(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		img.Set(x, 0, color.NRGBA{R: 200, A: 255})
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	return buf.Bytes()
}

// storedAvatar - сохранённая PNG-аватарка testID пользователя testUser.
func storedAvatar() domain.Avatar {
	return domain.Avatar{
		ID:               testID,
		UserID:           testUser,
		FileName:         "a.png",
		MimeType:         "image/png",
		S3Key:            domain.OriginalKey(testID, "png"),
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusCompleted,
		Thumbnails: map[string]string{
			"100x100": domain.ThumbnailKey(testID, "100x100", "png"),
			"300x300": domain.ThumbnailKey(testID, "300x300", "png"),
		},
	}
}

// createdAvatar - matcher аргумента CreateAvatar для загрузки PNG 40x30.
func createdAvatar(size int) any {
	return mock.MatchedBy(func(a domain.Avatar) bool {
		return a.ID != "" && a.UserID == testUser && a.FileName == "a.png" && a.MimeType == "image/png" &&
			a.S3Key == domain.OriginalKey(a.ID, "png") && a.SizeBytes == int64(size) &&
			a.Width == 40 && a.Height == 30 &&
			a.UploadStatus == domain.UploadStatusUploading && a.ProcessingStatus == domain.ProcessingStatusPending
	})
}

// echoCreate - CreateAvatar, возвращающий переданную аватарку.
func echoCreate(_ context.Context, a domain.Avatar) (domain.Avatar, error) {
	return a, nil
}

func TestUpload_Success(t *testing.T) {
	svc, m := newService(t)
	ctx := context.Background()
	data := pngImage(t, 40, 30)

	var id string
	m.repo.EXPECT().CreateAvatar(ctx, createdAvatar(len(data))).RunAndReturn(
		func(ctx context.Context, a domain.Avatar) (domain.Avatar, error) {
			id = a.ID
			return echoCreate(ctx, a)
		}).Once()
	m.files.EXPECT().Put(ctx, mock.AnythingOfType("string"), mock.Anything, int64(len(data)), "image/png").
		RunAndReturn(func(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
			assert.Equal(t, domain.OriginalKey(id, "png"), key)
			got, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.Equal(t, data, got)
			return nil
		}).Once()
	m.repo.EXPECT().UpdateUploadStatus(ctx, mock.Anything, domain.UploadStatusUploaded).Return(nil).Once()
	m.pub.EXPECT().Publish(ctx, domain.RoutingKeyAvatarUploaded, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, event any) error {
			assert.Equal(t, domain.AvatarUploadEvent{AvatarID: id, UserID: testUser, S3Key: domain.OriginalKey(id, "png")}, event)
			return nil
		}).Once()

	got, err := svc.Upload(ctx, UploadInput{UserID: testUser, FileName: "a.png", Data: data})
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, domain.UploadStatusUploaded, got.UploadStatus)
	assert.Equal(t, domain.ProcessingStatusPending, got.ProcessingStatus)
}

func TestUpload_InvalidImage(t *testing.T) {
	svc, _ := newService(t)

	_, err := svc.Upload(context.Background(), UploadInput{UserID: testUser, Data: []byte("GIF89a....")})
	require.ErrorIs(t, err, domain.ErrInvalidImage)
	require.ErrorIs(t, err, imageproc.ErrUnsupportedFormat)
}

func TestUpload_Failures(t *testing.T) {
	ctx := context.Background()

	t.Run("сбой репозитория", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().CreateAvatar(ctx, mock.Anything).Return(domain.Avatar{}, errBoom).Once()

		_, err := svc.Upload(ctx, UploadInput{UserID: testUser, Data: pngImage(t, 4, 4)})
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("сбой хранилища - состояние failed", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().CreateAvatar(ctx, mock.Anything).RunAndReturn(echoCreate).Once()
		m.files.EXPECT().Put(ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errBoom).Once()
		m.repo.EXPECT().UpdateUploadStatus(ctx, mock.Anything, domain.UploadStatusFailed).Return(nil).Once()

		_, err := svc.Upload(ctx, UploadInput{UserID: testUser, Data: pngImage(t, 4, 4)})
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("сбой отметки загрузки", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().CreateAvatar(ctx, mock.Anything).RunAndReturn(echoCreate).Once()
		m.files.EXPECT().Put(ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		m.repo.EXPECT().UpdateUploadStatus(ctx, mock.Anything, domain.UploadStatusUploaded).Return(errBoom).Once()

		_, err := svc.Upload(ctx, UploadInput{UserID: testUser, Data: pngImage(t, 4, 4)})
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("сбой публикации - обработка failed", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().CreateAvatar(ctx, mock.Anything).RunAndReturn(echoCreate).Once()
		m.files.EXPECT().Put(ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		m.repo.EXPECT().UpdateUploadStatus(ctx, mock.Anything, domain.UploadStatusUploaded).Return(nil).Once()
		m.pub.EXPECT().Publish(ctx, mock.Anything, mock.Anything).Return(errBoom).Once()
		m.repo.EXPECT().UpdateProcessingStatus(ctx, mock.Anything, domain.ProcessingStatusFailed).Return(nil).Once()

		_, err := svc.Upload(ctx, UploadInput{UserID: testUser, Data: pngImage(t, 4, 4)})
		require.ErrorIs(t, err, errBoom)
	})
}

func TestGetListLatest(t *testing.T) {
	ctx := context.Background()
	svc, m := newService(t)

	_, err := svc.Get(ctx, "not-a-uuid")
	require.ErrorIs(t, err, domain.ErrAvatarNotFound, "некорректный идентификатор не доходит до базы")

	m.repo.EXPECT().GetAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
	got, err := svc.Get(ctx, testID)
	require.NoError(t, err)
	assert.Equal(t, storedAvatar(), got)

	m.repo.EXPECT().LatestAvatar(ctx, testUser).Return(storedAvatar(), nil).Once()
	got, err = svc.Latest(ctx, testUser)
	require.NoError(t, err)
	assert.Equal(t, testID, got.ID)

	m.repo.EXPECT().ListAvatars(ctx, testUser).Return([]domain.Avatar{storedAvatar()}, nil).Once()
	list, err := svc.List(ctx, testUser)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	deleteEvent := domain.AvatarDeleteEvent{AvatarID: testID, S3Keys: storedAvatar().Keys()}

	t.Run("своя аватарка", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
		m.repo.EXPECT().SoftDeleteAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
		m.pub.EXPECT().Publish(ctx, domain.RoutingKeyAvatarDeleted, deleteEvent).Return(nil).Once()

		require.NoError(t, svc.Delete(ctx, testID, testUser))
	})

	t.Run("чужая аватарка", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(storedAvatar(), nil).Once()

		require.ErrorIs(t, svc.Delete(ctx, testID, "intruder"), domain.ErrForbidden)
	})

	t.Run("не найдена", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(domain.Avatar{}, domain.ErrAvatarNotFound).Once()

		require.ErrorIs(t, svc.Delete(ctx, testID, testUser), domain.ErrAvatarNotFound)
	})

	t.Run("удалена параллельно", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
		m.repo.EXPECT().SoftDeleteAvatar(ctx, testID).Return(domain.Avatar{}, domain.ErrAvatarNotFound).Once()

		require.ErrorIs(t, svc.Delete(ctx, testID, testUser), domain.ErrAvatarNotFound)
	})

	t.Run("сбой публикации не мешает удалению", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
		m.repo.EXPECT().SoftDeleteAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
		m.pub.EXPECT().Publish(ctx, domain.RoutingKeyAvatarDeleted, deleteEvent).Return(errBoom).Once()

		require.NoError(t, svc.Delete(ctx, testID, testUser))
	})
}

func TestDeleteLatest(t *testing.T) {
	ctx := context.Background()

	t.Run("своя", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().LatestAvatar(ctx, testUser).Return(storedAvatar(), nil).Once()
		m.repo.EXPECT().SoftDeleteAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
		m.pub.EXPECT().Publish(ctx, domain.RoutingKeyAvatarDeleted, mock.Anything).Return(nil).Once()

		require.NoError(t, svc.DeleteLatest(ctx, testUser, testUser))
	})

	t.Run("чужой пользователь", func(t *testing.T) {
		svc, _ := newService(t)

		require.ErrorIs(t, svc.DeleteLatest(ctx, testUser, "intruder"), domain.ErrForbidden)
	})

	t.Run("аватарок нет", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().LatestAvatar(ctx, testUser).Return(domain.Avatar{}, domain.ErrAvatarNotFound).Once()

		require.ErrorIs(t, svc.DeleteLatest(ctx, testUser, testUser), domain.ErrAvatarNotFound)
	})
}

// readImage читает изображение и проверяет его формат и размеры.
func readImage(t *testing.T, img Image, format imageproc.Format, width, height int) {
	t.Helper()

	data, err := io.ReadAll(img.Body)
	require.NoError(t, err)
	require.NoError(t, img.Body.Close())

	info, err := imageproc.Inspect(data)
	require.NoError(t, err)
	assert.Equal(t, imageproc.Info{Format: format, Width: width, Height: height}, info)
	assert.Equal(t, format.MimeType(), img.ContentType)
	assert.Equal(t, int64(len(data)), img.Size)
}

// object - объект хранилища с содержимым data.
func object(data []byte) storage.Object {
	return storage.Object{Body: io.NopCloser(bytes.NewReader(data)), ContentType: "image/png", Size: int64(len(data))}
}

func TestImage(t *testing.T) {
	ctx := context.Background()
	original := pngImage(t, 64, 48)
	thumb := pngImage(t, 100, 100)
	avatar := storedAvatar()

	t.Run("оригинал потоком", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(avatar, nil).Once()
		m.files.EXPECT().Get(ctx, avatar.S3Key).Return(object(original), nil).Once()

		img, err := svc.Image(ctx, testID, ImageRequest{Size: imageproc.SizeOriginal})
		require.NoError(t, err)
		assert.Equal(t, `"`+testID+`-original-png"`, img.ETag)
		readImage(t, img, imageproc.FormatPNG, 64, 48)
	})

	t.Run("готовая миниатюра потоком", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(avatar, nil).Once()
		m.files.EXPECT().Get(ctx, avatar.Thumbnails["100x100"]).Return(object(thumb), nil).Once()

		img, err := svc.Image(ctx, testID, ImageRequest{Size: "100x100", Format: "png"})
		require.NoError(t, err)
		assert.Equal(t, `"`+testID+`-100x100-png"`, img.ETag)
		readImage(t, img, imageproc.FormatPNG, 100, 100)
	})

	t.Run("миниатюра в другом формате", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(avatar, nil).Once()
		m.files.EXPECT().Download(ctx, avatar.Thumbnails["100x100"]).Return(thumb, nil).Once()

		img, err := svc.Image(ctx, testID, ImageRequest{Size: "100x100", Format: "webp"})
		require.NoError(t, err)
		assert.Equal(t, `"`+testID+`-100x100-webp"`, img.ETag)
		readImage(t, img, imageproc.FormatWebP, 100, 100)
	})

	t.Run("миниатюра ещё не готова - строится из оригинала", func(t *testing.T) {
		svc, m := newService(t)
		pending := avatar
		pending.Thumbnails = nil
		pending.ProcessingStatus = domain.ProcessingStatusProcessing
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(pending, nil).Once()
		m.files.EXPECT().Download(ctx, avatar.S3Key).Return(original, nil).Once()

		img, err := svc.Image(ctx, testID, ImageRequest{Size: "300x300"})
		require.NoError(t, err)
		readImage(t, img, imageproc.FormatPNG, 300, 300)
	})

	t.Run("оригинал в другом формате", func(t *testing.T) {
		svc, m := newService(t)
		m.repo.EXPECT().GetAvatar(ctx, testID).Return(avatar, nil).Once()
		m.files.EXPECT().Download(ctx, avatar.S3Key).Return(original, nil).Once()

		img, err := svc.Image(ctx, testID, ImageRequest{Format: "jpeg"})
		require.NoError(t, err)
		readImage(t, img, imageproc.FormatJPEG, 64, 48)
	})
}

func TestImage_Errors(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		setup   func(m mocks)
		wantErr error
		name    string
		req     ImageRequest
	}{
		{name: "некорректный размер", req: ImageRequest{Size: "50x50"}, wantErr: domain.ErrInvalidParameter},
		{name: "некорректный формат", req: ImageRequest{Format: "gif"}, wantErr: domain.ErrInvalidParameter},
		{
			name: "не найдена", wantErr: domain.ErrAvatarNotFound,
			setup: func(m mocks) {
				m.repo.EXPECT().GetAvatar(ctx, testID).Return(domain.Avatar{}, domain.ErrAvatarNotFound).Once()
			},
		},
		{
			name: "оригинал ещё загружается", wantErr: domain.ErrAvatarNotFound,
			setup: func(m mocks) {
				a := storedAvatar()
				a.UploadStatus = domain.UploadStatusUploading
				m.repo.EXPECT().GetAvatar(ctx, testID).Return(a, nil).Once()
			},
		},
		{
			name: "файл пропал из хранилища", wantErr: storage.ErrObjectNotFound,
			setup: func(m mocks) {
				m.repo.EXPECT().GetAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
				m.files.EXPECT().Get(ctx, mock.Anything).Return(storage.Object{}, storage.ErrObjectNotFound).Once()
			},
		},
		{
			name: "сбой скачивания", req: ImageRequest{Format: "webp"}, wantErr: errBoom,
			setup: func(m mocks) {
				m.repo.EXPECT().GetAvatar(ctx, testID).Return(storedAvatar(), nil).Once()
				m.files.EXPECT().Download(ctx, mock.Anything).Return(nil, errBoom).Once()
			},
		},
		{
			name: "неизвестный MIME-тип в базе", wantErr: imageproc.ErrUnsupportedFormat,
			setup: func(m mocks) {
				a := storedAvatar()
				a.MimeType = "image/gif"
				m.repo.EXPECT().GetAvatar(ctx, testID).Return(a, nil).Once()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, m := newService(t)
			if tt.setup != nil {
				tt.setup(m)
			}

			_, err := svc.Image(ctx, testID, tt.req)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
