package http1

import (
    "bufio"
    "fmt"
    "strings"
)

// WriteContinue writes an interim 100 Continue response.
func WriteContinue(bw *bufio.Writer) error {
    _, err := fmt.Fprint(bw, "HTTP/1.1 100 Continue\r\n\r\n")
    return err
}

// SanitizeHeaderKey ensures header name is a valid token; returns empty string if invalid.
func SanitizeHeaderKey(k string) string {
    if k == "" { return "" }
    for i := 0; i < len(k); i++ {
        c := k[i]
        if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
            continue
        }
        switch c {
        case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
            continue
        default:
            return ""
        }
    }
    return k
}

// SanitizeHeaderValue removes CR/LF and control chars except HTAB.
func SanitizeHeaderValue(v string) string {
    if v == "" { return v }
    var b strings.Builder
    b.Grow(len(v))
    for i := 0; i < len(v); i++ {
        c := v[i]
        if c == '\r' || c == '\n' || c == 0x7f { continue }
        if c < 0x20 && c != '\t' { continue }
        b.WriteByte(c)
    }
    return b.String()
}
