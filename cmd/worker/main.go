// Package main - воркер GophProfile: создаёт миниатюры загруженных аватарок
// и удаляет файлы удалённых, получая события из RabbitMQ.
//
// Настройки задаются переменными окружения, флагами или файлом конфигурации в
// формате JSON (флаг -c/--config или переменная CONFIG). Приоритет:
// окружение, флаг, файл, значение по умолчанию.
//
//	LOG_LEVEL,    -l, --log-level    уровень логирования (info)
//	CONCURRENCY,      --concurrency  одновременно обрабатываемых сообщений очереди (4)
//	MAX_RETRIES,      --max-retries  попыток обработки сообщения (5)
//	S3_ENDPOINT,      --s3-endpoint  адрес S3-хранилища без схемы
//	S3_BUCKET,        --s3-bucket    бакет S3-хранилища (avatars)
//	S3_USE_SSL,       --s3-use-ssl   HTTPS к S3-хранилищу (false)
//
// Секреты задаются только окружением или файлом: DATABASE_DSN, RABBITMQ_URL,
// S3_ACCESS_KEY, S3_SECRET_KEY.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/broker"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/repository"
	"github.com/e-l-l-a-r/gophprofile/internal/retry"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
	"github.com/e-l-l-a-r/gophprofile/internal/worker"
)

// startTimeout - время ожидания внешних компонентов при старте.
const startTimeout = 30 * time.Second

// Сведения о сборке; задаются при сборке через -ldflags "-X main.buildVersion=...".
var (
	buildVersion string
	buildDate    string
	buildCommit  string
)

func main() {
	if err := execute(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// execute выводит сведения о сборке и запускает воркер, который работает до
// сигнала завершения.
func execute(args []string) error {
	if err := logger.PrintBuildInfo(os.Stdout, buildVersion, buildDate, buildCommit); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()

	return run(ctx, args)
}

// run подключается к базе, хранилищу и брокеру и обрабатывает события, пока
// не отменён ctx. После отмены дожидается завершения начатых обработок и
// закрывает подключения.
func run(ctx context.Context, args []string) (err error) {
	conf, err := getConfig(args, os.Stderr)
	if err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}
		return err
	}

	log, err := logger.New(conf.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()

	pg, err := repository.NewPostgres(conf.DatabaseDSN)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, pg.Close()) }()

	s3, err := storage.NewS3(storage.Config{
		Endpoint:  conf.S3Endpoint,
		AccessKey: conf.S3AccessKey,
		SecretKey: conf.S3SecretKey,
		Bucket:    conf.S3Bucket,
		UseSSL:    conf.S3UseSSL,
	})
	if err != nil {
		return err
	}

	mq := broker.New(conf.RabbitMQURL, log)
	defer func() { err = errors.Join(err, mq.Close()) }()

	if err = start(ctx, pg, s3, mq); err != nil {
		return err
	}

	policy := retry.Default()
	policy.MaxAttempts = conf.MaxRetries
	w := worker.New(pg, s3, policy, conf.Concurrency, log)

	log.Info("воркер запущен", zap.Int("обработчиков", conf.Concurrency), zap.Int("попыток", conf.MaxRetries))
	err = w.Run(ctx, mq)
	log.Info("воркер остановлен")

	return err
}

// start проверяет базу и применяет миграции, создаёт бакет и подключается к
// брокеру. Миграции применяет и сервер; повторное применение безопасно, а
// воркер не зависит от порядка запуска.
func start(ctx context.Context, pg *repository.Postgres, s3 *storage.S3, mq *broker.Client) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()

	if err := pg.Ping(ctx); err != nil {
		return err
	}
	if err := pg.Migrate(); err != nil {
		return err
	}
	if err := s3.EnsureBucket(ctx); err != nil {
		return err
	}

	return mq.Connect()
}
