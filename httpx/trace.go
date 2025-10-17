package httpx

import (
    "context"
    "crypto/rand"
    "encoding/hex"
    "strings"
)

func genTraceID() string {
    var b [16]byte
    for {
        if _, err := rand.Read(b[:]); err == nil {
            // Must not be all zeros
            zero := true
            for _, v := range b {
                if v != 0 { zero = false; break }
            }
            if !zero {
                return strings.ToLower(hex.EncodeToString(b[:]))
            }
        }
        // retry on error or all-zero
    }
}

func genSpanID() string {
    var b [8]byte
    for {
        if _, err := rand.Read(b[:]); err == nil {
            zero := true
            for _, v := range b {
                if v != 0 { zero = false; break }
            }
            if !zero {
                return strings.ToLower(hex.EncodeToString(b[:]))
            }
        }
    }
}

// parseTraceparent extracts trace-id, span-id, flags. Returns ok=false if invalid.
func parseTraceparent(v string) (traceID, spanID, flags string, ok bool) {
    if v == "" { return "", "", "", false }
    v = strings.TrimSpace(v)
    parts := strings.Split(v, "-")
    if len(parts) < 4 { return "", "", "", false }
    ver, tid, sid, fl := parts[0], parts[1], parts[2], parts[3]
    if len(ver) != 2 || len(tid) != 32 || len(sid) != 16 || len(fl) != 2 {
        return "", "", "", false
    }
    // Basic hex validation
    if !isHex(tid) || !isHex(sid) || !isHex(fl) { return "", "", "", false }
    if strings.ToLower(tid) == strings.Repeat("0", 32) || strings.ToLower(sid) == strings.Repeat("0", 16) {
        return "", "", "", false
    }
    return strings.ToLower(tid), strings.ToLower(sid), strings.ToLower(fl), true
}

func formatTraceparent(traceID, spanID, flags string) string {
    if flags == "" { flags = "01" }
    return "00-" + strings.ToLower(traceID) + "-" + strings.ToLower(spanID) + "-" + strings.ToLower(flags)
}

func isHex(s string) bool {
    for i := 0; i < len(s); i++ {
        c := s[i]
        if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
            continue
        }
        return false
    }
    return true
}

// Trace carries minimal trace context for propagation.
// Trace carries minimal W3C trace context for propagation.
// TraceID is 32‑hex, SpanID is 16‑hex. Flags are 2‑hex (e.g. "01").
type Trace struct {
    TraceID      string
    SpanID       string
    ParentSpanID string
    Flags        string // 2‑hex digit flags (e.g., "01")
}

type traceKeyType struct{}
var traceKey traceKeyType

// WithTrace stores trace context in ctx.
func WithTrace(ctx context.Context, tr Trace) context.Context {
    return context.WithValue(ctx, traceKey, tr)
}

// TraceFrom extracts trace context from ctx.
func TraceFrom(ctx context.Context) (Trace, bool) {
    if v := ctx.Value(traceKey); v != nil {
        if tr, ok := v.(Trace); ok {
            return tr, true
        }
    }
    return Trace{}, false
}
