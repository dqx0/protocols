package httpx

import "context"

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyCorrelationID
)

// WithRequestID returns a new context that carries a request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestIDFrom extracts the request ID from ctx.
func RequestIDFrom(ctx context.Context) (string, bool) {
	v := ctx.Value(ctxKeyRequestID)
	if v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

// WithCorrelationID returns a new context that carries a correlation ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyCorrelationID, id)
}

// CorrelationIDFrom extracts the correlation ID from ctx.
func CorrelationIDFrom(ctx context.Context) (string, bool) {
	v := ctx.Value(ctxKeyCorrelationID)
	if v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}
