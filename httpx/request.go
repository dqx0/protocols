package httpx

import (
    "context"
    "io"
    "net/url"
)

// Request represents an HTTP request.
//
// Fields are a subset tailored for HTTP/1.1. Body is an io.ReadCloser.
// ContentLength is -1 when unknown. Context can be set via WithContext.
type Request struct {
    Method        string
    URL           *url.URL
    RequestURI    string
    Proto         string
    Header        Header
    Body          io.ReadCloser
    // GetBody, if non-nil, returns a new copy of Body for retransmission
    // (e.g., redirects 307/308). The caller must Close the returned body.
    GetBody       func() (io.ReadCloser, error)
    Host          string
    ContentLength int64
    ctx           context.Context
    // RequestID is the server/client generated identifier for this request.
    RequestID     string
    // CorrelationID is a propagated ID from the peer (e.g., X-Request-ID/Traceparent).
    CorrelationID string
    // TraceID is the W3C trace-id (32 hex). If empty, a new one may be generated for outbound requests.
    TraceID       string
    // SpanID is the current span id (16 hex). For inbound requests, the server generates a new one.
    SpanID        string
    // ParentSpanID is the upstream span id, if parsed from traceparent.
    ParentSpanID  string
    // TraceState carries tracestate header content, if any, for propagation.
    TraceState    string
}

// Context returns the request's context. If nil, returns Background.
func (r *Request) Context() context.Context {
    if r == nil || r.ctx == nil { return context.Background() }
    return r.ctx
}

// WithContext returns a shallow copy of r with its context changed to ctx.
func WithContext(r *Request, ctx context.Context) *Request {
    if r == nil { return nil }
    r2 := *r
    r2.ctx = ctx
    return &r2
}
