package logger

import (
	"context"
	"io"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// contextKey - имя поля с context.Context: по нему otelzap берёт trace и span.
const contextKey = "context"

// stdoutCore пишет записи в формате JSON. Поле с context.Context он заменяет
// на trace_id и span_id, поэтому контекст в вывод не сериализуется.
type stdoutCore struct {
	zapcore.Core
}

func newStdoutCore(w io.Writer, service string) zapcore.Core {
	cfg := zap.NewProductionEncoderConfig()
	cfg.TimeKey = "time"
	cfg.EncodeTime = zapcore.RFC3339NanoTimeEncoder
	core := zapcore.NewCore(zapcore.NewJSONEncoder(cfg), zapcore.AddSync(w), zapcore.DebugLevel)

	return stdoutCore{Core: core.With([]zapcore.Field{zap.String("service", service)})}
}

func (c stdoutCore) With(fields []zapcore.Field) zapcore.Core {
	return stdoutCore{Core: c.Core.With(withTraceFields(fields))}
}

func (c stdoutCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}

	return ce
}

func (c stdoutCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	return c.Core.Write(ent, withTraceFields(fields))
}

// withTraceFields заменяет поле с контекстом полями trace_id и span_id.
func withTraceFields(fields []zapcore.Field) []zapcore.Field {
	out := make([]zapcore.Field, 0, len(fields)+2)
	for _, f := range fields {
		ctx, ok := f.Interface.(context.Context)
		if f.Key != contextKey || !ok {
			out = append(out, f)
			continue
		}
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			out = append(out, zap.String("trace_id", sc.TraceID().String()), zap.String("span_id", sc.SpanID().String()))
		}
	}

	return out
}
