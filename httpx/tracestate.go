package httpx

import (
    "strings"
)

// TraceStateBuilder provides safe construction of a W3C tracestate header value.
// It performs basic key/value validation and ordering (most-recent first).
type TraceStateBuilder struct {
    order []string            // keys in order
    kv    map[string]string   // normalized key -> value
}

// NewTraceStateBuilder parses an existing tracestate string.
func NewTraceStateBuilder(v string) *TraceStateBuilder {
    b := &TraceStateBuilder{kv: make(map[string]string)}
    v = strings.TrimSpace(v)
    if v == "" {
        return b
    }
    parts := strings.Split(v, ",")
    for _, part := range parts {
        part = strings.TrimSpace(part)
        if part == "" { continue }
        i := strings.IndexByte(part, '=')
        if i <= 0 { continue }
        k := strings.ToLower(strings.TrimSpace(part[:i]))
        val := strings.TrimSpace(part[i+1:])
        if !validTSKey(k) || !validTSValue(val) { continue }
        if _, ok := b.kv[k]; ok { continue }
        b.kv[k] = val
        b.order = append(b.order, k)
    }
    return b
}

// Set inserts or updates key with value. Newly set keys move to the front as per spec.
// Returns false if key/value invalid.
func (b *TraceStateBuilder) Set(key, value string) bool {
    k := strings.ToLower(strings.TrimSpace(key))
    v := strings.TrimSpace(value)
    if !validTSKey(k) || !validTSValue(v) {
        return false
    }
    if _, ok := b.kv[k]; ok {
        // remove existing key from order
        for i, ek := range b.order {
            if ek == k {
                b.order = append(b.order[:i], b.order[i+1:]...)
                break
            }
        }
    }
    b.kv[k] = v
    // prepend
    b.order = append([]string{k}, b.order...)
    return true
}

// String renders the tracestate.
func (b *TraceStateBuilder) String() string {
    if len(b.order) == 0 { return "" }
    var sb strings.Builder
    for i, k := range b.order {
        if i > 0 { sb.WriteByte(',') }
        sb.WriteString(k)
        sb.WriteByte('=')
        sb.WriteString(b.kv[k])
    }
    return sb.String()
}

// Basic key validation per W3C (simplified): key or key@tenant, lower-case a-z0-9 and _-*./
func validTSKey(k string) bool {
    if k == "" { return false }
    // vendor format: key or key@tenant
    parts := strings.Split(k, "@")
    if len(parts) > 2 { return false }
    for _, p := range parts {
        if p == "" { return false }
        for i := 0; i < len(p); i++ {
            c := p[i]
            if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '*' || c == '/' || c == '.' {
                continue
            }
            return false
        }
    }
    return true
}

// Basic value validation: disallow control chars and commas; trim spaces.
func validTSValue(v string) bool {
    if v == "" { return false }
    for i := 0; i < len(v); i++ {
        c := v[i]
        if c < 0x20 || c == 0x7f || c == ',' {
            return false
        }
    }
    return true
}

