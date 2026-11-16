package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/caarlos0/env/v6"
	"github.com/spf13/pflag"

	"github.com/e-l-l-a-r/gophprofile/internal/config"
)

const (
	// defaultLogLevel - уровень логирования по умолчанию.
	defaultLogLevel = "info"
	// defaultConcurrency - число одновременно обрабатываемых сообщений очереди.
	defaultConcurrency = 4
	// defaultMaxRetries - число попыток обработки сообщения по умолчанию.
	defaultMaxRetries = 5
	// defaultS3Bucket - бакет хранилища файлов по умолчанию.
	defaultS3Bucket = "avatars"
)

// envConfig - настройки, прочитанные из переменных окружения. Указатели
// позволяют отличить незаданную переменную (nil) от заданной нулевым значением.
type envConfig struct {
	Concurrency *int   `env:"CONCURRENCY"`
	MaxRetries  *int   `env:"MAX_RETRIES"`
	S3UseSSL    *bool  `env:"S3_USE_SSL"`
	LogLevel    string `env:"LOG_LEVEL"`
	DatabaseDSN string `env:"DATABASE_DSN"`
	RabbitMQURL string `env:"RABBITMQ_URL"`
	S3Endpoint  string `env:"S3_ENDPOINT"`
	S3AccessKey string `env:"S3_ACCESS_KEY"`
	S3SecretKey string `env:"S3_SECRET_KEY"`
	S3Bucket    string `env:"S3_BUCKET"`
	ConfigPath  string `env:"CONFIG"`
}

// Config - итоговые настройки воркера после разрешения приоритета окружения,
// флагов и файла конфигурации.
//
// Config содержит секреты (DatabaseDSN, RabbitMQURL, S3SecretKey): не выводите
// его в лог целиком.
type Config struct {
	LogLevel    string
	DatabaseDSN string
	RabbitMQURL string
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	ConfigPath  string
	Concurrency int
	MaxRetries  int
	S3UseSSL    bool
}

// getConfig собирает настройки и проверяет их корректность. Сообщения об
// ошибках разбора аргументов и справка выводятся в output.
func getConfig(args []string, output io.Writer) (Config, error) {
	conf, err := parseConfig(args, output)
	if err != nil {
		return Config{}, err
	}

	if err := conf.validate(); err != nil {
		return Config{}, err
	}

	return conf, nil
}

// parseConfig собирает настройки из окружения, аргументов args и файла
// конфигурации. Приоритет: окружение, флаг, файл, значение по умолчанию.
// Секреты задаются только через окружение или файл конфигурации.
func parseConfig(args []string, output io.Writer) (Config, error) {
	var envConf envConfig
	if err := env.Parse(&envConf); err != nil {
		return Config{}, fmt.Errorf("не удалось разобрать переменные окружения: %w", err)
	}

	fs := pflag.NewFlagSet("worker", pflag.ContinueOnError)
	fs.SetOutput(output)

	flagLogLevel := fs.StringP("log-level", "l", defaultLogLevel, "уровень логирования: debug, info, warn, error")
	flagConcurrency := fs.Int("concurrency", defaultConcurrency, "число одновременно обрабатываемых сообщений очереди")
	flagMaxRetries := fs.Int("max-retries", defaultMaxRetries, "число попыток обработки сообщения")
	flagS3Endpoint := fs.String("s3-endpoint", "", "адрес S3-хранилища без схемы (host:port)")
	flagS3Bucket := fs.String("s3-bucket", defaultS3Bucket, "бакет S3-хранилища")
	flagS3UseSSL := fs.Bool("s3-use-ssl", false, "обращаться к S3-хранилищу по HTTPS")
	flagConfig := fs.StringP("config", "c", "", "путь к файлу конфигурации в формате JSON")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Воркер GophProfile.\nИспользование: %s [флаги]\n\n%s\n"+
			"Секреты задаются только переменными окружения или файлом конфигурации:\n"+
			"  DATABASE_DSN, RABBITMQ_URL, S3_ACCESS_KEY, S3_SECRET_KEY\n", fs.Name(), fs.FlagUsages())
	}

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("не удалось разобрать аргументы: %w", err)
	}

	configPath := envConf.ConfigPath
	if configPath == "" {
		configPath = *flagConfig
	}

	var fileConf config.Worker
	if err := config.Load(configPath, &fileConf); err != nil {
		return Config{}, err
	}

	return Config{
		ConfigPath: configPath,
		LogLevel: config.ResolveString(envConf.LogLevel,
			fs.Changed("log-level"), *flagLogLevel, fileConf.LogLevel),
		DatabaseDSN: config.ResolveString(envConf.DatabaseDSN, false, "", fileConf.DatabaseDSN),
		RabbitMQURL: config.ResolveString(envConf.RabbitMQURL, false, "", fileConf.RabbitMQURL),
		S3Endpoint: config.ResolveString(envConf.S3Endpoint,
			fs.Changed("s3-endpoint"), *flagS3Endpoint, fileConf.S3Endpoint),
		S3AccessKey: config.ResolveString(envConf.S3AccessKey, false, "", fileConf.S3AccessKey),
		S3SecretKey: config.ResolveString(envConf.S3SecretKey, false, "", fileConf.S3SecretKey),
		S3Bucket: config.ResolveString(envConf.S3Bucket,
			fs.Changed("s3-bucket"), *flagS3Bucket, fileConf.S3Bucket),
		S3UseSSL: config.ResolveBool(envConf.S3UseSSL,
			fs.Changed("s3-use-ssl"), *flagS3UseSSL, fileConf.S3UseSSL),
		Concurrency: config.ResolveNumber(envConf.Concurrency,
			fs.Changed("concurrency"), *flagConcurrency, fileConf.Concurrency),
		MaxRetries: config.ResolveNumber(envConf.MaxRetries,
			fs.Changed("max-retries"), *flagMaxRetries, fileConf.MaxRetries),
	}, nil
}

// validate проверяет, что заданы все обязательные настройки и что они допустимы.
func (c Config) validate() error {
	var errs []error

	if c.DatabaseDSN == "" {
		errs = append(errs, errors.New("не задана строка подключения к базе (DATABASE_DSN)"))
	}
	if c.RabbitMQURL == "" {
		errs = append(errs, errors.New("не задан адрес брокера (RABBITMQ_URL)"))
	}
	if c.S3Endpoint == "" {
		errs = append(errs, errors.New("не задан адрес S3-хранилища (S3_ENDPOINT)"))
	}
	if c.S3AccessKey == "" || c.S3SecretKey == "" {
		errs = append(errs, errors.New("не заданы ключи S3-хранилища (S3_ACCESS_KEY, S3_SECRET_KEY)"))
	}
	if c.S3Bucket == "" {
		errs = append(errs, errors.New("не задан бакет S3-хранилища"))
	}
	if c.Concurrency <= 0 {
		errs = append(errs, errors.New("число обработчиков должно быть положительным"))
	}
	if c.MaxRetries <= 0 {
		errs = append(errs, errors.New("число попыток должно быть положительным"))
	}

	return errors.Join(errs...)
}
