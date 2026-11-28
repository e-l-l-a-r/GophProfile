package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/logger"
)

// HeaderUserID - заголовок с идентификатором пользователя, выполняющего запрос.
const HeaderUserID = "X-User-ID"

// maxUserIDLength - предельная длина идентификатора пользователя (размер
// столбца user_id в базе).
const maxUserIDLength = 255

// userIDKey - ключ идентификатора пользователя в контексте запроса.
type userIDKey struct{}

// userIDFromContext возвращает идентификатор пользователя, сохранённый
// requireUserID.
func userIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey{}).(string)

	return id
}

// requireUserID требует заголовок X-User-ID и сохраняет его значение в
// контексте запроса. Без заголовка запрос отклоняется с кодом 401.
func requireUserID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(HeaderUserID))
		switch {
		case id == "":
			writeError(w, http.StatusUnauthorized, "Unauthorized", "Header "+HeaderUserID+" is required")
			return
		case len(id) > maxUserIDLength:
			writeError(w, http.StatusBadRequest, "Invalid parameter", "Header "+HeaderUserID+" is too long")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, id)))
	})
}

func traceRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		// Шаблон маршрута известен только после прохождения роутера.
		if route := chi.RouteContext(r.Context()).RoutePattern(); route != "" {
			span := trace.SpanFromContext(r.Context())
			span.SetName(r.Method + " " + route)
			span.SetAttributes(semconv.HTTPRoute(route))
			// otelhttp не знает шаблона chi; без метки route метрики не разделить по маршрутам.
			if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
				labeler.Add(semconv.HTTPRoute(route))
			}
		}
	})
}

// requestLogger логирует каждый запрос: метод, путь, статус, размер ответа и
// длительность.
func requestLogger(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			log.Ctx(r.Context()).Info("запрос обработан",
				zap.String("method", r.Method),
				zap.String("path", r.URL.RequestURI()),
				zap.Int("status", ww.Status()),
				zap.Int("size", ww.BytesWritten()),
				zap.Duration("duration", time.Since(start)),
				zap.String("request", middleware.GetReqID(r.Context())),
			)
		})
	}
}

// recoverer превращает панику обработчика в ответ 500 и запись в лог.
func recoverer(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// Прерывание ответа через ErrAbortHandler обрабатывает сам net/http.
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}
					log.Ctx(r.Context()).Error("паника при обработке запроса",
						zap.String("method", r.Method), zap.String("path", r.URL.Path), zap.Any("panic", rec), zap.Stack("stack"))
					writeError(w, http.StatusInternalServerError, "Internal server error", "")
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
