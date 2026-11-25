// Package handler реализует HTTP-интерфейс сервиса аватарок: REST API
// /api/v1, веб-интерфейс /web, проверку работоспособности /health и
// общие middleware.
package handler

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
)

// Avatars - бизнес-логика аватарок, которую использует HTTP-интерфейс.
type Avatars interface {
	Upload(ctx context.Context, in service.UploadInput) (domain.Avatar, error)
	Get(ctx context.Context, id string) (domain.Avatar, error)
	Latest(ctx context.Context, userID string) (domain.Avatar, error)
	List(ctx context.Context, userID string) ([]domain.Avatar, error)
	Delete(ctx context.Context, id, requesterID string) error
	DeleteLatest(ctx context.Context, userID, requesterID string) error
	Image(ctx context.Context, id string, req service.ImageRequest) (service.Image, error)
}

// Checker проверяет доступность внешнего компонента (база, хранилище, брокер).
type Checker interface {
	Ping(ctx context.Context) error
}

// Config - параметры HTTP-интерфейса.
type Config struct {
	// Checkers - проверяемые /health компоненты по имени.
	Checkers map[string]Checker
	// MaxUploadSize - предельный размер загружаемого файла в байтах.
	MaxUploadSize int64
	// HealthTimeout - время на проверку одного компонента.
	HealthTimeout time.Duration
}

// Handler - HTTP-интерфейс сервиса аватарок.
type Handler struct {
	avatars Avatars
	log     *logger.Logger
	web     Web
	conf    Config
}

// New создаёт HTTP-интерфейс с веб-интерфейсом из ресурсов web.
func New(avatars Avatars, web Web, conf Config, log *logger.Logger) *Handler {
	return &Handler{avatars: avatars, web: web, conf: conf, log: log}
}

// Routes возвращает маршрутизатор со всеми маршрутами и middleware.
func (h *Handler) Routes() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, requestLogger(h.log), recoverer(h.log))

	r.Get("/health", h.health)
	h.webRoutes(r)

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/avatars", func(r chi.Router) {
			r.With(requireUserID).Post("/", h.upload)
			r.Get("/{avatarID}", h.image)
			r.Get("/{avatarID}/metadata", h.metadata)
			r.With(requireUserID).Delete("/{avatarID}", h.deleteAvatar)
		})
		r.Route("/users/{userID}", func(r chi.Router) {
			r.Get("/avatar", h.userImage)
			r.Get("/avatars", h.list)
			r.With(requireUserID).Delete("/avatar", h.deleteUserAvatar)
		})
	})

	return r
}
