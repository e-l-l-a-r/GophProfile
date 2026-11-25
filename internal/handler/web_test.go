package handler

import (
	"bytes"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
)

// webForm формирует форму веб-загрузки: поле userId и файл в поле image.
func webForm(t *testing.T, userID string, data []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// Файл идёт раньше userId: порядок полей в форме не важен.
	if data != nil {
		fw, err := mw.CreateFormFile("image", "a.png")
		require.NoError(t, err)
		_, err = fw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, mw.WriteField("userId", userID))
	require.NoError(t, mw.Close())

	r := httptest.NewRequest(http.MethodPost, "/web/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())

	return r
}

func TestWeb_Redirects(t *testing.T) {
	h, _ := newTestServer(t, nil)

	for _, path := range []string{"/", "/web"} {
		w := do(h, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusFound, w.Code, path)
		assert.Equal(t, "/web/upload", w.Header().Get("Location"))
	}
}

func TestWeb_UploadPage(t *testing.T) {
	h, _ := newTestServer(t, nil)

	w := do(h, httptest.NewRequest(http.MethodGet, "/web/upload", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, w.Body.String(), `id="uploadForm"`)
	assert.Contains(t, w.Body.String(), `action="/web/upload"`)
}

func TestWeb_Upload(t *testing.T) {
	t.Run("успех - переход в галерею", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Upload(mock.Anything, service.UploadInput{UserID: "user 1", FileName: "a.png", Data: []byte("img")}).
			Return(domain.Avatar{ID: testID}, nil).Once()

		w := do(h, webForm(t, " user 1 ", []byte("img")))

		assert.Equal(t, http.StatusSeeOther, w.Code)
		assert.Equal(t, "/web/gallery/user%201", w.Header().Get("Location"))
	})

	t.Run("пользователь из заголовка", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().Upload(mock.Anything, mock.MatchedBy(func(in service.UploadInput) bool {
			return in.UserID == testUser
		})).Return(domain.Avatar{ID: testID}, nil).Once()

		r := webForm(t, "", []byte("img"))
		r.Header.Set(HeaderUserID, testUser)
		w := do(h, r)

		assert.Equal(t, http.StatusSeeOther, w.Code)
	})

	tests := []struct {
		request    func(t *testing.T) *http.Request
		setup      func(m *MockAvatars)
		name       string
		wantText   string
		wantStatus int
	}{
		{
			name:       "нет пользователя",
			request:    func(t *testing.T) *http.Request { return webForm(t, "", []byte("img")) },
			wantStatus: http.StatusBadRequest,
			wantText:   "User ID is required",
		},
		{
			name:       "слишком длинный пользователь",
			request:    func(t *testing.T) *http.Request { return webForm(t, strings.Repeat("u", 256), []byte("img")) },
			wantStatus: http.StatusBadRequest,
			wantText:   "User ID is too long",
		},
		{
			name:       "нет файла",
			request:    func(t *testing.T) *http.Request { return webForm(t, testUser, nil) },
			wantStatus: http.StatusBadRequest,
			wantText:   "File is required",
		},
		{
			name: "файл слишком большой",
			request: func(t *testing.T) *http.Request {
				return webForm(t, testUser, bytes.Repeat([]byte("x"), testMaxSize+1))
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantText:   "Maximum file size: 1.0 KiB",
		},
		{
			name:    "неподдерживаемый формат",
			request: func(t *testing.T) *http.Request { return webForm(t, testUser, []byte("gif")) },
			setup: func(m *MockAvatars) {
				m.EXPECT().Upload(mock.Anything, mock.Anything).Return(domain.Avatar{}, domain.ErrInvalidImage).Once()
			},
			wantStatus: http.StatusBadRequest,
			wantText:   "Supported formats: jpeg, png, webp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, avatars := newTestServer(t, nil)
			if tt.setup != nil {
				tt.setup(avatars)
			}

			w := do(h, tt.request(t))

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
			assert.Contains(t, w.Body.String(), tt.wantText)
		})
	}
}

func TestWeb_Gallery(t *testing.T) {
	t.Run("аватарки пользователя", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		evil := storedAvatar()
		evil.FileName = `<script>alert(1)</script>.png`
		avatars.EXPECT().List(mock.Anything, testUser).Return([]domain.Avatar{storedAvatar(), evil}, nil).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/web/gallery/"+testUser, nil))

		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "2 avatar(s)")
		assert.Contains(t, body, `src="/api/v1/avatars/`+testID+`?size=300x300"`)
		assert.Contains(t, body, "640&times;480, 2.0 KiB")
		assert.Contains(t, body, "2026-01-02 03:04:05 UTC")
		assert.Contains(t, body, `data-delete="`+testID+`"`)
		assert.NotContains(t, body, "<script>alert(1)</script>", "имя файла экранируется")
	})

	t.Run("пусто", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().List(mock.Anything, testUser).Return(nil, nil).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/web/gallery/"+testUser, nil))

		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "No avatars yet.")
	})

	t.Run("сбой", func(t *testing.T) {
		h, avatars := newTestServer(t, nil)
		avatars.EXPECT().List(mock.Anything, testUser).Return(nil, errBoom).Once()

		w := do(h, httptest.NewRequest(http.MethodGet, "/web/gallery/"+testUser, nil))

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "Internal server error")
	})
}

func TestWeb_TemplateError(t *testing.T) {
	broken := template.Must(template.New("gallery.html").Parse(`{{.Missing.Field}}`))
	avatars := NewMockAvatars(t)
	avatars.EXPECT().List(mock.Anything, testUser).Return(nil, nil).Once()
	h := New(avatars, Web{Static: fstest.MapFS{}, Templates: broken}, Config{MaxUploadSize: testMaxSize}, logger.Nop()).Routes()

	w := do(h, httptest.NewRequest(http.MethodGet, "/web/gallery/"+testUser, nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "Internal server error\n", w.Body.String(), "полуготовая страница не отдаётся")
}

func TestHumanSize(t *testing.T) {
	assert.Equal(t, "512 B", humanSize(512))
	assert.Equal(t, "1.5 KiB", humanSize(1536))
	assert.Equal(t, "10.0 MiB", humanSize(10<<20))
}
