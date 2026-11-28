package handler

import (
	"context"
	"net/http"
	"sync"

	"go.uber.org/zap"
)

const (
	// statusOK - компонент или сервис работает.
	statusOK = "ok"
	// statusUnavailable - компонент недоступен.
	statusUnavailable = "unavailable"
	// statusDegraded - сервис работает не полностью: часть компонентов недоступна.
	statusDegraded = "degraded"
)

// componentStatus - состояние компонента в ответе /health.
type componentStatus struct {
	Status string `json:"status"`
}

// healthResponse - ответ /health.
type healthResponse struct {
	Components map[string]componentStatus `json:"components"`
	Status     string                     `json:"status"`
}

// health обрабатывает GET /health: параллельно проверяет все компоненты и
// отвечает 200, если все доступны, иначе 503. Причины недоступности пишутся в
// лог, а не в ответ.
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{Components: make(map[string]componentStatus, len(h.conf.Checkers)), Status: statusOK}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for name, checker := range h.conf.Checkers {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(r.Context(), h.conf.HealthTimeout)
			defer cancel()

			status := statusOK
			if err := checker.Ping(ctx); err != nil {
				status = statusUnavailable
				h.log.Ctx(ctx).Warn("компонент недоступен", zap.String("component", name), zap.Error(err))
			}

			mu.Lock()
			defer mu.Unlock()
			resp.Components[name] = componentStatus{Status: status}
			if status != statusOK {
				resp.Status = statusDegraded
			}
		})
	}
	wg.Wait()

	code := http.StatusOK
	if resp.Status != statusOK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, resp)
}
