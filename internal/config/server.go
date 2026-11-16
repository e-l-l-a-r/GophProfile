package config

// Server - настройки HTTP-сервера, прочитанные из файла конфигурации. Нулевой
// указатель означает, что параметр в файле не задан.
//
// Файл содержит секреты (database_dsn, s3_secret_key, rabbitmq_url): ограничьте
// права доступа к нему (например, chmod 600) и не добавляйте его в систему
// контроля версий.
type Server struct {
	Infra
	// Address - адрес и порт HTTP-сервера.
	Address *string `json:"address"`
	// MaxUploadSize - максимальный размер загружаемого файла в байтах.
	MaxUploadSize *int64 `json:"max_upload_size"`
	// ShutdownTimeout - время на корректное завершение: строка
	// time.ParseDuration или число секунд.
	ShutdownTimeout *Duration `json:"shutdown_timeout"`
}

// Worker - настройки воркера асинхронной обработки, прочитанные из файла
// конфигурации. Нулевой указатель означает, что параметр в файле не задан.
type Worker struct {
	Infra
	// Concurrency - число одновременно обрабатываемых сообщений.
	Concurrency *int `json:"concurrency"`
	// MaxRetries - число повторов обработки сообщения до отправки в очередь
	// недоставленных сообщений.
	MaxRetries *int `json:"max_retries"`
}

// Infra - общие для сервера и воркера настройки логирования и подключений к
// инфраструктуре.
type Infra struct {
	// LogLevel - уровень логирования: debug, info, warn или error.
	LogLevel *string `json:"log_level"`
	// DatabaseDSN - строка подключения к PostgreSQL.
	DatabaseDSN *string `json:"database_dsn"`
	// RabbitMQURL - адрес подключения к RabbitMQ (amqp://user:pass@host:port/).
	RabbitMQURL *string `json:"rabbitmq_url"`
	// S3Endpoint - адрес S3-совместимого хранилища без схемы (host:port).
	S3Endpoint *string `json:"s3_endpoint"`
	// S3AccessKey - идентификатор ключа доступа к хранилищу.
	S3AccessKey *string `json:"s3_access_key"`
	// S3SecretKey - секретный ключ доступа к хранилищу.
	S3SecretKey *string `json:"s3_secret_key"`
	// S3Bucket - имя бакета для файлов аватарок.
	S3Bucket *string `json:"s3_bucket"`
	// S3UseSSL включает HTTPS при обращении к хранилищу.
	S3UseSSL *bool `json:"s3_use_ssl"`
}
