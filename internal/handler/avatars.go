package handler

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
)

// multipartOverhead - запас на заголовки и границы multipart поверх
// предельного размера файла.
const multipartOverhead = 1 << 20

// cacheMaxAge - время кеширования изображений клиентом. Содержимое аватарки
// не меняется, поэтому кешировать можно надолго.
const cacheMaxAge = 24 * time.Hour

// fileFields - имена поля формы с файлом: "file" по спецификации API и "image",
// которое использует веб-интерфейс.
var fileFields = []string{"file", "image"}

// userIDField - поле веб-формы с идентификатором пользователя.
const userIDField = "userId"

// errFileTooLarge - файл больше предельного размера.
var errFileTooLarge = errors.New("файл слишком большой")

// errNoFile - в форме нет файла.
var errNoFile = errors.New("в форме нет файла")

// uploadResponse - ответ на загрузку аватарки.
type uploadResponse struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
}

// dimensions - размеры изображения.
type dimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// thumbnail - миниатюра в ответе с метаданными.
type thumbnail struct {
	Size string `json:"size"`
	URL  string `json:"url"`
}

// metadataResponse - метаданные аватарки.
type metadataResponse struct {
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	ID               string      `json:"id"`
	UserID           string      `json:"user_id"`
	URL              string      `json:"url"`
	FileName         string      `json:"file_name"`
	MimeType         string      `json:"mime_type"`
	UploadStatus     string      `json:"upload_status"`
	ProcessingStatus string      `json:"processing_status"`
	Thumbnails       []thumbnail `json:"thumbnails"`
	Dimensions       dimensions  `json:"dimensions"`
	Size             int64       `json:"size"`
}

// AvatarURL возвращает адрес изображения аватарки id.
func AvatarURL(id string) string {
	return "/api/v1/avatars/" + id
}

// ThumbnailURL возвращает адрес миниатюры аватарки id размера size.
func ThumbnailURL(id, size string) string {
	return AvatarURL(id) + "?size=" + size
}

// newMetadata формирует метаданные аватарки для ответа.
func newMetadata(a domain.Avatar) metadataResponse {
	thumbnails := []thumbnail{}
	for _, size := range imageproc.ThumbnailSizes {
		if _, ok := a.Thumbnails[size.Name]; ok {
			thumbnails = append(thumbnails, thumbnail{Size: size.Name, URL: ThumbnailURL(a.ID, size.Name)})
		}
	}

	return metadataResponse{
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
		ID:               a.ID,
		UserID:           a.UserID,
		URL:              AvatarURL(a.ID),
		FileName:         a.FileName,
		MimeType:         a.MimeType,
		UploadStatus:     string(a.UploadStatus),
		ProcessingStatus: string(a.ProcessingStatus),
		Thumbnails:       thumbnails,
		Dimensions:       dimensions{Width: a.Width, Height: a.Height},
		Size:             a.SizeBytes,
	}
}

// upload обрабатывает POST /api/v1/avatars.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	form, err := h.readUpload(w, r)
	if err != nil {
		status, resp := h.classifyUploadError(r, err)
		writeJSON(w, status, resp)
		return
	}
	in := form.input
	in.UserID = userIDFromContext(r.Context())

	avatar, err := h.avatars.Upload(r.Context(), in)
	if err != nil {
		writeServiceError(w, r, h.log, err)
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{
		CreatedAt: avatar.CreatedAt,
		ID:        avatar.ID,
		UserID:    avatar.UserID,
		URL:       AvatarURL(avatar.ID),
		Status:    string(domain.ProcessingStatusProcessing),
	})
}

// uploadForm - прочитанная форма загрузки.
type uploadForm struct {
	// userID - значение поля userId (только веб-форма).
	userID string
	input  service.UploadInput
}

// readUpload читает форму multipart/form-data потоком, не сохраняя её на
// диск: файл из поля file (или image) и поле userId; порядок частей
// любой. Возвращает errFileTooLarge, если файл больше MaxUploadSize, и
// errNoFile, если файла в форме нет.
func (h *Handler) readUpload(w http.ResponseWriter, r *http.Request) (uploadForm, error) {
	r.Body = http.MaxBytesReader(w, r.Body, h.conf.MaxUploadSize+multipartOverhead)

	mr, err := r.MultipartReader()
	if err != nil {
		return uploadForm{}, err
	}

	var form uploadForm
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return uploadForm{}, err
		}

		err = h.readPart(part, &form)
		_ = part.Close()
		if err != nil {
			return uploadForm{}, err
		}
	}

	if len(form.input.Data) == 0 {
		return uploadForm{}, errNoFile
	}

	return form, nil
}

// readPart читает одну часть формы в form; неизвестные части пропускаются.
func (h *Handler) readPart(part *multipart.Part, form *uploadForm) error {
	switch {
	case isFileField(part) && form.input.Data == nil:
		data, err := io.ReadAll(io.LimitReader(part, h.conf.MaxUploadSize+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > h.conf.MaxUploadSize {
			return errFileTooLarge
		}
		form.input = service.UploadInput{FileName: fileName(part.FileName()), Data: data}
	case part.FormName() == userIDField:
		value, err := io.ReadAll(io.LimitReader(part, maxUserIDLength+1))
		if err != nil {
			return err
		}
		form.userID = string(value)
	}

	return nil
}

// isFileField сообщает, что часть формы содержит файл аватарки.
func isFileField(part *multipart.Part) bool {
	for _, name := range fileFields {
		if part.FormName() == name {
			return true
		}
	}

	return false
}

// fileName очищает имя файла от пути и ограничивает его длину размером
// столбца базы.
func fileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		name = ""
	}
	if r := []rune(name); len(r) > 255 {
		name = string(r[:255])
	}

	return name
}

// classifyUploadError сопоставляет ошибке чтения формы загрузки HTTP-статус и
// описание для клиента.
func (h *Handler) classifyUploadError(r *http.Request, err error) (int, errorResponse) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok || errors.Is(err, errFileTooLarge) {
		maxSize := h.conf.MaxUploadSize
		return http.StatusRequestEntityTooLarge, errorResponse{Error: "File too large", MaxSize: &maxSize}
	}
	if errors.Is(err, errNoFile) {
		return http.StatusBadRequest, errorResponse{Error: "File is required", Details: "Send the image in the form field \"file\""}
	}

	h.log.Debug("некорректная форма загрузки", zap.String("путь", r.URL.Path), zap.Error(err))

	return http.StatusBadRequest, errorResponse{Error: "Invalid request", Details: "Expected multipart/form-data with field \"file\""}
}

// image обрабатывает GET /api/v1/avatars/{avatarID}.
func (h *Handler) image(w http.ResponseWriter, r *http.Request) {
	h.serveImage(w, r, chi.URLParam(r, "avatarID"))
}

// userImage обрабатывает GET /api/v1/users/{userID}/avatar - самую новую
// аватарку пользователя.
func (h *Handler) userImage(w http.ResponseWriter, r *http.Request) {
	avatar, err := h.avatars.Latest(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		writeServiceError(w, r, h.log, err)
		return
	}

	// Самая новая аватарка меняется, поэтому ответ кешируется ненадолго.
	h.serveImageWithCache(w, r, avatar.ID, "no-cache")
}

// serveImage отдаёт изображение аватарки id с долгим кешированием.
func (h *Handler) serveImage(w http.ResponseWriter, r *http.Request, id string) {
	h.serveImageWithCache(w, r, id, "public, max-age="+strconv.Itoa(int(cacheMaxAge.Seconds())))
}

// serveImageWithCache отдаёт изображение аватарки id нужного размера и
// формата с заголовком Cache-Control cacheControl. Если метка If-None-Match
// совпадает, отвечает 304 без тела.
func (h *Handler) serveImageWithCache(w http.ResponseWriter, r *http.Request, id, cacheControl string) {
	img, err := h.avatars.Image(r.Context(), id, service.ImageRequest{
		Size:   r.URL.Query().Get("size"),
		Format: r.URL.Query().Get("format"),
	})
	if err != nil {
		writeServiceError(w, r, h.log, err)
		return
	}
	defer func() { _ = img.Body.Close() }()

	w.Header().Set("ETag", img.ETag)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Vary", "Accept-Encoding")
	if etagMatches(r.Header.Get("If-None-Match"), img.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", img.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(img.Size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, img.Body); err != nil {
		h.log.Debug("передача изображения прервана", zap.String("аватарка", id), zap.Error(err))
	}
}

// etagMatches сообщает, совпадает ли метка etag с одной из меток заголовка
// If-None-Match (или заголовок равен "*").
func etagMatches(header, etag string) bool {
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == "*" || candidate == etag {
			return true
		}
	}

	return false
}

// metadata обрабатывает GET /api/v1/avatars/{avatarID}/metadata.
func (h *Handler) metadata(w http.ResponseWriter, r *http.Request) {
	avatar, err := h.avatars.Get(r.Context(), chi.URLParam(r, "avatarID"))
	if err != nil {
		writeServiceError(w, r, h.log, err)
		return
	}

	writeJSON(w, http.StatusOK, newMetadata(avatar))
}

// list обрабатывает GET /api/v1/users/{userID}/avatars.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	avatars, err := h.avatars.List(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		writeServiceError(w, r, h.log, err)
		return
	}

	resp := make([]metadataResponse, 0, len(avatars))
	for _, avatar := range avatars {
		resp = append(resp, newMetadata(avatar))
	}
	writeJSON(w, http.StatusOK, resp)
}

// deleteAvatar обрабатывает DELETE /api/v1/avatars/{avatarID}.
func (h *Handler) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	err := h.avatars.Delete(r.Context(), chi.URLParam(r, "avatarID"), userIDFromContext(r.Context()))
	if err != nil {
		writeServiceError(w, r, h.log, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// deleteUserAvatar обрабатывает DELETE /api/v1/users/{userID}/avatar.
func (h *Handler) deleteUserAvatar(w http.ResponseWriter, r *http.Request) {
	err := h.avatars.DeleteLatest(r.Context(), chi.URLParam(r, "userID"), userIDFromContext(r.Context()))
	if err != nil {
		writeServiceError(w, r, h.log, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
