package broker

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
)

const (
	// Exchange - обменник событий аватарок (direct).
	Exchange = "avatars.exchange"
	// DeadLetterExchange - обменник, в который попадают сообщения, которые
	// воркер не смог обработать (direct, с теми же ключами маршрутизации).
	DeadLetterExchange = "avatars.dlx"
	// QueueUploaded - очередь событий загрузки аватарок.
	QueueUploaded = "avatars.uploaded"
	// QueueDeleted - очередь событий удаления аватарок.
	QueueDeleted = "avatars.deleted"
	// deadLetterSuffix - суффикс имени очереди недоставленных сообщений.
	deadLetterSuffix = ".dlq"
)

// binding - привязка очереди к обменнику событий по ключу маршрутизации.
type binding struct {
	queue      string
	routingKey string
}

// bindings - очереди событий и их ключи маршрутизации.
var bindings = []binding{
	{queue: QueueUploaded, routingKey: domain.RoutingKeyAvatarUploaded},
	{queue: QueueDeleted, routingKey: domain.RoutingKeyAvatarDeleted},
}

// DeadLetterQueue возвращает имя очереди недоставленных сообщений для
// очереди queue.
func DeadLetterQueue(queue string) string {
	return queue + deadLetterSuffix
}

// declarer - часть *amqp.Channel, нужная для объявления топологии.
type declarer interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
}

// declareTopology объявляет обменники и очереди. Объявление идемпотентно,
// поэтому выполняется при каждом подключении. Отклонённое сообщение очереди
// событий через DeadLetterExchange с исходным ключом маршрутизации попадает в
// её очередь недоставленных сообщений.
func declareTopology(ch declarer) error {
	for _, exchange := range []string{Exchange, DeadLetterExchange} {
		if err := ch.ExchangeDeclare(exchange, amqp.ExchangeDirect, true, false, false, false, nil); err != nil {
			return fmt.Errorf("не удалось объявить обменник %q: %w", exchange, err)
		}
	}

	for _, b := range bindings {
		dlq := DeadLetterQueue(b.queue)
		if err := declareBoundQueue(ch, dlq, b.routingKey, DeadLetterExchange, nil); err != nil {
			return err
		}

		args := amqp.Table{"x-dead-letter-exchange": DeadLetterExchange}
		if err := declareBoundQueue(ch, b.queue, b.routingKey, Exchange, args); err != nil {
			return err
		}
	}

	return nil
}

// declareBoundQueue объявляет надёжную очередь и привязывает её к обменнику.
func declareBoundQueue(ch declarer, queue, routingKey, exchange string, args amqp.Table) error {
	if _, err := ch.QueueDeclare(queue, true, false, false, false, args); err != nil {
		return fmt.Errorf("не удалось объявить очередь %q: %w", queue, err)
	}
	if err := ch.QueueBind(queue, routingKey, exchange, false, nil); err != nil {
		return fmt.Errorf("не удалось привязать очередь %q к %q: %w", queue, exchange, err)
	}

	return nil
}
