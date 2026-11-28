package repository

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// storageCacheTTL - как долго значение avatars.storage.bytes переиспользуется,
// чтобы не выполнять SUM по таблице при каждой выгрузке метрик.
const storageCacheTTL = 30 * time.Second

const queryStorageBytes = `SELECT COALESCE(SUM(size_bytes), 0) FROM avatars WHERE deleted_at IS NULL`

// StorageBytes возвращает суммарный размер оригиналов неудалённых аватарок.
func (p *Postgres) StorageBytes(ctx context.Context) (int64, error) {
	var total int64
	if err := p.db.QueryRowContext(ctx, queryStorageBytes).Scan(&total); err != nil {
		return 0, fmt.Errorf("не удалось посчитать размер хранилища: %w", err)
	}

	return total, nil
}

// RegisterDBMetrics регистрирует метрики пула соединений. Возвращаемая функция
// отменяет регистрацию.
func (p *Postgres) RegisterDBMetrics() (func() error, error) {
	meter := otel.Meter("gophprofile/db")

	open, err := meter.Int64ObservableGauge("db.client.connections.open",
		metric.WithDescription("Open database connections"))
	if err != nil {
		return nil, err
	}
	inUse, err := meter.Int64ObservableGauge("db.client.connections.in_use",
		metric.WithDescription("Database connections in use"))
	if err != nil {
		return nil, err
	}
	idle, err := meter.Int64ObservableGauge("db.client.connections.idle",
		metric.WithDescription("Idle database connections"))
	if err != nil {
		return nil, err
	}
	waitCount, err := meter.Int64ObservableCounter("db.client.connections.wait_count",
		metric.WithDescription("Total number of waits for a connection"))
	if err != nil {
		return nil, err
	}
	waitSeconds, err := meter.Float64ObservableCounter("db.client.connections.wait_duration",
		metric.WithUnit("s"), metric.WithDescription("Total time blocked waiting for a connection"))
	if err != nil {
		return nil, err
	}

	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		stats := p.db.Stats()
		o.ObserveInt64(open, int64(stats.OpenConnections))
		o.ObserveInt64(inUse, int64(stats.InUse))
		o.ObserveInt64(idle, int64(stats.Idle))
		o.ObserveInt64(waitCount, stats.WaitCount)
		o.ObserveFloat64(waitSeconds, stats.WaitDuration.Seconds())
		return nil
	}, open, inUse, idle, waitCount, waitSeconds)
	if err != nil {
		return nil, err
	}

	return reg.Unregister, nil
}

// RegisterStorageMetric регистрирует gauge avatars.storage.bytes с общим
// размером хранилища без разбивки по пользователям (низкая кардинальность).
func (p *Postgres) RegisterStorageMetric() (func() error, error) {
	meter := otel.Meter("gophprofile/business")

	gauge, err := meter.Int64ObservableGauge("avatars.storage.bytes",
		metric.WithUnit("By"), metric.WithDescription("Total storage used by avatars"))
	if err != nil {
		return nil, err
	}

	var (
		cached  int64
		fetched time.Time
	)
	reg, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		if fetched.IsZero() || time.Since(fetched) > storageCacheTTL {
			total, err := p.StorageBytes(ctx)
			if err != nil {
				return err
			}
			cached, fetched = total, time.Now()
		}
		o.ObserveInt64(gauge, cached)
		return nil
	}, gauge)
	if err != nil {
		return nil, err
	}

	return reg.Unregister, nil
}
