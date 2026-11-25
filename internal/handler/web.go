package handler

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
)

// Web - ресурсы веб-интерфейса.
type Web struct {
	// Static - статические файлы; index.html - страница загрузки.
	Static fs.FS
	// Templates - шаблоны gallery.html и error.html.
	Templates *template.Template
}

// galleryItem - аватарка на странице галереи.
type galleryItem struct {
	ID           string
	URL          string
	ThumbnailURL string
	MetadataURL  string
	FileName     string
	Size         string
	CreatedAt    string
	Status       string
	Width        int
	Height       int
}

// galleryPage - данные страницы галереи.
type galleryPage struct {
	UserID  string
	Avatars []galleryItem
}

// errorPage - данные страницы ошибки.
type errorPage struct {
	Message string
	Details string
	Status  int
}

// webRoutes регистрирует маршруты веб-интерфейса.
func (h *Handler) webRoutes(r chi.Router) {
	r.Get("/", h.redirect("/web/upload"))
	r.Get("/web", h.redirect("/web/upload"))
	r.Get("/web/upload", h.uploadPage)
	r.Post("/web/upload", h.webUpload)
	r.Get("/web/gallery/{userID}", h.gallery)
}

// redirect возвращает обработчик, перенаправляющий на target.
func (h *Handler) redirect(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// uploadPage обрабатывает GET /web/upload - страницу загрузки.
func (h *Handler) uploadPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, h.web.Static, "index.html")
}

// webUpload обрабатывает POST /web/upload - отправку формы без JavaScript.
// Пользователь берётся из поля userId (или заголовка X-User-ID); после
// успешной загрузки браузер перенаправляется в галерею пользователя.
func (h *Handler) webUpload(w http.ResponseWriter, r *http.Request) {
	form, err := h.readUpload(w, r)
	if err != nil {
		status, resp := h.classifyUploadError(r, err)
		h.renderError(w, r, status, resp)
		return
	}

	userID := strings.TrimSpace(form.userID)
	if userID == "" {
		userID = strings.TrimSpace(r.Header.Get(HeaderUserID))
	}
	switch {
	case userID == "":
		h.renderError(w, r, http.StatusBadRequest, errorResponse{Error: "User ID is required"})
		return
	case len(userID) > maxUserIDLength:
		h.renderError(w, r, http.StatusBadRequest, errorResponse{Error: "User ID is too long"})
		return
	}

	in := form.input
	in.UserID = userID
	if _, err := h.avatars.Upload(r.Context(), in); err != nil {
		status, resp := classifyError(r, h.log, err)
		h.renderError(w, r, status, resp)
		return
	}

	http.Redirect(w, r, GalleryURL(userID), http.StatusSeeOther)
}

// GalleryURL возвращает адрес галереи пользователя userID.
func GalleryURL(userID string) string {
	return "/web/gallery/" + url.PathEscape(userID)
}

// gallery обрабатывает GET /web/gallery/{userID} - галерею аватарок
// пользователя.
func (h *Handler) gallery(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")

	avatars, err := h.avatars.List(r.Context(), userID)
	if err != nil {
		status, resp := classifyError(r, h.log, err)
		h.renderError(w, r, status, resp)
		return
	}

	page := galleryPage{UserID: userID, Avatars: make([]galleryItem, 0, len(avatars))}
	for _, a := range avatars {
		page.Avatars = append(page.Avatars, newGalleryItem(a))
	}

	h.render(w, r, http.StatusOK, "gallery.html", page)
}

// newGalleryItem готовит аватарку к показу в галерее. Миниатюра 300x300
// отдаётся сервером и до завершения обработки: тогда она строится на лету.
func newGalleryItem(a domain.Avatar) galleryItem {
	return galleryItem{
		ID:           a.ID,
		URL:          AvatarURL(a.ID),
		ThumbnailURL: ThumbnailURL(a.ID, imageproc.ThumbnailSizes[len(imageproc.ThumbnailSizes)-1].Name),
		MetadataURL:  AvatarURL(a.ID) + "/metadata",
		FileName:     a.FileName,
		Size:         humanSize(a.SizeBytes),
		CreatedAt:    a.CreatedAt.UTC().Format(time.DateTime + " UTC"),
		Status:       string(a.ProcessingStatus),
		Width:        a.Width,
		Height:       a.Height,
	}
}

// humanSize форматирует размер в байтах для человека.
func humanSize(n int64) string {
	const unit = 1024
	switch {
	case n < unit:
		return fmt.Sprintf("%d B", n)
	case n < unit*unit:
		return fmt.Sprintf("%.1f KiB", float64(n)/unit)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(unit*unit))
	}
}

// renderError показывает страницу ошибки.
func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, status int, resp errorResponse) {
	details := resp.Details
	if resp.MaxSize != nil {
		details = "Maximum file size: " + humanSize(*resp.MaxSize)
	}

	h.render(w, r, status, "error.html", errorPage{Status: status, Message: resp.Error, Details: details})
}

// render выполняет шаблон name и отвечает статусом status. Шаблон сначала
// выполняется в буфер, чтобы ошибка шаблона не оставила полуготовую страницу.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := h.web.Templates.ExecuteTemplate(&buf, name, data); err != nil {
		h.log.Error("ошибка шаблона", zap.String("шаблон", name), zap.String("путь", r.URL.Path), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
