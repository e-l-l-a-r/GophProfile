// Package broker реализует обмен событиями аватарок через RabbitMQ: топологию
// (direct-обменник, очереди событий и очереди недоставленных сообщений),
// публикацию с подтверждениями брокера и потребление с ручным подтверждением.
// При обрыве соединения клиент переподключается при следующем обращении.
package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/retry"
)

// errClosed возвращается при обращении к закрытому клиенту.
var errClosed = errors.New("клиент брокера закрыт")

// Client - подключение к RabbitMQ.
type Client struct {
	conn  *amqp.Connection
	pubCh *amqp.Channel
	log   *logger.Logger
	url   string
	retry retry.Policy
	// mu защищает conn и closed, pubMu - канал публикации pubCh.
	mu     sync.Mutex
	pubMu  sync.Mutex
	closed bool
}

// New создаёт клиент RabbitMQ по адресу url без подключения: подключение
// устанавливается вызовом Connect или при первом обращении.
func New(url string, log *logger.Logger) *Client {
	return &Client{url: url, log: log, retry: retry.Default()}
}

// Dial подключается к RabbitMQ по адресу url и объявляет топологию.
func Dial(url string, log *logger.Logger) (*Client, error) {
	c := New(url, log)
	if err := c.Connect(); err != nil {
		return nil, err
	}

	return c, nil
}

// Connect подключается к брокеру и объявляет топологию.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return errClosed
	}

	return c.connectLocked()
}

// connectLocked устанавливает соединение и объявляет топологию. Вызывается
// под c.mu.
func (c *Client) connectLocked() error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return fmt.Errorf("не удалось подключиться к брокеру: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("не удалось открыть канал брокера: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := declareTopology(ch); err != nil {
		_ = conn.Close()
		return err
	}

	c.conn = conn

	return nil
}

// channel открывает новый канал, при необходимости переподключаясь.
func (c *Client) channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, errClosed
	}
	if c.conn == nil || c.conn.IsClosed() {
		c.log.Warn("соединение с брокером потеряно, переподключение")
		if err := c.connectLocked(); err != nil {
			return nil, err
		}
	}

	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть канал брокера: %w", err)
	}

	return ch, nil
}

// Ping проверяет доступность брокера, открывая и закрывая канал.
func (c *Client) Ping(_ context.Context) error {
	ch, err := c.channel()
	if err != nil {
		return fmt.Errorf("брокер недоступен: %w", err)
	}
	if err := ch.Close(); err != nil {
		return fmt.Errorf("брокер недоступен: %w", err)
	}

	return nil
}

// Close закрывает соединение. Последующие обращения возвращают ошибку.
func (c *Client) Close() error {
	c.pubMu.Lock()
	c.pubCh = nil
	c.pubMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	if c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("не удалось закрыть соединение с брокером: %w", err)
	}

	return nil
}
