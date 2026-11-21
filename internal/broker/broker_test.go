package broker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/retry"
)

var errBoom = errors.New("сбой брокера")

// fastRetry - политика повторов с короткими задержками для тестов.
func fastRetry() retry.Policy {
	return retry.Policy{Initial: time.Millisecond, Max: 10 * time.Millisecond, Multiplier: 2, MaxAttempts: 3}
}

// fakeDeclarer запоминает объявленные обменники, очереди и привязки.
type fakeDeclarer struct {
	failOn    string
	queueArgs map[string]amqp.Table
	exchanges []string
	bindings  []string
}

func (f *fakeDeclarer) ExchangeDeclare(name, kind string, durable, _, _, _ bool, _ amqp.Table) error {
	if f.failOn == "exchange:"+name {
		return errBoom
	}
	f.exchanges = append(f.exchanges, name+"/"+kind+"/"+boolStr(durable))

	return nil
}

func (f *fakeDeclarer) QueueDeclare(name string, durable, _, _, _ bool, args amqp.Table) (amqp.Queue, error) {
	if f.failOn == "queue:"+name {
		return amqp.Queue{}, errBoom
	}
	if f.queueArgs == nil {
		f.queueArgs = map[string]amqp.Table{}
	}
	f.queueArgs[name+"/"+boolStr(durable)] = args

	return amqp.Queue{Name: name}, nil
}

func (f *fakeDeclarer) QueueBind(name, key, exchange string, _ bool, _ amqp.Table) error {
	if f.failOn == "bind:"+name {
		return errBoom
	}
	f.bindings = append(f.bindings, exchange+" -["+key+"]-> "+name)

	return nil
}

func boolStr(b bool) string {
	if b {
		return "durable"
	}
	return "transient"
}

func TestDeclareTopology(t *testing.T) {
	f := &fakeDeclarer{}
	require.NoError(t, declareTopology(f))

	assert.Equal(t, []string{"avatars.exchange/direct/durable", "avatars.dlx/direct/durable"}, f.exchanges)
	assert.ElementsMatch(t, []string{
		"avatars.exchange -[avatar.uploaded]-> avatars.uploaded",
		"avatars.dlx -[avatar.uploaded]-> avatars.uploaded.dlq",
		"avatars.exchange -[avatar.deleted]-> avatars.deleted",
		"avatars.dlx -[avatar.deleted]-> avatars.deleted.dlq",
	}, f.bindings)

	dlx := amqp.Table{"x-dead-letter-exchange": DeadLetterExchange}
	assert.Equal(t, map[string]amqp.Table{
		"avatars.uploaded/durable":     dlx,
		"avatars.uploaded.dlq/durable": nil,
		"avatars.deleted/durable":      dlx,
		"avatars.deleted.dlq/durable":  nil,
	}, f.queueArgs)
}

func TestDeclareTopology_Errors(t *testing.T) {
	for _, failOn := range []string{
		"exchange:" + Exchange,
		"exchange:" + DeadLetterExchange,
		"queue:" + QueueUploaded,
		"queue:" + DeadLetterQueue(QueueDeleted),
		"bind:" + QueueDeleted,
	} {
		t.Run(failOn, func(t *testing.T) {
			require.ErrorIs(t, declareTopology(&fakeDeclarer{failOn: failOn}), errBoom)
		})
	}
}

func TestNewPublishing(t *testing.T) {
	event := domain.AvatarUploadEvent{AvatarID: "a", UserID: "u", S3Key: "k"}

	msg, err := newPublishing(domain.RoutingKeyAvatarUploaded, event)
	require.NoError(t, err)

	assert.Equal(t, "application/json", msg.ContentType)
	assert.Equal(t, amqp.Persistent, msg.DeliveryMode)
	assert.Equal(t, domain.RoutingKeyAvatarUploaded, msg.Type)
	assert.NotEmpty(t, msg.MessageId)
	assert.False(t, msg.Timestamp.IsZero())
	assert.JSONEq(t, `{"avatar_id":"a","user_id":"u","s3_key":"k"}`, string(msg.Body))

	other, err := newPublishing(domain.RoutingKeyAvatarUploaded, event)
	require.NoError(t, err)
	assert.NotEqual(t, msg.MessageId, other.MessageId, "идентификаторы сообщений уникальны")

	_, err = newPublishing("x", make(chan int))
	require.Error(t, err)
}

// fakeAcknowledger запоминает, как было подтверждено сообщение.
type fakeAcknowledger struct {
	err     error
	result  string
	mu      sync.Mutex
	settled int
}

func (f *fakeAcknowledger) Ack(uint64, bool) error {
	return f.settle("ack")
}

func (f *fakeAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	if requeue {
		return f.settle("nack-requeue")
	}
	return f.settle("nack-dlq")
}

func (f *fakeAcknowledger) Reject(uint64, bool) error {
	return f.settle("reject")
}

func (f *fakeAcknowledger) settle(result string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = result
	f.settled++

	return f.err
}

func TestHandleDelivery(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		ctx     context.Context
		handler Handler
		ackErr  error
		name    string
		want    string
	}{
		{
			name: "успех - ack", ctx: context.Background(), want: "ack",
			handler: func(context.Context, Message) error { return nil },
		},
		{
			name: "ошибка - в очередь недоставленных", ctx: context.Background(), want: "nack-dlq",
			handler: func(context.Context, Message) error { return errBoom },
		},
		{
			name: "паника - в очередь недоставленных", ctx: context.Background(), want: "nack-dlq",
			handler: func(context.Context, Message) error { panic("бум") },
		},
		{
			name: "остановка - возврат в очередь", ctx: canceled, want: "nack-requeue",
			handler: func(ctx context.Context, _ Message) error { return ctx.Err() },
		},
		{
			name: "сбой подтверждения только логируется", ctx: context.Background(), want: "ack", ackErr: errBoom,
			handler: func(context.Context, Message) error { return nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{log: logger.Nop()}
			ack := &fakeAcknowledger{err: tt.ackErr}
			d := amqp.Delivery{
				Acknowledger: ack,
				MessageId:    "m-1",
				RoutingKey:   domain.RoutingKeyAvatarUploaded,
				Redelivered:  true,
				Body:         []byte(`{}`),
			}

			var got Message
			c.handleDelivery(tt.ctx, d, func(ctx context.Context, msg Message) error {
				got = msg
				return tt.handler(ctx, msg)
			})

			assert.Equal(t, tt.want, ack.result)
			assert.Equal(t, 1, ack.settled)
			assert.Equal(t, Message{ID: "m-1", RoutingKey: domain.RoutingKeyAvatarUploaded, Body: []byte(`{}`), Redelivered: true}, got)
		})
	}
}

func TestDial_Unreachable(t *testing.T) {
	_, err := Dial("amqp://guest:guest@127.0.0.1:1/", logger.Nop())
	require.Error(t, err)
}

func TestClient_Closed(t *testing.T) {
	c := New("amqp://127.0.0.1:1/", logger.Nop())
	c.retry = fastRetry()
	require.NoError(t, c.Close())

	require.ErrorIs(t, c.Connect(), errClosed)
	require.ErrorIs(t, c.Ping(context.Background()), errClosed)
	require.ErrorIs(t, c.Publish(context.Background(), "k", struct{}{}), errClosed)
	require.ErrorIs(t, c.Consume(context.Background(), QueueUploaded, 1, nil), errClosed)
}

func TestClient_Publish_SerializationError(t *testing.T) {
	c := &Client{log: logger.Nop(), retry: fastRetry()}

	require.Error(t, c.Publish(context.Background(), "k", make(chan int)))
}

func TestDeadLetterQueue(t *testing.T) {
	assert.Equal(t, "avatars.uploaded.dlq", DeadLetterQueue(QueueUploaded))
}

func TestMessage_BodyIsEventJSON(t *testing.T) {
	msg, err := newPublishing(domain.RoutingKeyAvatarDeleted, domain.AvatarDeleteEvent{AvatarID: "a", S3Keys: []string{"k1", "k2"}})
	require.NoError(t, err)

	var got domain.AvatarDeleteEvent
	require.NoError(t, json.Unmarshal(msg.Body, &got))
	assert.Equal(t, []string{"k1", "k2"}, got.S3Keys)
}
