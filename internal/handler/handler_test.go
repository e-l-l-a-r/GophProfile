package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
	"github.com/e-l-l-a-r/gophprofile/web"
)

const (
	testID      = "11111111-1111-1111-1111-111111111111"
	testUser    = "user-1"
	testMaxSize = 1024
)

var (
	errBoom  = errors.New("сбой")
	testTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

// newTestServer создаёт обработчик с моком сервиса и проверками checkers.
func newTestServer(t *testing.T, checkers map[string]Checker) (http.Handler, *MockAvatars) {
	t.Helper()

	avatars := NewMockAvatars(t)
	h := New(avatars, Web{Static: web.Static, Templates: web.Templates},
		Config{Checkers: checkers, MaxUploadSize: testMaxSize, HealthTimeout: time.Second}, logger.Nop())

	return h.Routes(), avatars
}

// do выполняет запрос к обработчику.
func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	return w
}

// decode разбирает JSON-ответ.
func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v), w.Body.String())

	return v
}

// multipartBody формирует форму с файлом data в поле field.
func multipartBody(t *testing.T, field, name string, data []byte) (io.Reader, string) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	require.NoError(t, mw.WriteField("comment", "ignored"))
	if field != "" {
		fw, err := mw.CreateFormFile(field, name)
		require.NoError(t, err)
		_, err = fw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())

	return &buf, mw.FormDataContentType()
}

// uploadRequest формирует запрос загрузки.
func uploadRequest(t *testing.T, field, name string, data []byte, userID string) *http.Request {
	t.Helper()

	body, contentType := multipartBody(t, field, name, data)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	r.Header.Set("Content-Type", contentType)
	if userID != "" {
		r.Header.Set(HeaderUserID, userID)
	}

	return r
}

func storedAvatar() domain.Avatar {
	return domain.Avatar{
		CreatedAt:        testTime,
		UpdatedAt:        testTime.Add(time.Minute),
		Thumbnails:       map[string]string{"300x300": "t/300", "100x100": "t/100"},
		ID:               testID,
		UserID:           testUser,
		FileName:         "a.png",
		MimeType:         "image/png",
		S3Key:            "originals/a.png",
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusCompleted,
		SizeBytes:        2048,
		Width:            640,
		Height:           480,
	}
}

func TestUpload(t *testing.T) {
	for _, field := range []string{"file", "image"} {
		t.Run("поле "+field, func(t *testing.T) {
			h, avatars := newTestServer(t, nil)
			avatars.EXPECT().Upload(mock.Anything, service.UploadInput{UserID: testUser, FileName: "a.png", Data: []byte("img")}).
				Return(domain.Avatar{ID: testID, UserID: testUser, CreatedAt: testTime}, nil).Once()

			w := do(h, uploadRequest(t, field, `C:\photos\a.png`, []byte("img"), testUser))

			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			assert.JSONEq(t, `{
				"id": "`+testID+`",
				"user_id": "user-1",
				"url": "/api/v1/avatars/`+testID+`",
				"status": "processing",
				"created_at": "2026-01-02T03:04:05Z"
			}`, w.Body.String())
		})
	}
}

func TestUpload_Errors(t *testing.T) {
	tests := []struct {
		request    func(t *testing.T) *http.Request
		setup      func(m *MockAvatars)
		wantBody   string
		name       string
		wantStatus int
	}{
		{
			name:       "без X-User-ID",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "a.png", []byte("img"), "") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "слишком длинный X-User-ID",
			request: func(t *testing.T) *http.Request {
				return uploadRequest(t, "file", "a.png", []byte("img"), strings.Repeat("u", 256))
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "нет файла",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "", "", nil, testUser) },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "пустой файл",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "a.png", nil, testUser) },
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "не multipart",
			request: func(*testing.T) *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", strings.NewReader("{}"))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set(HeaderUserID, testUser)
				return r
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "файл больше предела",
			request: func(t *testing.T) *http.Request {
				return uploadRequest(t, "file", "a.png", bytes.Repeat([]byte("x"), testMaxSize+1), testUser)
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"error":"File too large","max_size":1024}`,
		},
		{
			name: "тело больше предела с запасом",
			request: func(t *testing.T) *http.Request {
				return uploadRequest(t, "comment2", "a.png", bytes.Repeat([]byte("x"), 2<<20), testUser)
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"error":"File too large","max_size":1024}`,
		},
		{
			name:    "неподдерживаемый формат",
			request: func(t *testing.T) *http.Request { return uploadRequest(t, "file", "a.gif", []byte("gif"), testUser) },
			setup: func(m *MockAvatars) {
				m.EXPECT().Upload(mock.Anything, mock.Anything).Return(domain.Avatar{}, domain.ErrInvalidImage).Once()
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid file format","details":"Supported formats: jpeg, png, webp"}`,
		},
		{
			name:    "слишком большое разрешение",
			request: func(t *testing.T) *http.Request { return uploadRequest(t, "file", "a.png", []byte("img"), testUser) },
			setup: func(m *MockAvatars) {
				m.EXPECT().Upload(mock.Anything, mock.Anything).Return(domain.Avatar{}, fmt.Errorf("%w: %w", domain.ErrInvalidImage, imageproc.ErrTooManyPixels)).Once()
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Image too large","details":"Maximum resolution: 50000000 pixels"}`,
		},
		{
			name:    "внутренняя ошибка",
			request: func(t *testing.T) *http.Request { return uploadRequest(t, "file", "a.png", []byte("img"), testUser) },
			setup: func(m *MockAvatars) {
				m.EXPECT().Upload(mock.Anything, mock.Anything).Return(domain.Avatar{}, errBoom).Once()
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error":"Internal server error"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, avatars := newTestServer(t, nil)
			if tt.setup != nil {
				tt.setup(avatars)
			}

			w := do(h, tt.request(t))

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
			if tt.wantBody != "" {
				assert.JSONEq(t, tt.wantBody, w.Body.String())
			}
		})
	}
}

// image - изображение для ответа мока.
func image(data string) service.Image {
	return service.Image{
		Body:        io.NopCloser(strings.NewReader(data)),
		ContentType: "image/webp",
		ETag:        `"etag-1"`,
		Size:        int64(len(data)),
	}
}

func TestImage(t *testing.T) {
	t.Run("изображение с параметрами", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Image(mock.Anything, testID, service.ImageRequest{Size: "300x300", Format: "webp"}).
			Return(image("webp-bytes"), nil).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+testID+"?size=300x300&format=webp", nil))

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "webp-bytes", w.Body.String())
		assert.Equal(t, "image/webp", w.Header().Get("Content-Type"))
		assert.Equal(t, "10", w.Header().Get("Content-Length"))
		assert.Equal(t, `"etag-1"`, w.Header().Get("ETag"))
		assert.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"))
	})

	t.Run("не изменилось - 304", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Image(mock.Anything, testID, service.ImageRequest{}).Return(image("x"), nil).Once()

		r := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+testID, nil)
		r.Header.Set("If-None-Match", `"other", W/"etag-1"`)
		w := do(h, r)

		assert.Equal(t, http.StatusNotModified, w.Code)
		assert.Empty(t, w.Body.String())
		assert.Equal(t, `"etag-1"`, w.Header().Get("ETag"))
	})

	t.Run("не найдена", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Image(mock.Anything, testID, service.ImageRequest{}).
			Return(service.Image{}, domain.ErrAvatarNotFound).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+testID, nil))

		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.JSONEq(t, `{"error":"Avatar not found"}`, w.Body.String())
	})

	t.Run("некорректный параметр", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Image(mock.Anything, testID, service.ImageRequest{Size: "1x1"}).
			Return(service.Image{}, domain.ErrInvalidParameter).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+testID+"?size=1x1", nil))

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestUserImage(t *testing.T) {
	t.Run("самая новая аватарка", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Latest(mock.Anything, testUser).Return(storedAvatar(), nil).Once()
		avatars.EXPECT().Image(mock.Anything, testID, service.ImageRequest{Size: "100x100"}).Return(image("img"), nil).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUser+"/avatar?size=100x100", nil))

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "img", w.Body.String())
		assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	})

	t.Run("аватарок нет", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Latest(mock.Anything, testUser).Return(domain.Avatar{}, domain.ErrAvatarNotFound).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUser+"/avatar", nil))

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestMetadata(t *testing.T) {
	h, avatars := newTestServer(t, nil)
	avatars.EXPECT().Get(mock.Anything, testID).Return(storedAvatar(), nil).Once()

	w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+testID+"/metadata", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{
		"id": "`+testID+`",
		"user_id": "user-1",
		"url": "/api/v1/avatars/`+testID+`",
		"file_name": "a.png",
		"mime_type": "image/png",
		"size": 2048,
		"dimensions": {"width": 640, "height": 480},
		"thumbnails": [
			{"size": "100x100", "url": "/api/v1/avatars/`+testID+`?size=100x100"},
			{"size": "300x300", "url": "/api/v1/avatars/`+testID+`?size=300x300"}
		],
		"upload_status": "uploaded",
		"processing_status": "completed",
		"created_at": "2026-01-02T03:04:05Z",
		"updated_at": "2026-01-02T03:05:05Z"
	}`, w.Body.String())
}

func TestMetadata_NotFound(t *testing.T) {
	h, avatars := newTestServer(t, nil)
	avatars.EXPECT().Get(mock.Anything, "missing").Return(domain.Avatar{}, domain.ErrAvatarNotFound).Once()

	w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/missing/metadata", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestList(t *testing.T) {
	t.Run("аватарки и миниатюры без готовых", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		pending := storedAvatar()
		pending.Thumbnails = nil
		avatars.EXPECT().List(mock.Anything, testUser).Return([]domain.Avatar{storedAvatar(), pending}, nil).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUser+"/avatars", nil))

		require.Equal(t, http.StatusOK, w.Code)
		got := decode[[]metadataResponse](t, w)
		require.Len(t, got, 2)
		assert.Len(t, got[0].Thumbnails, 2)
		assert.NotNil(t, got[1].Thumbnails)
		assert.Empty(t, got[1].Thumbnails)
	})

	t.Run("пусто - массив, а не null", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().List(mock.Anything, testUser).Return(nil, nil).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUser+"/avatars", nil))

		require.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `[]`, w.Body.String())
	})

	t.Run("сбой", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().List(mock.Anything, testUser).Return(nil, errBoom).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUser+"/avatars", nil))

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// deleteRequest формирует DELETE-запрос от пользователя userID.
func deleteRequest(path, userID string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, path, nil)
	if userID != "" {
		r.Header.Set(HeaderUserID, userID)
	}

	return r
}

func TestDelete(t *testing.T) {
	tests := []struct {
		err        error
		name       string
		userID     string
		wantBody   string
		wantStatus int
	}{
		{name: "своя", userID: testUser, wantStatus: http.StatusNoContent},
		{
			name: "чужая", userID: "intruder", err: domain.ErrForbidden, wantStatus: http.StatusForbidden,
			wantBody: `{"error":"Forbidden","details":"You can only delete your own avatars"}`,
		},
		{name: "не найдена", userID: testUser, err: domain.ErrAvatarNotFound, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, avatars := newTestServer(t, nil)
			avatars.EXPECT().Delete(mock.Anything, testID, tt.userID).Return(tt.err).Once()

			w := do(h, deleteRequest("/api/v1/avatars/"+testID, tt.userID))

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantBody != "" {
				assert.JSONEq(t, tt.wantBody, w.Body.String())
			}
		})
	}

	t.Run("без X-User-ID", func(t *testing.T) {
		h, _ := newTestServer(t, nil)

		w := do(h, deleteRequest("/api/v1/avatars/"+testID, ""))

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestDeleteUserAvatar(t *testing.T) {
	h, avatars := newTestServer(t, nil)
	avatars.EXPECT().DeleteLatest(mock.Anything, testUser, testUser).Return(nil).Once()
	avatars.EXPECT().DeleteLatest(mock.Anything, testUser, "intruder").Return(domain.ErrForbidden).Once()

	assert.Equal(t, http.StatusNoContent, do(h, deleteRequest("/api/v1/users/"+testUser+"/avatar", testUser)).Code)
	assert.Equal(t, http.StatusForbidden, do(h, deleteRequest("/api/v1/users/"+testUser+"/avatar", "intruder")).Code)
}

func TestHealth(t *testing.T) {
	t.Run("все компоненты доступны", func(t *testing.T) {
		db, s3 := NewMockChecker(t), NewMockChecker(t)
		db.EXPECT().Ping(mock.Anything).Return(nil).Once()
		s3.EXPECT().Ping(mock.Anything).Return(nil).Once()
		h, _ := newTestServer(t, map[string]Checker{"database": db, "storage": s3})

		w := do(h, httptest.NewRequest(http.MethodGet, "/health", nil))

		require.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"status":"ok","components":{"database":{"status":"ok"},"storage":{"status":"ok"}}}`, w.Body.String())
	})

	t.Run("компонент недоступен", func(t *testing.T) {
		db, broker := NewMockChecker(t), NewMockChecker(t)
		db.EXPECT().Ping(mock.Anything).Return(nil).Once()
		broker.EXPECT().Ping(mock.Anything).Return(errBoom).Once()
		h, _ := newTestServer(t, map[string]Checker{"database": db, "broker": broker})

		w := do(h, httptest.NewRequest(http.MethodGet, "/health", nil))

		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.JSONEq(t, `{"status":"degraded","components":{"database":{"status":"ok"},"broker":{"status":"unavailable"}}}`,
			w.Body.String())
	})

	t.Run("проверка ограничена по времени", func(t *testing.T) {
		slow := NewMockChecker(t)
		slow.EXPECT().Ping(mock.Anything).RunAndReturn(func(ctx context.Context) error {
			_, hasDeadline := ctx.Deadline()
			assert.True(t, hasDeadline)
			return nil
		}).Once()
		h, _ := newTestServer(t, map[string]Checker{"slow": slow})

		assert.Equal(t, http.StatusOK, do(h, httptest.NewRequest(http.MethodGet, "/health", nil)).Code)
	})
}

func TestRecoverer(t *testing.T) {
	h, avatars := newTestServer(t, nil)
	avatars.EXPECT().Get(mock.Anything, testID).RunAndReturn(func(context.Context, string) (domain.Avatar, error) {
		panic("бум")
	}).Once()

	w := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+testID+"/metadata", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
}

func TestEtagMatches(t *testing.T) {
	assert.True(t, etagMatches(`"a"`, `"a"`))
	assert.True(t, etagMatches(`"b", W/"a"`, `"a"`))
	assert.True(t, etagMatches(`*`, `"a"`))
	assert.False(t, etagMatches(``, `"a"`))
	assert.False(t, etagMatches(`"b"`, `"a"`))
}

func TestFileName(t *testing.T) {
	assert.Equal(t, "a.png", fileName("a.png"))
	assert.Equal(t, "a.png", fileName("/etc/a.png"))
	assert.Equal(t, "a.png", fileName(`C:\Users\me\a.png`))
	assert.Empty(t, fileName(""))
	assert.Len(t, []rune(fileName(strings.Repeat("я", 300))), 255)
}
