package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workerEnvVars - все переменные окружения, которые читает воркер.
var workerEnvVars = []string{
	"LOG_LEVEL", "CONCURRENCY", "MAX_RETRIES", "DATABASE_DSN", "RABBITMQ_URL",
	"S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_BUCKET", "S3_USE_SSL", "CONFIG",
}

// cleanEnv удаляет переменные окружения воркера и восстанавливает их по
// завершении теста.
func cleanEnv(t *testing.T) {
	t.Helper()

	for _, name := range workerEnvVars {
		t.Setenv(name, "") // регистрирует восстановление исходного значения
		require.NoError(t, os.Unsetenv(name))
	}
}

// requiredEnv задаёт минимальный набор обязательных настроек.
func requiredEnv(t *testing.T) {
	t.Helper()

	t.Setenv("DATABASE_DSN", "postgres://localhost/db")
	t.Setenv("RABBITMQ_URL", "amqp://localhost/")
	t.Setenv("S3_ENDPOINT", "localhost:9000")
	t.Setenv("S3_ACCESS_KEY", "access")
	t.Setenv("S3_SECRET_KEY", "secret")
}

func TestGetConfig_Defaults(t *testing.T) {
	cleanEnv(t)
	requiredEnv(t)

	got, err := getConfig(nil, &bytes.Buffer{})
	require.NoError(t, err)

	assert.Equal(t, Config{
		LogLevel:    defaultLogLevel,
		DatabaseDSN: "postgres://localhost/db",
		RabbitMQURL: "amqp://localhost/",
		S3Endpoint:  "localhost:9000",
		S3AccessKey: "access",
		S3SecretKey: "secret",
		S3Bucket:    defaultS3Bucket,
		Concurrency: defaultConcurrency,
		MaxRetries:  defaultMaxRetries,
	}, got)
}

func TestGetConfig_Priority(t *testing.T) {
	cleanEnv(t)
	requiredEnv(t)
	path := filepath.Join(t.TempDir(), "worker.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"concurrency": 8, "max_retries": 2, "log_level": "warn"}`), 0o600))
	t.Setenv("CONFIG", path)
	t.Setenv("MAX_RETRIES", "7")

	got, err := getConfig([]string{"--concurrency", "3"}, &bytes.Buffer{})
	require.NoError(t, err)

	assert.Equal(t, 3, got.Concurrency, "флаг важнее файла")
	assert.Equal(t, 7, got.MaxRetries, "окружение важнее файла")
	assert.Equal(t, "warn", got.LogLevel, "файл важнее значения по умолчанию")
	assert.Equal(t, path, got.ConfigPath)
}

func TestGetConfig_Errors(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T)
		name  string
		args  []string
	}{
		{name: "нет обязательных настроек", setup: func(*testing.T) {}},
		{name: "нулевое число обработчиков", setup: requiredEnv, args: []string{"--concurrency", "0"}},
		{name: "нулевое число попыток", setup: requiredEnv, args: []string{"--max-retries", "0"}},
		{name: "пустой бакет", setup: requiredEnv, args: []string{"--s3-bucket", ""}},
		{name: "неизвестный флаг", setup: requiredEnv, args: []string{"--unknown"}},
		{name: "некорректное окружение", setup: func(t *testing.T) { requiredEnv(t); t.Setenv("CONCURRENCY", "x") }},
		{name: "нет файла конфигурации", setup: requiredEnv, args: []string{"-c", "/nonexistent.json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanEnv(t)
			tt.setup(t)

			_, err := getConfig(tt.args, &bytes.Buffer{})
			require.Error(t, err)
		})
	}
}

func TestRun_ConfigErrors(t *testing.T) {
	cleanEnv(t)
	require.NoError(t, run(context.Background(), []string{"--help"}), "справка - не ошибка")
	require.Error(t, run(context.Background(), nil))

	requiredEnv(t)
	t.Setenv("LOG_LEVEL", "громко")
	require.Error(t, run(context.Background(), nil), "некорректный уровень логирования")
}

func TestRun_DatabaseUnavailable(t *testing.T) {
	cleanEnv(t)
	requiredEnv(t)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("DATABASE_DSN", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")

	require.Error(t, run(context.Background(), nil))
}
