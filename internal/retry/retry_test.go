package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("сбой")

// fastPolicy - политика с короткими задержками для тестов.
func fastPolicy(attempts int) Policy {
	return Policy{Initial: time.Millisecond, Max: 4 * time.Millisecond, Multiplier: 2, MaxAttempts: attempts}
}

func TestPolicy_Delay(t *testing.T) {
	p := Policy{Initial: 100 * time.Millisecond, Max: time.Second, Multiplier: 2}

	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{name: "перед первой попыткой задержки нет", attempt: 1, want: 0},
		{name: "вторая попытка", attempt: 2, want: 100 * time.Millisecond},
		{name: "третья попытка", attempt: 3, want: 200 * time.Millisecond},
		{name: "пятая попытка", attempt: 5, want: 800 * time.Millisecond},
		{name: "ограничение сверху", attempt: 6, want: time.Second},
		{name: "далеко за ограничением", attempt: 100, want: time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, p.Delay(tt.attempt))
		})
	}

	t.Run("множитель меньше 1 считается равным 1", func(t *testing.T) {
		p := Policy{Initial: 50 * time.Millisecond, Max: time.Second, Multiplier: 0.5}
		assert.Equal(t, 50*time.Millisecond, p.Delay(4))
	})
}

func TestPolicy_WithJitter(t *testing.T) {
	p := Policy{Jitter: 0.5}
	for range 100 {
		got := p.withJitter(100 * time.Millisecond)
		assert.GreaterOrEqual(t, got, 50*time.Millisecond)
		assert.LessOrEqual(t, got, 150*time.Millisecond)
	}

	assert.Equal(t, time.Second, Policy{}.withJitter(time.Second), "без разброса задержка не меняется")
}

func TestDefault(t *testing.T) {
	p := Default()
	assert.Equal(t, 5, p.MaxAttempts)
	assert.Equal(t, 800*time.Millisecond, p.Delay(5))
}

func TestDo(t *testing.T) {
	tests := []struct {
		wantErr   error
		name      string
		results   []error
		attempts  int
		wantCalls int
	}{
		{name: "успех с первой попытки", attempts: 3, results: []error{nil}, wantCalls: 1},
		{name: "успех после сбоев", attempts: 3, results: []error{errBoom, errBoom, nil}, wantCalls: 3},
		{name: "попытки исчерпаны", attempts: 3, results: []error{errBoom, errBoom, errBoom}, wantCalls: 3, wantErr: errBoom},
		{name: "постоянная ошибка", attempts: 3, results: []error{Permanent(errBoom)}, wantCalls: 1, wantErr: errBoom},
		{name: "без повторов", attempts: 0, results: []error{errBoom}, wantCalls: 1, wantErr: errBoom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := Do(context.Background(), fastPolicy(tt.attempts), func(context.Context) error {
				err := tt.results[calls]
				calls++
				return err
			})

			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.False(t, IsPermanent(err), "наружу возвращается исходная ошибка")
		})
	}
}

func TestDo_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	err := Do(ctx, Policy{Initial: time.Hour, Max: time.Hour, MaxAttempts: 3}, func(context.Context) error {
		calls++
		cancel()
		return errBoom
	})

	assert.Equal(t, 1, calls)
	require.ErrorIs(t, err, errBoom)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDoValue(t *testing.T) {
	calls := 0
	got, err := DoValue(context.Background(), fastPolicy(3), func(context.Context) (int, error) {
		calls++
		if calls < 2 {
			return 0, errBoom
		}
		return 42, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 42, got)
	assert.Equal(t, 2, calls)
}

func TestPermanent(t *testing.T) {
	require.NoError(t, Permanent(nil))

	err := Permanent(errBoom)
	assert.True(t, IsPermanent(err))
	assert.True(t, IsPermanent(errors.Join(errors.New("обёртка"), err)))
	assert.False(t, IsPermanent(errBoom))
	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, errBoom.Error(), err.Error())
}
