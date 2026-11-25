package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func TestDuration_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "строка с секундами", input: `"1s"`, want: time.Second},
		{name: "строка с миллисекундами", input: `"300ms"`, want: 300 * time.Millisecond},
		{name: "число секунд", input: `5`, want: 5 * time.Second},
		{name: "дробное число секунд", input: `1.5`, want: 1500 * time.Millisecond},
		{name: "некорректная строка", input: `"abc"`, wantErr: true},
		{name: "булево значение", input: `true`, wantErr: true},
		{name: "некорректный JSON", input: `{`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalJSON([]byte(tt.input))
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, d.Std())
		})
	}
}

func TestLoad(t *testing.T) {
	type sample struct {
		Address *string   `json:"address"`
		Timeout *Duration `json:"timeout"`
	}

	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.json")
	require.NoError(t, os.WriteFile(validPath, []byte(`{"address":"localhost:1","timeout":"2s"}`), 0o600))
	brokenPath := filepath.Join(dir, "broken.json")
	require.NoError(t, os.WriteFile(brokenPath, []byte(`{`), 0o600))

	t.Run("пустой путь", func(t *testing.T) {
		var got sample
		require.NoError(t, Load("", &got))
		assert.Nil(t, got.Address)
	})

	t.Run("корректный файл", func(t *testing.T) {
		var got sample
		require.NoError(t, Load(validPath, &got))
		require.NotNil(t, got.Address)
		assert.Equal(t, "localhost:1", *got.Address)
		require.NotNil(t, got.Timeout)
		assert.Equal(t, 2*time.Second, got.Timeout.Std())
	})

	t.Run("файл не существует", func(t *testing.T) {
		var got sample
		require.Error(t, Load(filepath.Join(dir, "missing.json"), &got))
	})

	t.Run("некорректный JSON", func(t *testing.T) {
		var got sample
		require.Error(t, Load(brokenPath, &got))
	})
}

func TestResolveString(t *testing.T) {
	tests := []struct {
		file        *string
		name        string
		env         string
		flag        string
		want        string
		flagChanged bool
	}{
		{name: "окружение важнее всего", env: "env", flagChanged: true, flag: "flag", file: ptr("file"), want: "env"},
		{name: "флаг важнее файла", flagChanged: true, flag: "flag", file: ptr("file"), want: "flag"},
		{name: "файл важнее значения по умолчанию", flag: "default", file: ptr("file"), want: "file"},
		{name: "значение по умолчанию", flag: "default", want: "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolveString(tt.env, tt.flagChanged, tt.flag, tt.file))
		})
	}
}

func TestResolveBool(t *testing.T) {
	tests := []struct {
		env         *bool
		file        *bool
		name        string
		flagChanged bool
		flag        bool
		want        bool
	}{
		{name: "окружение false важнее всего", env: ptr(false), flagChanged: true, flag: true, file: ptr(true), want: false},
		{name: "флаг важнее файла", flagChanged: true, flag: true, file: ptr(false), want: true},
		{name: "файл важнее значения по умолчанию", flag: false, file: ptr(true), want: true},
		{name: "значение по умолчанию", flag: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolveBool(tt.env, tt.flagChanged, tt.flag, tt.file))
		})
	}
}

func TestResolveNumber(t *testing.T) {
	tests := []struct {
		env         *int64
		file        *int64
		name        string
		flag        int64
		want        int64
		flagChanged bool
	}{
		{name: "окружение 0 важнее всего", env: ptr(int64(0)), flagChanged: true, flag: 2, file: ptr(int64(3)), want: 0},
		{name: "флаг важнее файла", flagChanged: true, flag: 2, file: ptr(int64(3)), want: 2},
		{name: "файл важнее значения по умолчанию", flag: 2, file: ptr(int64(3)), want: 3},
		{name: "значение по умолчанию", flag: 2, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolveNumber(tt.env, tt.flagChanged, tt.flag, tt.file))
		})
	}
}

func TestResolveDuration(t *testing.T) {
	fileValue := Duration(10 * time.Second)

	tests := []struct {
		env         *time.Duration
		file        *Duration
		name        string
		flag        time.Duration
		want        time.Duration
		flagChanged bool
	}{
		{name: "окружение важнее всего", env: ptr(time.Second), flagChanged: true, flag: 2 * time.Second, file: &fileValue, want: time.Second},
		{name: "флаг важнее файла", flagChanged: true, flag: 2 * time.Second, file: &fileValue, want: 2 * time.Second},
		{name: "файл важнее значения по умолчанию", flag: 3 * time.Second, file: &fileValue, want: 10 * time.Second},
		{name: "значение по умолчанию", flag: 3 * time.Second, want: 3 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolveDuration(tt.env, tt.flagChanged, tt.flag, tt.file))
		})
	}
}

func TestLoad_Server(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"address": "0.0.0.0:8080",
		"max_upload_size": 1024,
		"shutdown_timeout": "15s",
		"log_level": "debug",
		"database_dsn": "postgres://db",
		"rabbitmq_url": "amqp://mq",
		"s3_endpoint": "minio:9000",
		"s3_access_key": "access",
		"s3_secret_key": "secret",
		"s3_bucket": "avatars",
		"s3_use_ssl": false
	}`), 0o600))

	var got Server
	require.NoError(t, Load(path, &got))

	require.NotNil(t, got.Address)
	assert.Equal(t, "0.0.0.0:8080", *got.Address)
	assert.Equal(t, int64(1024), *got.MaxUploadSize)
	assert.Equal(t, 15*time.Second, got.ShutdownTimeout.Std())
	assert.Equal(t, "debug", *got.LogLevel)
	assert.Equal(t, "postgres://db", *got.DatabaseDSN)
	assert.Equal(t, "amqp://mq", *got.RabbitMQURL)
	assert.Equal(t, "minio:9000", *got.S3Endpoint)
	assert.Equal(t, "access", *got.S3AccessKey)
	assert.Equal(t, "secret", *got.S3SecretKey)
	assert.Equal(t, "avatars", *got.S3Bucket)
	require.NotNil(t, got.S3UseSSL, "явно заданный false отличается от отсутствующего ключа")
	assert.False(t, *got.S3UseSSL)

	var empty Server
	require.NoError(t, json.Unmarshal([]byte(`{}`), &empty))
	assert.Nil(t, empty.S3UseSSL)
	assert.Nil(t, empty.ShutdownTimeout)
}

func TestLoad_Worker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"concurrency": 4,
		"max_retries": 5,
		"s3_bucket": "avatars"
	}`), 0o600))

	var got Worker
	require.NoError(t, Load(path, &got))

	assert.Equal(t, 4, *got.Concurrency)
	assert.Equal(t, 5, *got.MaxRetries)
	assert.Equal(t, "avatars", *got.S3Bucket)
	assert.Nil(t, got.DatabaseDSN)
}

func TestDuration_MarshalRoundTripInStruct(t *testing.T) {
	var got struct {
		Interval Duration `json:"interval"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"interval":"1m"}`), &got))
	assert.Equal(t, time.Minute, got.Interval.Std())
}
