package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/e-l-l-a-r/gophprofile/internal/retry"
	"github.com/e-l-l-a-r/gophprofile/internal/telemetry"
)

// errNotConfirmed возвращается, если брокер отверг опубликованное сообщение.
var errNotConfirmed = errors.New("брокер не подтвердил сообщение")

// Publish публикует событие event в формате JSON в обменник Exchange с ключом
// routingKey и ждёт подтверждения брокера. Сообщение сохраняется на диске
// брокера и получает уникальный MessageId, который при повторах публикации
// не меняется: по нему воркер распознаёт повторную доставку. Временные сбои
// повторяются с экспоненциальной задержкой.
func (c *Client) Publish(ctx context.Context, routingKey string, event any) (err error) {
	ctx, span := otel.Tracer("gophprofile/broker").Start(ctx, "rabbitmq.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attribute.String("messaging.system", "rabbitmq"), attribute.String("peer.service", "rabbitmq"),
			attribute.String("messaging.destination.name", Exchange),
			attribute.String("messaging.rabbitmq.destination.routing_key", routingKey)),
	)
	defer telemetry.EndSpan(span, &err)

	msg, err := newPublishing(routingKey, event)
	if err != nil {
		return err
	}
	msg.Headers = amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, traceHeaders(msg.Headers))

	if err := retry.Do(ctx, c.retry, func(ctx context.Context) error {
		return c.publishOnce(ctx, routingKey, msg)
	}); err != nil {
		return fmt.Errorf("не удалось опубликовать событие %q: %w", routingKey, err)
	}

	return nil
}

// newPublishing готовит сообщение с событием event.
func newPublishing(routingKey string, event any) (amqp.Publishing, error) {
	body, err := json.Marshal(event)
	if err != nil {
		return amqp.Publishing{}, fmt.Errorf("не удалось сериализовать событие %q: %w", routingKey, err)
	}

	return amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    uuid.NewString(),
		Timestamp:    time.Now().UTC(),
		Type:         routingKey,
		Body:         body,
	}, nil
}

// publishOnce выполняет одну попытку публикации. При ошибке канал публикации
// сбрасывается, чтобы следующая попытка открыла новый.
func (c *Client) publishOnce(ctx context.Context, routingKey string, msg amqp.Publishing) error {
	ch, err := c.publishChannel()
	if err != nil {
		if errors.Is(err, errClosed) {
			return retry.Permanent(err)
		}
		return err
	}

	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, Exchange, routingKey, false, false, msg)
	if err != nil {
		c.resetPublishChannel(ch)
		return fmt.Errorf("ошибка публикации: %w", err)
	}

	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		c.resetPublishChannel(ch)
		return fmt.Errorf("не дождались подтверждения брокера: %w", err)
	}
	if !acked {
		return errNotConfirmed
	}

	return nil
}

// publishChannel возвращает канал публикации в режиме подтверждений,
// открывая его при необходимости.
func (c *Client) publishChannel() (*amqp.Channel, error) {
	c.pubMu.Lock()
	defer c.pubMu.Unlock()

	if c.pubCh != nil && !c.pubCh.IsClosed() {
		return c.pubCh, nil
	}

	ch, err := c.channel()
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("не удалось включить подтверждения публикации: %w", err)
	}
	c.pubCh = ch

	return ch, nil
}

// resetPublishChannel закрывает канал публикации ch, если он всё ещё текущий.
func (c *Client) resetPublishChannel(ch *amqp.Channel) {
	c.pubMu.Lock()
	defer c.pubMu.Unlock()

	if c.pubCh == ch {
		_ = ch.Close()
		c.pubCh = nil
	}
}
