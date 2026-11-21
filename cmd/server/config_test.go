package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverEnvVars - все переменные окружения, которые читает сервер.
var serverEnvVars = []string{
	"ADDRESS", "LOG_LEVEL", "MAX_UPLOAD_SIZE", "SHUTDOWN_TIMEOUT", "DATABASE_DSN", "RABBITMQ_URL",
	"S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_BUCKET", "S3_USE_SSL", "CONFIG",
}

// cleanEnv удаляет переменные окружения сервера и восстанавливает их по
// завершении теста. t.Setenv с пустым значением не подходит: переменная
// остаётся заданной.
func cleanEnv(t *testing.T) {
	t.Helper()

	for _, name := range serverEnvVars {
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

// writeConfigFile создаёт JSON-файл конфигурации и возвращает путь к нему.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestGetConfig_Defaults(t *testing.T) {
	cleanEnv(t)
	requiredEnv(t)

	got, err := getConfig(nil, &bytes.Buffer{})
	require.NoError(t, err)

	assert.Equal(t, Config{
		Address:         defaultAddress,
		LogLevel:        defaultLogLevel,
		DatabaseDSN:     "postgres://localhost/db",
		RabbitMQURL:     "amqp://localhost/",
		S3Endpoint:      "localhost:9000",
		S3AccessKey:     "access",
		S3SecretKey:     "secret",
		S3Bucket:        defaultS3Bucket,
		MaxUploadSize:   defaultMaxUploadSize,
		ShutdownTimeout: defaultShutdownTimeout,
	}, got)
}

func TestGetConfig_Priority(t *testing.T) {
	cleanEnv(t)
	path := writeConfigFile(t, `{
		"address": "file:1",
		"log_level": "warn",
		"max_upload_size": 100,
		"shutdown_timeout": 7,
		"database_dsn": "postgres://file",
		"rabbitmq_url": "amqp://file",
		"s3_endpoint": "file:9000",
		"s3_access_key": "file-access",
		"s3_secret_key": "file-secret",
		"s3_bucket": "file-bucket",
		"s3_use_ssl": true
	}`)
	t.Setenv("CONFIG", path)
	t.Setenv("ADDRESS", "env:1")
	t.Setenv("S3_SECRET_KEY", "env-secret")

	got, err := getConfig([]string{"-a", "flag:1", "-l", "debug", "--s3-bucket", "flag-bucket", "--max-upload-size", "200"}, &bytes.Buffer{})
	require.NoError(t, err)

	assert.Equal(t, Config{
		ConfigPath:      path,
		Address:         "env:1",
		LogLevel:        "debug",
		DatabaseDSN:     "postgres://file",
		RabbitMQURL:     "amqp://file",
		S3Endpoint:      "file:9000",
		S3AccessKey:     "file-access",
		S3SecretKey:     "env-secret",
		S3Bucket:        "flag-bucket",
		MaxUploadSize:   200,
		ShutdownTimeout: 7 * time.Second,
		S3UseSSL:        true,
	}, got)
}

func TestGetConfig_Errors(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T)
		name  string
		args  []string
	}{
		{name: "нет обязательных настроек", setup: func(*testing.T) {}},
		{name: "неизвестный флаг", setup: requiredEnv, args: []string{"--unknown"}},
		{
			name:  "некорректное окружение",
			setup: func(t *testing.T) { requiredEnv(t); t.Setenv("MAX_UPLOAD_SIZE", "много") },
		},
		{
			name:  "нулевой размер файла",
			setup: requiredEnv,
			args:  []string{"--max-upload-size", "0"},
		},
		{
			name:  "нулевое время завершения",
			setup: requiredEnv,
			args:  []string{"--shutdown-timeout", "0s"},
		},
		{
			name:  "нет файла конфигурации",
			setup: requiredEnv,
			args:  []string{"-c", "/nonexistent/config.json"},
		},
		{
			name:  "пустые адрес и бакет",
			setup: requiredEnv,
			args:  []string{"-a", "", "--s3-bucket", ""},
		},
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

func TestGetConfig_Help(t *testing.T) {
	cleanEnv(t)

	var out bytes.Buffer
	_, err := getConfig([]string{"--help"}, &out)
	require.Error(t, err)
	assert.Contains(t, out.String(), "S3_SECRET_KEY")
}
