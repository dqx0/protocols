package http1

import (
	"bufio"
	"fmt"
	"strings"
)

// WriteResponse writes a minimal HTTP/1.1 response.
// hdr keys should be canonicalized by caller.
func WriteResponse(bw *bufio.Writer, status int, reason string, hdr map[string][]string, body []byte, keepAlive bool) error {
	if reason == "" {
		reason = defaultReason(status)
	}
	if _, err := fmt.Fprintf(bw, "HTTP/1.1 %d %s\r\n", status, reason); err != nil {
		return err
	}
	for k, vv := range hdr {
		for _, v := range vv {
			if _, err := fmt.Fprintf(bw, "%s: %s\r\n", k, sanitizeHeaderValue(v)); err != nil {
				return err
			}
		}
	}
	if keepAlive {
		if _, err := fmt.Fprint(bw, "Connection: keep-alive\r\n"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprint(bw, "Connection: close\r\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(bw, "\r\n"); err != nil {
		return err
	}
	if len(body) > 0 {
		if _, err := bw.Write(body); err != nil {
			return err
		}
	}
	return nil
}

func defaultReason(code int) string {
	switch code {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 204:
		return "No Content"
	case 301:
		return "Moved Permanently"
	case 302:
		return "Found"
	case 304:
		return "Not Modified"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 500:
		return "Internal Server Error"
	case 501:
		return "Not Implemented"
	default:
		return ""
	}
}

// StartResponse writes the status line and headers, including
// Connection and optional Transfer-Encoding: chunked. It does not
// write any body bytes.
func StartResponse(bw *bufio.Writer, status int, reason string, hdr map[string][]string, chunked, keepAlive bool) error {
	if reason == "" {
		reason = defaultReason(status)
	}
	if _, err := fmt.Fprintf(bw, "HTTP/1.1 %d %s\r\n", status, reason); err != nil {
		return err
	}
	// If chunked, ensure Transfer-Encoding header and omit any Content-Length.
	if chunked {
		delete(hdr, "Content-Length")
		// Add TE: chunked
		if _, err := fmt.Fprint(bw, "Transfer-Encoding: chunked\r\n"); err != nil {
			return err
		}
	}
	for k, vv := range hdr {
		// Skip any user-supplied Connection header; we will set it based on keepAlive.
		if k == "Connection" {
			continue
		}
		for _, v := range vv {
			if _, err := fmt.Fprintf(bw, "%s: %s\r\n", k, sanitizeHeaderValue(v)); err != nil {
				return err
			}
		}
	}
	if keepAlive {
		if _, err := fmt.Fprint(bw, "Connection: keep-alive\r\n"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprint(bw, "Connection: close\r\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(bw, "\r\n"); err != nil {
		return err
	}
	return nil
}

// WriteChunked writes one HTTP/1.1 chunk for chunked transfer encoding.
func WriteChunked(bw *bufio.Writer, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := fmt.Fprintf(bw, "%x\r\n", len(p)); err != nil {
		return 0, err
	}
	if _, err := bw.Write(p); err != nil {
		return 0, err
	}
	if _, err := fmt.Fprint(bw, "\r\n"); err != nil {
		return 0, err
	}
	return len(p), nil
}

// EndChunked writes the terminating zero-length chunk.
func EndChunked(bw *bufio.Writer) error {
	if _, err := fmt.Fprint(bw, "0\r\n\r\n"); err != nil {
		return err
	}
	return nil
}

func sanitizeHeaderValue(v string) string {
	if v == "" {
		return v
	}
	// Remove CR/LF and other control chars except HTAB
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\r' || c == '\n' || c == 0x7f {
			continue
		}
		if c < 0x20 && c != '\t' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
