package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"
	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/broker"
	"github.com/e-l-l-a-r/gophprofile/internal/handler"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/repository"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
	"github.com/e-l-l-a-r/gophprofile/web"
)

const (
	// startTimeout - время ожидания внешнего компонента при старте.
	startTimeout = 30 * time.Second
	// healthTimeout - время на проверку одного компонента в /health.
	healthTimeout = 2 * time.Second
	// readHeaderTimeout - время на чтение заголовков запроса.
	readHeaderTimeout = 10 * time.Second
	// idleTimeout - время жизни простаивающего keep-alive соединения.
	idleTimeout = 2 * time.Minute
)

// serverErrors - канал, по которому HTTP-сервер сообщает об ошибке Serve,
// возникшей после успешного старта.
type serverErrors chan error

// infra - внешние компоненты, необходимые сервису. Один провайдер даёт их
// все, чтобы в тестах их можно было заменить моками.
type infra struct {
	fx.Out

	Repository service.Repository
	Files      service.FileStorage
	Publisher  service.Publisher
	Checkers   map[string]handler.Checker
}

// provideLogger создаёт логгер по итоговой конфигурации.
func provideLogger(conf Config) (*logger.Logger, error) {
	return logger.New(conf.LogLevel)
}

// provideInfra подключается к PostgreSQL, S3 и RabbitMQ. При старте
// приложение проверяет базу и применяет миграции, создаёт бакет и
// подключается к брокеру; при остановке закрывает подключения. Хук
// регистрируется раньше хука сервера, поэтому подключения закрываются уже
// после остановки сервера.
func provideInfra(lc fx.Lifecycle, conf Config, log *logger.Logger) (infra, error) {
	pg, err := repository.NewPostgres(conf.DatabaseDSN)
	if err != nil {
		return infra{}, err
	}

	s3, err := storage.NewS3(storage.Config{
		Endpoint:  conf.S3Endpoint,
		AccessKey: conf.S3AccessKey,
		SecretKey: conf.S3SecretKey,
		Bucket:    conf.S3Bucket,
		UseSSL:    conf.S3UseSSL,
	})
	if err != nil {
		return infra{}, errors.Join(err, pg.Close())
	}

	mq := broker.New(conf.RabbitMQURL, log)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, startTimeout)
			defer cancel()

			if err := startInfra(ctx, pg, s3, mq, log); err != nil {
				return errors.Join(err, mq.Close(), pg.Close())
			}

			return nil
		},
		OnStop: func(context.Context) error {
			return errors.Join(mq.Close(), pg.Close())
		},
	})

	return infra{
		Repository: pg,
		Files:      s3,
		Publisher:  mq,
		Checkers:   map[string]handler.Checker{"database": pg, "storage": s3, "broker": mq},
	}, nil
}

// startInfra проверяет базу и применяет миграции, создаёт бакет и
// подключается к брокеру.
func startInfra(ctx context.Context, pg *repository.Postgres, s3 *storage.S3, mq *broker.Client, log *logger.Logger) error {
	if err := pg.Ping(ctx); err != nil {
		return err
	}
	log.Info("применяю миграции базы данных")
	if err := pg.Migrate(); err != nil {
		return err
	}
	if err := s3.EnsureBucket(ctx); err != nil {
		return err
	}

	return mq.Connect()
}

// provideAvatarService создаёт сервис аватарок.
func provideAvatarService(
	repo service.Repository, files service.FileStorage, publisher service.Publisher, log *logger.Logger,
) *service.AvatarService {
	return service.NewAvatarService(repo, files, publisher, log)
}

// provideRouter создаёт маршрутизатор HTTP-интерфейса.
func provideRouter(avatars *service.AvatarService, checkers map[string]handler.Checker, conf Config, log *logger.Logger) http.Handler {
	return handler.New(avatars, handler.Web{Static: web.Static, Templates: web.Templates}, handler.Config{
		Checkers:      checkers,
		MaxUploadSize: conf.MaxUploadSize,
		HealthTimeout: healthTimeout,
	}, log).Routes()
}

// provideListener занимает сетевой адрес сервера. Адрес занимается при сборке
// приложения, поэтому ошибка привязки прерывает запуск до старта остальных
// компонентов.
func provideListener(lc fx.Lifecycle, conf Config) (net.Listener, error) {
	var lcfg net.ListenConfig

	ln, err := lcfg.Listen(context.Background(), "tcp", conf.Address)
	if err != nil {
		return nil, fmt.Errorf("не удалось занять адрес %q: %w", conf.Address, err)
	}

	lc.Append(fx.Hook{OnStop: func(context.Context) error {
		// Остановка сервера уже закрыла слушатель; повторное закрытие не ошибка.
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}

		return nil
	}})

	return ln, nil
}

// provideHTTPServer создаёт HTTP-сервер. Сервер запускается в OnStart; ошибка
// Serve после старта передаётся в errs. При остановке сервер перестаёт
// принимать соединения и ждёт завершения начатых запросов не дольше
// ShutdownTimeout.
func provideHTTPServer(
	lc fx.Lifecycle, conf Config, log *logger.Logger, ln net.Listener, router http.Handler, errs serverErrors,
) *http.Server {
	server := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          zap.NewStdLog(log.Logger),
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			log.Info("HTTP-сервер запущен", zap.String("адрес", ln.Addr().String()))

			go func() {
				if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errs <- err
				}
			}()

			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("останавливаю HTTP-сервер")

			ctx, cancel := context.WithTimeout(ctx, conf.ShutdownTimeout)
			defer cancel()

			if err := server.Shutdown(ctx); err != nil {
				return errors.Join(fmt.Errorf("не удалось корректно остановить HTTP-сервер: %w", err), server.Close())
			}

			return nil
		},
	})

	return server
}
