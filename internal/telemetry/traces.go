package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// Propagator передаёт W3C Trace Context и Baggage между процессами.
func Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}

// NewTracerProvider регистрирует провайдер с пакетным экспортом спанов по OTLP/gRPC.
func NewTracerProvider(ctx context.Context, serviceName string) (*sdktrace.TracerProvider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("не удалось создать ресурс трассировки: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if os.Getenv("OTEL_TRACES_EXPORTER") != "none" {
		exporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("не удалось создать экспортёр трассировки: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	provider := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(Propagator())
	return provider, nil
}

// ShutdownTracerProvider ограничивает время отправки накопленных спанов.
func ShutdownTracerProvider(ctx context.Context, provider *sdktrace.TracerProvider) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return provider.Shutdown(ctx)
}

// EndSpan отмечает ошибку без экспорта её текста, который может содержать данные запроса.
func EndSpan(span trace.Span, err *error) {
	if *err != nil {
		span.SetStatus(codes.Error, "")
	}
	span.End()
}
