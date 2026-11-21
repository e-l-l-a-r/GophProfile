package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/handler"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
)

// okChecker - компонент, который всегда доступен.
type okChecker struct{}

func (okChecker) Ping(context.Context) error { return nil }

// mockInfra возвращает провайдер внешних компонентов на моках.
func mockInfra(t *testing.T) func() infra {
	return func() infra {
		repo := service.NewMockRepository(t)
		repo.EXPECT().ListAvatars(mock.Anything, "user-1").Return([]domain.Avatar{{ID: "a-1", UserID: "user-1"}}, nil).Maybe()

		return infra{
			Repository: repo,
			Files:      service.NewMockFileStorage(t),
			Publisher:  service.NewMockPublisher(t),
			Checkers:   map[string]handler.Checker{"database": okChecker{}},
		}
	}
}

// freeAddr возвращает свободный адрес локального TCP-порта.
func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return addr
}

// startServer запускает сервер через run и ждёт, пока он начнёт отвечать.
// Возвращает адрес и функцию остановки, возвращающую результат run.
func startServer(t *testing.T) (addr string, stop func() error) {
	t.Helper()

	cleanEnv(t)
	requiredEnv(t)
	addr = freeAddr(t)
	t.Setenv("ADDRESS", addr)
	t.Setenv("LOG_LEVEL", "error")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, mockInfra(t)) }()

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()

		return true
	}, 5*time.Second, 20*time.Millisecond, "сервер не начал принимать соединения")

	return addr, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("сервер не остановился")
			return nil
		}
	}
}

// get выполняет GET-запрос и возвращает статус и тело ответа.
func get(t *testing.T, url string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(body)
}

func TestRun_ServesAndStops(t *testing.T) {
	addr, stop := startServer(t)

	status, body := get(t, "http://"+addr+"/health")
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"status":"ok","components":{"database":{"status":"ok"}}}`, body)

	status, body = get(t, "http://"+addr+"/api/v1/users/user-1/avatars")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"id":"a-1"`)

	require.NoError(t, stop())

	_, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	require.Error(t, err, "после остановки порт свободен")
}

func TestRun_ConfigError(t *testing.T) {
	cleanEnv(t)

	err := run(context.Background(), nil, mockInfra(t))
	require.Error(t, err)
}

func TestRun_Help(t *testing.T) {
	cleanEnv(t)

	require.NoError(t, run(context.Background(), []string{"--help"}, mockInfra(t)))
}

func TestRun_AddressInUse(t *testing.T) {
	cleanEnv(t)
	requiredEnv(t)

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	t.Setenv("ADDRESS", ln.Addr().String())

	err = run(context.Background(), nil, mockInfra(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "не удалось занять адрес")
}

func TestExecute_Help(t *testing.T) {
	// С --help execute выводит сведения о сборке и справку, не запуская сервер.
	cleanEnv(t)
	require.NoError(t, execute([]string{"--help"}))
}
