//go:build integration

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
)

// BrokerSuite проверяет Client на настоящем RabbitMQ в контейнере.
type BrokerSuite struct {
	suite.Suite
	container *tcrabbit.RabbitMQContainer
	client    *Client
	url       string
}

func TestBrokerSuite(t *testing.T) {
	suite.Run(t, new(BrokerSuite))
}

func (s *BrokerSuite) SetupSuite() {
	ctx := context.Background()

	container, err := tcrabbit.Run(ctx, "rabbitmq:4-management-alpine")
	s.Require().NoError(err)
	s.container = container

	s.url, err = container.AmqpURL(ctx)
	s.Require().NoError(err)
}

func (s *BrokerSuite) TearDownSuite() {
	testcontainers.CleanupContainer(s.T(), s.container)
}

func (s *BrokerSuite) SetupTest() {
	var err error
	s.client, err = Dial(s.url, logger.Nop())
	s.Require().NoError(err)
	s.client.retry = fastRetry()

	ch, err := s.client.channel()
	s.Require().NoError(err)
	defer func() { _ = ch.Close() }()
	for _, q := range []string{QueueUploaded, QueueDeleted, DeadLetterQueue(QueueUploaded), DeadLetterQueue(QueueDeleted)} {
		_, err := ch.QueuePurge(q, false)
		s.Require().NoError(err)
	}
}

func (s *BrokerSuite) TearDownTest() {
	s.NoError(s.client.Close())
}

// consume запускает потребление из queue в фоне и возвращает функцию остановки.
func (s *BrokerSuite) consume(queue string, handler Handler) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.client.Consume(ctx, queue, 2, handler) }()

	return func() {
		cancel()
		select {
		case err := <-done:
			s.NoError(err)
		case <-time.After(10 * time.Second):
			s.Fail("Consume не завершился после отмены контекста")
		}
	}
}

// queueLen возвращает число сообщений в очереди.
func (s *BrokerSuite) queueLen(queue string) int {
	ch, err := s.client.channel()
	s.Require().NoError(err)
	defer func() { _ = ch.Close() }()

	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, queueArgs(queue))
	s.Require().NoError(err)

	return q.Messages
}

// queueArgs возвращает аргументы, с которыми очередь объявлена в топологии.
func queueArgs(queue string) amqp.Table {
	if queue == QueueUploaded || queue == QueueDeleted {
		return amqp.Table{"x-dead-letter-exchange": DeadLetterExchange}
	}
	return nil
}

func (s *BrokerSuite) TestPing() {
	s.NoError(s.client.Ping(context.Background()))
}

func (s *BrokerSuite) TestPublishAndConsume() {
	ctx := context.Background()
	event := domain.AvatarUploadEvent{AvatarID: "a-1", UserID: "u-1", S3Key: "originals/a-1"}
	s.Require().NoError(s.client.Publish(ctx, domain.RoutingKeyAvatarUploaded, event))

	got := make(chan Message, 1)
	stop := s.consume(QueueUploaded, func(_ context.Context, msg Message) error {
		got <- msg
		return nil
	})
	defer stop()

	select {
	case msg := <-got:
		s.NotEmpty(msg.ID)
		s.Equal(domain.RoutingKeyAvatarUploaded, msg.RoutingKey)
		var decoded domain.AvatarUploadEvent
		s.Require().NoError(json.Unmarshal(msg.Body, &decoded))
		s.Equal(event, decoded)
	case <-time.After(10 * time.Second):
		s.Fail("сообщение не получено")
	}

	s.Eventually(func() bool { return s.queueLen(QueueUploaded) == 0 }, 5*time.Second, 50*time.Millisecond,
		"обработанное сообщение подтверждено")
	s.Equal(0, s.queueLen(QueueDeleted), "событие загрузки не попадает в очередь удаления")
}

func (s *BrokerSuite) TestFailedMessageGoesToDeadLetterQueue() {
	s.Require().NoError(s.client.Publish(context.Background(), domain.RoutingKeyAvatarDeleted,
		domain.AvatarDeleteEvent{AvatarID: "a-1", S3Keys: []string{"k"}}))

	stop := s.consume(QueueDeleted, func(context.Context, Message) error {
		return errors.New("не удалось")
	})
	defer stop()

	s.Eventually(func() bool { return s.queueLen(DeadLetterQueue(QueueDeleted)) == 1 }, 10*time.Second, 50*time.Millisecond)
	s.Equal(0, s.queueLen(QueueDeleted))
}

func (s *BrokerSuite) TestInterruptedMessageIsRequeued() {
	s.Require().NoError(s.client.Publish(context.Background(), domain.RoutingKeyAvatarUploaded, domain.AvatarUploadEvent{AvatarID: "a-1"}))

	started := make(chan struct{})
	stop := s.consume(QueueUploaded, func(ctx context.Context, _ Message) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		s.Fail("обработка не началась")
	}
	stop()

	s.Eventually(func() bool { return s.queueLen(QueueUploaded) == 1 }, 5*time.Second, 50*time.Millisecond,
		"прерванное остановкой сообщение вернулось в очередь")
	s.Equal(0, s.queueLen(DeadLetterQueue(QueueUploaded)))
}

func (s *BrokerSuite) TestReconnectAfterConnectionLoss() {
	var (
		mu       sync.Mutex
		received []string
	)
	stop := s.consume(QueueUploaded, func(_ context.Context, msg Message) error {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg.ID)
		return nil
	})
	defer stop()

	ctx := context.Background()
	s.Require().NoError(s.client.Publish(ctx, domain.RoutingKeyAvatarUploaded, domain.AvatarUploadEvent{AvatarID: "1"}))
	s.Eventually(func() bool { mu.Lock(); defer mu.Unlock(); return len(received) == 1 }, 10*time.Second, 50*time.Millisecond)

	// Обрыв соединения: публикация и потребление должны восстановиться.
	s.client.mu.Lock()
	s.Require().NoError(s.client.conn.Close())
	s.client.mu.Unlock()

	s.Require().NoError(s.client.Publish(ctx, domain.RoutingKeyAvatarUploaded, domain.AvatarUploadEvent{AvatarID: "2"}))
	s.Eventually(func() bool { mu.Lock(); defer mu.Unlock(); return len(received) == 2 }, 15*time.Second, 50*time.Millisecond)
}
