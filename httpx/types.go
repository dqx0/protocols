package httpx

import (
	"net/textproto"
)

// Header represents HTTP header fields.
//
// It maps a canonicalized header name to a list of values.
// The helper methods perform canonicalization compatible with
// the Go standard library.
type Header map[string][]string

// Get returns the first value associated with key, or "".
func (h Header) Get(key string) string {
	if h == nil {
		return ""
	}
	k := textproto.CanonicalMIMEHeaderKey(key)
	if vv, ok := h[k]; ok && len(vv) > 0 {
		return vv[0]
	}
	return ""
}

// Set sets the header entries associated with key to a single value.
// It replaces any existing values associated with key.
func (h Header) Set(key, value string) {
	if h == nil {
		return
	}
	k := textproto.CanonicalMIMEHeaderKey(key)
	h[k] = []string{value}
}

// Add adds the key, value pair to the header.
func (h Header) Add(key, value string) {
	if h == nil {
		return
	}
	k := textproto.CanonicalMIMEHeaderKey(key)
	h[k] = append(h[k], value)
}

// Del deletes the values associated with key.
func (h Header) Del(key string) {
	if h == nil {
		return
	}
	k := textproto.CanonicalMIMEHeaderKey(key)
	delete(h, k)
}
