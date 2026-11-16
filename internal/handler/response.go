package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
)

// errorResponse - тело ответа с ошибкой.
type errorResponse struct {
	// MaxSize - предельный размер файла; только для ответа 413.
	MaxSize *int64 `json:"max_size,omitempty"`
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// writeJSON отвечает статусом status и телом body в формате JSON.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError отвечает ошибкой со статусом status.
func writeError(w http.ResponseWriter, status int, message, details string) {
	writeJSON(w, status, errorResponse{Error: message, Details: details})
}

// classifyError сопоставляет ошибке сервиса HTTP-статус и описание для
// клиента. Неизвестные ошибки логируются и скрываются за 500.
func classifyError(r *http.Request, log *logger.Logger, err error) (status int, resp errorResponse) {
	switch {
	case errors.Is(err, domain.ErrAvatarNotFound):
		return http.StatusNotFound, errorResponse{Error: "Avatar not found"}
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, errorResponse{Error: "Forbidden", Details: "You can only delete your own avatars"}
	case errors.Is(err, domain.ErrInvalidImage):
		return http.StatusBadRequest, errorResponse{Error: "Invalid file format", Details: "Supported formats: jpeg, png, webp"}
	case errors.Is(err, domain.ErrInvalidParameter):
		return http.StatusBadRequest, errorResponse{Error: "Invalid parameter", Details: err.Error()}
	default:
		log.Error("ошибка обработки запроса",
			zap.String("метод", r.Method), zap.String("путь", r.URL.Path), zap.Error(err))
		return http.StatusInternalServerError, errorResponse{Error: "Internal server error"}
	}
}

// writeServiceError отвечает ошибкой сервиса в формате JSON.
func writeServiceError(w http.ResponseWriter, r *http.Request, log *logger.Logger, err error) {
	status, resp := classifyError(r, log, err)
	writeJSON(w, status, resp)
}
