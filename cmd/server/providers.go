package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"github.com/e-l-l-a-r/gophprofile/internal/broker"
	"github.com/e-l-l-a-r/gophprofile/internal/handler"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/repository"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
	"github.com/e-l-l-a-r/gophprofile/internal/telemetry"
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
func provideLogger(lc fx.Lifecycle, conf Config) (*logger.Logger, error) {
	log, err := logger.New(conf.LogLevel)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return log.Shutdown(shutdownCtx)
	}})
	return log, nil
}

// Хук регистрируется до HTTP-сервера, чтобы сбросить спаны после завершения запросов.
func registerTracing(lc fx.Lifecycle) {
	var provider *sdktrace.TracerProvider
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var err error
			provider, err = telemetry.NewTracerProvider(ctx, "gophprofile-server")
			return err
		},
		OnStop: func(ctx context.Context) error {
			return telemetry.ShutdownTracerProvider(ctx, provider)
		},
	})
}

func provideMeterProvider(lc fx.Lifecycle) (*metric.MeterProvider, error) {
	provider, err := telemetry.NewMeterProvider(context.Background(), "gophprofile-server")
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = provider.Shutdown(shutdownCtx)
		return nil
	}})
	return provider, nil
}

// provideInfra подключается к PostgreSQL, S3 и RabbitMQ и управляет их жизненным циклом.
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
			if err := registerInfraMetrics(pg, mq); err != nil {
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

// registerInfraMetrics подключает метрики пула БД, размера хранилища и очередей.
// Регистрации живут до завершения процесса.
func registerInfraMetrics(pg *repository.Postgres, mq *broker.Client) error {
	for _, register := range []func() (func() error, error){
		pg.RegisterDBMetrics, pg.RegisterStorageMetric, mq.RegisterQueueMetrics,
	} {
		if _, err := register(); err != nil {
			return fmt.Errorf("не удалось зарегистрировать метрики: %w", err)
		}
	}

	return nil
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
func provideRouter(provider *metric.MeterProvider, avatars *service.AvatarService, checkers map[string]handler.Checker, conf Config, log *logger.Logger) http.Handler {
	router := handler.New(avatars, handler.Web{Static: web.Static, Templates: web.Templates}, handler.Config{
		Checkers:      checkers,
		MaxUploadSize: conf.MaxUploadSize,
		HealthTimeout: healthTimeout,
	}, log).Routes()
	return otelhttp.NewHandler(router, "gophprofile-server",
		otelhttp.WithMeterProvider(provider),
		otelhttp.WithPropagators(telemetry.Propagator()),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
	)
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
			log.Info("HTTP-сервер запущен", zap.String("address", ln.Addr().String()))

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
