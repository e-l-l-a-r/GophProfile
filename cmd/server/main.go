// Package main - HTTP-сервер GophProfile: REST API аватарок, проверка
// работоспособности и веб-интерфейс.
//
// Настройки задаются переменными окружения, флагами или файлом конфигурации в
// формате JSON (флаг -c/--config или переменная CONFIG). Приоритет:
// окружение, флаг, файл, значение по умолчанию.
//
//	ADDRESS,          -a, --address           адрес HTTP-сервера (localhost:8080)
//	LOG_LEVEL,        -l, --log-level         уровень логирования (info)
//	MAX_UPLOAD_SIZE,      --max-upload-size   предельный размер файла, байт (10485760)
//	SHUTDOWN_TIMEOUT,     --shutdown-timeout  время на корректное завершение (15s)
//	S3_ENDPOINT,          --s3-endpoint       адрес S3-хранилища без схемы
//	S3_BUCKET,            --s3-bucket         бакет S3-хранилища (avatars)
//	S3_USE_SSL,           --s3-use-ssl        HTTPS к S3-хранилищу (false)
//
// Секреты задаются только окружением или файлом: DATABASE_DSN, RABBITMQ_URL,
// S3_ACCESS_KEY, S3_SECRET_KEY.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"go.uber.org/fx"

	"github.com/e-l-l-a-r/gophprofile/internal/logger"
)

// appStopTimeout - общее время на остановку всех компонентов приложения.
const appStopTimeout = 30 * time.Second

// Сведения о сборке; задаются при сборке через -ldflags "-X main.buildVersion=...".
var (
	buildVersion string
	buildDate    string
	buildCommit  string
)

// newApp собирает граф зависимостей приложения. Провайдер внешних
// компонентов infraProvider передаётся снаружи: сервер использует
// provideInfra, а тесты - моки.
func newApp(args []string, errs *serverErrors, infraProvider any) *fx.App {
	return fx.New(
		fx.NopLogger,
		fx.StopTimeout(appStopTimeout),
		fx.Provide(
			func() (Config, error) { return getConfig(args, os.Stderr) },
			func() serverErrors { return make(serverErrors, 1) },
			provideLogger,
			infraProvider,
			provideAvatarService,
			provideRouter,
			provideListener,
			provideHTTPServer,
		),
		fx.Populate(errs),
		fx.Invoke(func(*http.Server) {}),
	)
}

func main() {
	if err := execute(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// execute выводит сведения о сборке и запускает сервер, который работает до
// сигнала завершения.
func execute(args []string) error {
	if err := logger.PrintBuildInfo(os.Stdout, buildVersion, buildDate, buildCommit); err != nil {
		return err
	}

	// Подписываемся на сигналы до запуска компонентов, чтобы не потерять сигнал,
	// пришедший во время старта.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()

	return run(ctx, args, provideInfra)
}

// run запускает приложение и работает, пока не отменён ctx или сервер не
// сообщит об ошибке. Затем останавливает компоненты в порядке, обратном
// запуску: сначала HTTP-сервер, затем закрываются подключения.
func run(ctx context.Context, args []string, infraProvider any) error {
	var errs serverErrors

	app := newApp(args, &errs, infraProvider)
	if err := app.Err(); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}

		return err
	}

	if err := app.Start(ctx); err != nil {
		return err
	}

	var runErr error
	select {
	case runErr = <-errs:
	case <-ctx.Done():
	}

	// Останавливаем компоненты, даже если сервер завершился с ошибкой.
	stopCtx, cancel := context.WithTimeout(context.Background(), appStopTimeout)
	defer cancel()

	if err := app.Stop(stopCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}

	return runErr
}
