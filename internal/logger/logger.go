// Package logger предоставляет логгер приложения поверх OpenTelemetry bridge
// для zap и вспомогательные функции для вывода сведений о сборке.
package logger

import (
	"context"
	"fmt"
	"io"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// notAvailable - значение, которое выводится вместо незаданных сведений о сборке.
const notAvailable = "N/A"

// Logger — совместимый с текущими callers zap-логгер, отправляющий записи в OTLP.
type Logger struct {
	*zap.Logger
	provider *sdklog.LoggerProvider
}

// New создаёт логгер с заданным уровнем логирования и отправляет записи через
// OTLP gRPC exporter в OpenTelemetry Collector.
func New(level string) (*Logger, error) {
	lvl, err := zap.ParseAtomicLevel(level)
	if err != nil {
		return nil, fmt.Errorf("некорректный уровень логирования %q: %w", level, err)
	}

	ctx := context.Background()
	exporter, err := otlploggrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("не удалось создать OTLP exporter логов: %w", err)
	}
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "gophprofile"
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, fmt.Errorf("не удалось создать ресурс логирования: %w", err)
	}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)
	otelCore := otelzap.NewCore("github.com/e-l-l-a-r/gophprofile", otelzap.WithLoggerProvider(provider), otelzap.WithVersion(serviceName))
	core := zapcore.NewTee(newStdoutCore(os.Stdout, serviceName), otelCore)
	log := zap.New(core).WithOptions(zap.IncreaseLevel(lvl))
	return &Logger{Logger: log, provider: provider}, nil
}

// Ctx возвращает логгер, записи которого связаны с трейсом из ctx: в OTLP
// запись получает trace и span из контекста (поле "context" понимает
// otelzap), а в stdout - поля trace_id и span_id.
func (l *Logger) Ctx(ctx context.Context) *zap.Logger {
	return l.Logger.With(zap.Any(contextKey, ctx))
}

// Nop возвращает логгер, который отбрасывает все записи. Удобен в тестах и
// там, где логгер не настроен.
func Nop() *Logger {
	return &Logger{Logger: zap.NewNop()}
}

// Shutdown flushes buffered OpenTelemetry log records.
func (l *Logger) Shutdown(ctx context.Context) error {
	if l.provider == nil {
		return nil
	}
	return l.provider.Shutdown(ctx)
}

// Sync flushes buffered OpenTelemetry log records.
func (l *Logger) Sync() error {
	return l.Shutdown(context.Background())
}

// PrintBuildInfo выводит в w версию, дату и коммит сборки. Незаданные
// (пустые) значения заменяются на "N/A".
func PrintBuildInfo(w io.Writer, version, date, commit string) error {
	_, err := fmt.Fprintf(w, "Build version: %s\nBuild date: %s\nBuild commit: %s\n",
		orNotAvailable(version), orNotAvailable(date), orNotAvailable(commit))
	if err != nil {
		return fmt.Errorf("не удалось вывести сведения о сборке: %w", err)
	}

	return nil
}

// orNotAvailable возвращает value или "N/A", если value пусто.
func orNotAvailable(value string) string {
	if value == "" {
		return notAvailable
	}

	return value
}
