package broker

import amqp "github.com/rabbitmq/amqp091-go"

type traceHeaders amqp.Table

func (h traceHeaders) Get(key string) string {
	value, _ := h[key].(string)
	return value
}

func (h traceHeaders) Set(key, value string) { h[key] = value }

func (h traceHeaders) Keys() []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	return keys
}
