// Package telemetry configures OpenTelemetry metrics for the service.
package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// NewMeterProvider creates a periodic OTLP metrics provider and registers it globally.
func NewMeterProvider(ctx context.Context, serviceName string) (*metric.MeterProvider, error) {
	exporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithOS(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			attribute.String("environment", "local"),
		),
	)
	if err != nil {
		return nil, err
	}

	provider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(exporter, metric.WithInterval(2*time.Second))),
	)
	otel.SetMeterProvider(provider)
	return provider, nil
}
