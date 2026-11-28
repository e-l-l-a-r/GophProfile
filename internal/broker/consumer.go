package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Message - полученное из очереди сообщение.
type Message struct {
	// ID - уникальный идентификатор сообщения, заданный при публикации.
	ID string
	// RoutingKey - ключ маршрутизации, с которым сообщение опубликовано.
	RoutingKey string
	// Body - тело сообщения (JSON события).
	Body []byte
	// Redelivered - признак повторной доставки.
	Redelivered bool
}

// Handler обрабатывает сообщение. Ошибка означает, что сообщение обработать
// не удалось и повторять его бессмысленно: оно уходит в очередь
// недоставленных сообщений. Повторы при временных сбоях - забота обработчика.
type Handler func(ctx context.Context, msg Message) error

// Consume получает сообщения из очереди queue и передаёт их handler в
// concurrency параллельных обработчиков, пока не будет отменён ctx. При обрыве
// соединения переподключается с экспоненциальной задержкой. После отмены ctx
// дожидается завершения начатых обработок; сообщения, обработка которых
// прервана отменой, возвращаются в очередь.
func (c *Client) Consume(ctx context.Context, queue string, concurrency int, handler Handler) error {
	concurrency = max(concurrency, 1)

	for attempt := 1; ; attempt++ {
		started, err := c.consumeOnce(ctx, queue, concurrency, handler)
		if started {
			attempt = 1
		}
		if errors.Is(err, errClosed) {
			return err
		}

		// Остановка по ctx — штатное завершение: ошибка закрытого канала при
		// этом ожидаема и наружу не передаётся.
		delay := c.retry.Delay(min(attempt+1, 16))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		default:
		}

		c.log.Warn("потребление из очереди прервано, переподключение",
			zap.String("queue", queue), zap.Duration("delay", delay), zap.Error(err))

		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// consumeOnce потребляет сообщения по одному каналу, пока канал не закроется
// или не будет отменён ctx. started сообщает, что подписка была оформлена.
func (c *Client) consumeOnce(ctx context.Context, queue string, concurrency int, handler Handler) (bool, error) {
	ch, err := c.channel()
	if err != nil {
		return false, err
	}
	defer func() { _ = ch.Close() }()

	if err = ch.Qos(concurrency, 0, false); err != nil {
		return false, fmt.Errorf("не удалось задать предвыборку: %w", err)
	}

	tag := "worker-" + uuid.NewString()
	deliveries, err := ch.Consume(queue, tag, false, false, false, false, nil)
	if err != nil {
		return false, fmt.Errorf("не удалось подписаться на очередь %q: %w", queue, err)
	}
	c.log.Info("подписка на очередь оформлена", zap.String("queue", queue), zap.Int("handlers", concurrency))

	// После отмены ctx брокер перестаёт присылать сообщения, а канал deliveries
	// закрывается, что завершает обработчики.
	stop := context.AfterFunc(ctx, func() { _ = ch.Cancel(tag, false) })
	defer stop()

	var wg sync.WaitGroup
	for range concurrency {
		wg.Go(func() {
			for d := range deliveries {
				c.handleDelivery(ctx, d, handler)
			}
		})
	}
	wg.Wait()

	return true, fmt.Errorf("канал очереди %q закрыт", queue)
}

// handleDelivery передаёт сообщение обработчику и подтверждает его: при успехе
// - Ack, при ошибке - Nack без возврата в очередь (сообщение уходит в очередь
// недоставленных), при остановке - Nack с возвратом в очередь.
func (c *Client) handleDelivery(ctx context.Context, d amqp.Delivery, handler Handler) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, traceHeaders(d.Headers))
	ctx, span := otel.Tracer("gophprofile/broker").Start(ctx, "rabbitmq.process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination.name", d.Exchange),
			attribute.String("messaging.rabbitmq.destination.routing_key", d.RoutingKey)),
	)
	defer span.End()

	msg := Message{ID: d.MessageId, RoutingKey: d.RoutingKey, Body: d.Body, Redelivered: d.Redelivered}
	log := c.log.Ctx(ctx).With(zap.String("message", msg.ID), zap.String("key", msg.RoutingKey))

	err := safeHandle(ctx, handler, msg)

	var ackErr error
	switch {
	case err == nil:
		ackErr = d.Ack(false)
	case ctx.Err() != nil:
		log.Info("обработка прервана остановкой, сообщение возвращено в очередь", zap.Error(err))
		ackErr = d.Nack(false, true)
	default:
		log.Error("сообщение не обработано и отправлено в очередь недоставленных", zap.Error(err))
		ackErr = d.Nack(false, false)
	}
	if ackErr != nil {
		log.Error("не удалось подтвердить сообщение", zap.Error(ackErr))
	}
	if err != nil || ackErr != nil {
		span.SetStatus(codes.Error, "")
	}
}

// safeHandle вызывает handler, превращая панику в ошибку.
func safeHandle(ctx context.Context, handler Handler, msg Message) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("паника при обработке сообщения: %v", r)
		}
	}()

	return handler(ctx, msg)
}
