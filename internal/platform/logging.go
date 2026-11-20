package platform

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

type redactingHandler struct {
	inner slog.Handler
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(v.String()))
	case slog.KindGroup:
		orig := v.Group()
		cleaned := make([]any, 0, len(orig))
		for _, sub := range orig {
			cleaned = append(cleaned, redactAttr(sub))
		}
		return slog.Group(a.Key, cleaned...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok && err != nil {
			return slog.String(a.Key, Redact(err.Error()))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	cleanRec := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		cleanRec.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, cleanRec)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogger builds a structured JSON logger that passes every message, error,
// and string attribute value through Redact.
func NewLogger(w io.Writer, level string) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: parseLogLevel(level),
	})
	return slog.New(&redactingHandler{inner: base})
}
