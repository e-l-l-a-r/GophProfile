package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Метрики создаются через глобальный провайдер: он начинает отдавать данные,
// как только в приложении зарегистрирован настоящий MeterProvider.
// В метках только значения с малой кардинальностью: user_id не используется.
var (
	businessMeter = otel.Meter("gophprofile/business")

	uploadsTotal, _ = businessMeter.Int64Counter("avatars.uploads",
		metric.WithDescription("Total number of avatar uploads"))
	uploadDuration, _ = businessMeter.Float64Histogram("avatars.upload.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Avatar upload duration"))
	deletesTotal, _ = businessMeter.Int64Counter("avatars.deletes",
		metric.WithDescription("Total number of avatar deletions"))
	processingTotal, _ = businessMeter.Int64Counter("avatars.processing",
		metric.WithDescription("Total number of avatar processing runs by the worker"))
)

// Значения метки status.
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// Status возвращает значение метки status по ошибке операции.
func Status(err error) string {
	if err != nil {
		return StatusError
	}
	return StatusSuccess
}

// RecordUpload учитывает загрузку аватарки.
func RecordUpload(ctx context.Context, start time.Time, err error) {
	attrs := metric.WithAttributes(attribute.String("status", Status(err)))
	uploadsTotal.Add(ctx, 1, attrs)
	uploadDuration.Record(ctx, time.Since(start).Seconds(), attrs)
}

// RecordDelete учитывает удаление аватарки.
func RecordDelete(ctx context.Context, err error) {
	deletesTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", Status(err))))
}

// RecordProcessing учитывает обработку события воркером; operation - upload или delete.
func RecordProcessing(ctx context.Context, operation string, err error) {
	processingTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("status", Status(err)),
	))
}
