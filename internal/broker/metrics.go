package broker

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

// RegisterQueueMetrics регистрирует gauge глубины очереди и числа потребителей
// для очередей событий и их очередей недоставленных сообщений. Возвращаемая
// функция отменяет регистрацию.
func (c *Client) RegisterQueueMetrics() (func() error, error) {
	meter := otel.Meter("gophprofile/broker")

	depth, err := meter.Int64ObservableGauge("rabbitmq.queue.depth",
		metric.WithDescription("Messages ready in the queue"))
	if err != nil {
		return nil, err
	}
	consumers, err := meter.Int64ObservableGauge("rabbitmq.queue.consumers",
		metric.WithDescription("Consumers of the queue"))
	if err != nil {
		return nil, err
	}

	queues := make([]string, 0, 2*len(bindings))
	for _, b := range bindings {
		queues = append(queues, b.queue, DeadLetterQueue(b.queue))
	}

	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		ch, err := c.channel()
		if err != nil {
			c.log.Warn("не удалось получить глубину очередей", zap.Error(err))
			return nil
		}
		defer func() { _ = ch.Close() }()

		for _, name := range queues {
			q, err := ch.QueueDeclarePassive(name, true, false, false, false, nil)
			if err != nil {
				// Ошибка закрывает канал: дальше очереди не опросить до следующей выгрузки.
				c.log.Warn("не удалось получить состояние очереди", zap.String("queue", name), zap.Error(err))
				return nil
			}
			attrs := metric.WithAttributes(attribute.String("queue", name))
			o.ObserveInt64(depth, int64(q.Messages), attrs)
			o.ObserveInt64(consumers, int64(q.Consumers), attrs)
		}
		return nil
	}, depth, consumers)
	if err != nil {
		return nil, err
	}

	return reg.Unregister, nil
}
