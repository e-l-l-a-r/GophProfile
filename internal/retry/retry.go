// Package retry повторяет операции с экспоненциальной задержкой (exponential
// backoff) и случайным разбросом (jitter). Повтор прекращается при успехе,
// исчерпании попыток, отмене контекста или постоянной ошибке (Permanent).
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// Policy - параметры повторов.
type Policy struct {
	// Initial - задержка перед второй попыткой.
	Initial time.Duration
	// Max - верхняя граница задержки.
	Max time.Duration
	// Multiplier - множитель задержки между соседними попытками (не меньше 1).
	Multiplier float64
	// Jitter - доля случайного разброса задержки от 0 до 1: при 0.2 задержка
	// выбирается из диапазона [0.8d, 1.2d].
	Jitter float64
	// MaxAttempts - общее число попыток, включая первую; 0 и 1 - без повторов.
	MaxAttempts int
}

// Default возвращает политику по умолчанию: 5 попыток с задержками около
// 100 мс, 200 мс, 400 мс и 800 мс.
func Default() Policy {
	return Policy{
		Initial:     100 * time.Millisecond,
		Max:         5 * time.Second,
		Multiplier:  2,
		Jitter:      0.2,
		MaxAttempts: 5,
	}
}

// Delay возвращает задержку без разброса перед попыткой с номером attempt
// (попытки нумеруются с 1; перед первой задержки нет).
func (p Policy) Delay(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}

	delay := float64(p.Initial)
	for range attempt - 2 {
		delay *= max(p.Multiplier, 1)
		if delay >= float64(p.Max) {
			return p.Max
		}
	}

	return min(time.Duration(delay), p.Max)
}

// withJitter добавляет к задержке d случайный разброс согласно Jitter.
func (p Policy) withJitter(d time.Duration) time.Duration {
	if p.Jitter <= 0 || d <= 0 {
		return d
	}

	spread := float64(d) * min(p.Jitter, 1)

	return time.Duration(float64(d) - spread + rand.Float64()*2*spread) //nolint:gosec // криптостойкость не нужна
}

// permanentError - ошибка, после которой повторять операцию бессмысленно.
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

// Permanent помечает ошибку err постоянной: Do прекращает повторы и
// возвращает err. Для nil возвращает nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return &permanentError{err: err}
}

// IsPermanent сообщает, помечена ли ошибка err постоянной.
func IsPermanent(err error) bool {
	_, ok := errors.AsType[*permanentError](err)

	return ok
}

// Do выполняет fn, повторяя её при ошибке согласно политике p. Возвращает nil
// при успехе, исходную ошибку - при постоянной ошибке, а при исчерпании
// попыток или отмене ctx - последнюю ошибку fn, обёрнутую с пояснением.
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	_, err := DoValue(ctx, p, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})

	return err
}

// DoValue работает как Do для операций, возвращающих значение.
func DoValue[T any](ctx context.Context, p Policy, fn func(ctx context.Context) (T, error)) (T, error) {
	attempts := max(p.MaxAttempts, 1)

	var (
		zero T
		err  error
	)
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			timer := time.NewTimer(p.withJitter(p.Delay(attempt)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, fmt.Errorf("повторы прерваны после %d попыток: %w", attempt-1, errors.Join(err, ctx.Err()))
			case <-timer.C:
			}
		}

		var value T
		value, err = fn(ctx)
		if err == nil {
			return value, nil
		}
		if permanent, ok := errors.AsType[*permanentError](err); ok {
			return zero, permanent.err
		}
	}

	return zero, fmt.Errorf("попытки исчерпаны (%d): %w", attempts, err)
}
