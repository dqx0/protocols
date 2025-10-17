package httpx

import (
    "net/textproto"
)

type Header map[string][]string

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

func (h Header) Set(key, value string) {
    if h == nil {
        return
    }
    k := textproto.CanonicalMIMEHeaderKey(key)
    h[k] = []string{value}
}

func (h Header) Add(key, value string) {
    if h == nil {
        return
    }
    k := textproto.CanonicalMIMEHeaderKey(key)
    h[k] = append(h[k], value)
}

func (h Header) Del(key string) {
    if h == nil {
        return
    }
    k := textproto.CanonicalMIMEHeaderKey(key)
    delete(h, k)
}

