package httpx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"dqx0.com/go/protcols/internal/obs"
)

// BasicTransport is a minimal HTTP/1.1 Transport with a per-host
// connection pool, optional proxy and TLS support. It aims to be
// small and predictable for libraries that need explicit control.
type BasicTransport struct {
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleConnTimeout time.Duration
	MaxConnsPerHost int
	TLSConfig       *tls.Config
	// Proxy returns the proxy URL to use for a given request.
	// Only http proxies are supported in MVP.
	Proxy func(*Request) (*url.URL, error)

	Logger obs.Logger
	Meter  obs.Meter

	mu    sync.Mutex
	idle  map[string][]*pooledConn // key: host:port
	conns map[string]int           // total conns per key (idle + in-use)
	once  sync.Once
	stop  chan struct{}
}

type pooledConn struct {
	c       net.Conn
	br      *bufio.Reader
	bw      *bufio.Writer
	lastUse time.Time
}

// DefaultTransport is used by Client when Transport is nil.
var DefaultTransport = &BasicTransport{
	DialTimeout:     5 * time.Second,
	ReadTimeout:     0,
	WriteTimeout:    0,
	IdleConnTimeout: 30 * time.Second,
	MaxConnsPerHost: 8,
	idle:            make(map[string][]*pooledConn),
	conns:           make(map[string]int),
}

// NewBasicTransport returns a new BasicTransport with sane defaults and
// isolated connection pools suitable for use per Client instance in libraries.
// NewBasicTransport returns a new BasicTransport with defaults.
func NewBasicTransport() *BasicTransport {
	return &BasicTransport{
		DialTimeout:     5 * time.Second,
		IdleConnTimeout: 30 * time.Second,
		MaxConnsPerHost: 8,
		idle:            make(map[string][]*pooledConn),
		conns:           make(map[string]int),
	}
}

func (t *BasicTransport) RoundTrip(r *Request) (*Response, error) {
	t.once.Do(func() { t.startCleanup() })
	rtStart := time.Now()
	if r == nil || r.URL == nil {
		return nil, errors.New("httpx: nil request or URL")
	}
	scheme := r.URL.Scheme
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("httpx: unsupported scheme %q", scheme)
	}
	// Determine proxy
	var proxyURL *url.URL
	if t.Proxy != nil {
		if u, err := t.Proxy(r); err == nil {
			proxyURL = u
		}
	} else {
		if u, err := ProxyFromEnvironment(r); err == nil {
			proxyURL = u
		}
	}
	addr := hostPort(r.URL)
	var key string
	var dialFn func(context.Context) (net.Conn, error)
	var useProxyHTTP bool
	if proxyURL != nil && proxyURL.Scheme == "http" {
		// HTTP proxy path
		proxyAddr := hostPort(proxyURL)
		if scheme == "http" {
			// Plain HTTP via proxy: reuse proxy connection across targets
			key = "proxy-http://" + proxyAddr
			useProxyHTTP = true
			dialFn = func(ctx context.Context) (net.Conn, error) {
				d := net.Dialer{Timeout: t.DialTimeout}
				return d.DialContext(ctx, "tcp", proxyAddr)
			}
		} else { // https via CONNECT
			// Dedicated tunnel per target
			key = "proxy-tunnel://" + proxyAddr + "->" + addr
			dialFn = func(ctx context.Context) (net.Conn, error) {
				d := net.Dialer{Timeout: t.DialTimeout}
				c, err := d.DialContext(ctx, "tcp", proxyAddr)
				if err != nil {
					return nil, err
				}
				br := bufio.NewReader(c)
				bw := bufio.NewWriter(c)
				// CONNECT handshake
				if _, err := fmt.Fprintf(bw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr); err != nil {
					_ = c.Close()
					return nil, err
				}
				if h := proxyAuthHeader(proxyURL); h != "" {
					if _, err := fmt.Fprintf(bw, "Proxy-Authorization: %s\r\n", h); err != nil {
						_ = c.Close()
						return nil, err
					}
				}
				if _, err := fmt.Fprint(bw, "Connection: keep-alive\r\n\r\n"); err != nil {
					_ = c.Close()
					return nil, err
				}
				if err := bw.Flush(); err != nil {
					_ = c.Close()
					return nil, err
				}
				// Read response line and headers
				_, code, _, err := readStatusLine(br)
				if err != nil {
					_ = c.Close()
					return nil, err
				}
				if _, err := readHeaders(br); err != nil {
					_ = c.Close()
					return nil, err
				}
				if code != 200 {
					_ = c.Close()
					return nil, fmt.Errorf("httpx: proxy CONNECT failed: %d", code)
				}
				// TLS wrap
				cfg := t.TLSConfig
				if cfg == nil {
					cfg = &tls.Config{}
				}
				if cfg.ServerName == "" {
					cfg = cfg.Clone()
					cfg.ServerName = hostNoPort(r.URL.Host)
				}
				if len(cfg.NextProtos) == 0 {
					cfg = cfg.Clone()
					cfg.NextProtos = []string{"http/1.1"}
				}
				td := tls.Dialer{NetDialer: &d, Config: cfg}
				// We already have c connected; wrap manually to honor ctx cancellations via deadlines.
				tc := tls.Client(c, cfg)
				// Apply deadline from ctx if present during handshake
				if dl, ok := ctx.Deadline(); ok {
					_ = tc.SetDeadline(dl)
				}
				if err := tc.Handshake(); err != nil {
					_ = c.Close()
					return nil, err
				}
				if !dlIsZero(ctx) {
					_ = tc.SetDeadline(time.Time{})
				}
				_ = td // keep var used for future; currently we used manual client
				return tc, nil
			}
		}
	} else {
		// Direct connection
		key = scheme + "://" + addr
		dialFn = func(ctx context.Context) (net.Conn, error) {
			d := net.Dialer{Timeout: t.DialTimeout}
			if scheme == "https" {
				cfg := t.TLSConfig
				if cfg == nil {
					cfg = &tls.Config{}
				}
				// Ensure SNI and ALPN
				if cfg.ServerName == "" {
					cfg = cfg.Clone()
					cfg.ServerName = hostNoPort(r.URL.Host)
				}
				if len(cfg.NextProtos) == 0 {
					cfg = cfg.Clone()
					cfg.NextProtos = []string{"http/1.1"}
				}
				td := tls.Dialer{NetDialer: &d, Config: cfg}
				return td.DialContext(ctx, "tcp", addr)
			}
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	// Dial using request context
	pc, err := t.getConn(key, func(ctx context.Context) (net.Conn, error) { return dialFn(r.Context()) })
	if err != nil {
		t.logf(obs.Error, "dial %s failed: %v", key, err)
		t.metricCounter("httpx_client_requests_error", 1, obs.Label{Key: "stage", Value: "dial"})
		return nil, err
	}

	// Deadlines
	setWriteDeadlineWithContext(pc.c, t.WriteTimeout, r.Context())

	// Write request
	path := r.RequestURI
	if path == "" {
		if r.URL.Opaque != "" {
			path = r.URL.Opaque
		} else {
			path = r.URL.RequestURI()
			if path == "" {
				path = "/"
			}
		}
	}
	// Request line
	if useProxyHTTP {
		abs := absoluteURL(r.URL)
		if _, err := fmt.Fprintf(pc.bw, "%s %s HTTP/1.1\r\n", r.Method, abs); err != nil {
			t.closeConn(key, pc)
			t.logf(obs.Warn, "write request line failed: %v", err)
			return nil, err
		}
	} else {
		if _, err := fmt.Fprintf(pc.bw, "%s %s HTTP/1.1\r\n", r.Method, path); err != nil {
			t.closeConn(key, pc)
			t.logf(obs.Warn, "write request line failed: %v", err)
			return nil, err
		}
	}

	// Headers
	hdr := r.Header
	if hdr == nil {
		hdr = Header{}
	}
	// Request IDs from context or generate if missing
	if hdr.Get("X-Request-ID") == "" {
		if id, ok := RequestIDFrom(r.Context()); ok {
			hdr.Set("X-Request-ID", id)
		} else {
			hdr.Set("X-Request-ID", genID())
		}
	}
	if hdr.Get("X-Correlation-ID") == "" {
		if cid, ok := CorrelationIDFrom(r.Context()); ok {
			hdr.Set("X-Correlation-ID", cid)
		} else if r.CorrelationID != "" {
			hdr.Set("X-Correlation-ID", r.CorrelationID)
		}
	}
	// Correlation: ensure request ID and optional correlation ID
	reqID := hdr.Get("X-Request-ID")
	if reqID == "" {
		reqID = genID()
		hdr.Set("X-Request-ID", reqID)
	}
	if hdr.Get("X-Correlation-ID") == "" && r.CorrelationID != "" {
		hdr.Set("X-Correlation-ID", r.CorrelationID)
	}
	// Host
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	if hdr.Get("Host") == "" && host != "" {
		fmt.Fprintf(pc.bw, "Host: %s\r\n", host)
	}
	// W3C Trace Context: ensure Traceparent header, prefer ctx trace
	if hdr.Get("Traceparent") == "" {
		tid := r.TraceID
		if tid == "" {
			if tr, ok := TraceFrom(r.Context()); ok && tr.TraceID != "" {
				tid = tr.TraceID
				r.ParentSpanID = tr.SpanID
			}
		}
		if tid == "" {
			tid = genTraceID()
			r.TraceID = tid
		}
		sid := genSpanID()
		r.SpanID = sid
		tp := formatTraceparent(tid, sid, "01")
		fmt.Fprintf(pc.bw, "Traceparent: %s\r\n", tp)
	}
	// Tracestate: propagate if present on request and not already set
	if hdr.Get("Tracestate") == "" && r.TraceState != "" {
		fmt.Fprintf(pc.bw, "Tracestate: %s\r\n", r.TraceState)
	}
	// Proxy-Authorization for HTTP proxy (non-CONNECT requests)
	if useProxyHTTP {
		if proxyURL != nil {
			if h := proxyAuthHeader(proxyURL); h != "" {
				fmt.Fprintf(pc.bw, "Proxy-Authorization: %s\r\n", h)
			}
		}
	}
	// Connection keep-alive by default
	if strings.EqualFold(hdr.Get("Connection"), "close") {
		fmt.Fprint(pc.bw, "Connection: close\r\n")
	} else {
		fmt.Fprint(pc.bw, "Connection: keep-alive\r\n")
	}
	// Body handling
	var bodyBytes []byte
	var hasBody bool
	if r.Body != nil {
		hasBody = true
		if r.ContentLength >= 0 {
			fmt.Fprintf(pc.bw, "Content-Length: %d\r\n", r.ContentLength)
		} else {
			// Buffer body to compute length (MVP). For large bodies, users should set Content-Length.
			var buf bytes.Buffer
			if _, err := io.Copy(&buf, r.Body); err != nil {
				t.closeConn(addr, pc)
				return nil, err
			}
			bodyBytes = buf.Bytes()
			fmt.Fprintf(pc.bw, "Content-Length: %d\r\n", len(bodyBytes))
		}
	}
	// User headers (excluding Host/Connection/Content-Length which we've written above if needed)
	for k, vv := range hdr {
		ck := k
		if strings.EqualFold(ck, "Host") || strings.EqualFold(ck, "Connection") || strings.EqualFold(ck, "Content-Length") {
			continue
		}
		for _, v := range vv {
			if _, err := fmt.Fprintf(pc.bw, "%s: %s\r\n", ck, v); err != nil {
				t.closeConn(key, pc)
				t.logf(obs.Warn, "write header failed: %v", err)
				return nil, err
			}
		}
	}
	if _, err := fmt.Fprint(pc.bw, "\r\n"); err != nil {
		t.closeConn(key, pc)
		t.logf(obs.Warn, "write CRLF failed: %v", err)
		return nil, err
	}
	if hasBody {
		if bodyBytes != nil {
			if _, err := pc.bw.Write(bodyBytes); err != nil {
				t.closeConn(key, pc)
				t.logf(obs.Warn, "write body failed: %v", err)
				return nil, err
			}
		} else if r.ContentLength > 0 {
			if _, err := io.CopyN(pc.bw, r.Body, r.ContentLength); err != nil {
				t.closeConn(key, pc)
				t.logf(obs.Warn, "copy body failed: %v", err)
				return nil, err
			}
		}
	}
	if err := pc.bw.Flush(); err != nil {
		t.closeConn(key, pc)
		t.logf(obs.Warn, "flush request failed: %v", err)
		t.metricCounter("httpx_client_requests_error", 1, obs.Label{Key: "stage", Value: "write"})
		return nil, err
	}
	t.metricCounter("httpx_client_requests_total", 1, obs.Label{Key: "method", Value: r.Method})

	// Read response
	setReadDeadlineWithContext(pc.c, t.ReadTimeout, r.Context())
	proto, statusCode, status, err := readStatusLine(pc.br)
	if err != nil {
		t.closeConn(key, pc)
		t.logf(obs.Warn, "read status line failed: %v", err)
		t.metricCounter("httpx_client_requests_error", 1, obs.Label{Key: "stage", Value: "read_status"})
		return nil, err
	}
	rh, err := readHeaders(pc.br)
	if err != nil {
		t.closeConn(key, pc)
		t.logf(obs.Warn, "read headers failed: %v", err)
		t.metricCounter("httpx_client_requests_error", 1, obs.Label{Key: "stage", Value: "read_headers"})
		return nil, err
	}

	// Decide body framing
	method := r.Method
	var body io.ReadCloser
	var cl int64 = -1
	var reuse bool
	if noResponseBody(statusCode, method) {
		body = io.NopCloser(strings.NewReader(""))
		reuse = true
		cl = 0
	} else if hasChunkedTEMap(rh) {
		body = newClientChunkedBody(pc.br)
		reuse = true // can be reused if body is fully read/closed
	} else if v := getHeaderMap(rh, "Content-Length"); v != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n < 0 {
			t.closeConn(key, pc)
			t.logf(obs.Warn, "bad Content-Length: %q", v)
			return nil, ErrBadRequest
		}
		cl = n
		if cl == 0 {
			body = io.NopCloser(strings.NewReader(""))
			reuse = true
		} else {
			lr := &io.LimitedReader{R: pc.br, N: cl}
			body = &clientLimitedBody{lr: lr}
			reuse = true
		}
	} else {
		// Close-delimited body: read to EOF; do not reuse connection.
		body = io.NopCloser(pc.br)
		reuse = false
	}

	resp := &Response{
		Status:        fmt.Sprintf("%d %s", statusCode, status),
		StatusCode:    statusCode,
		Proto:         proto,
		Header:        Header(rh),
		Body:          nil, // set below
		ContentLength: cl,
	}
	t.metricCounter("httpx_client_responses_total", 1, obs.Label{Key: "status", Value: itoaStatus(statusCode)})
	t.metricHistogram("httpx_client_roundtrip_duration_ms", float64(time.Since(rtStart).Milliseconds()),
		obs.Label{Key: "method", Value: r.Method}, obs.Label{Key: "status", Value: itoaStatus(statusCode)})

	// Wrap body to manage connection reuse on Close.
	resp.Body = &clientBody{ReadCloser: body, t: t, addr: key, pc: pc, reusable: reuse}
	return resp, nil
}

func (t *BasicTransport) getConn(key string, dial func(context.Context) (net.Conn, error)) (*pooledConn, error) {
	t.mu.Lock()
	if list := t.idle[key]; len(list) > 0 {
		pc := list[len(list)-1]
		t.idle[key] = list[:len(list)-1]
		t.mu.Unlock()
		t.metricCounter("httpx_client_conn_reuse_total", 1)
		return pc, nil
	}
	// enforce max connections per key
	if t.MaxConnsPerHost > 0 && t.conns[key] >= t.MaxConnsPerHost {
		t.mu.Unlock()
		return nil, fmt.Errorf("httpx: max connections per host reached for %s", key)
	}
	t.mu.Unlock()
	c, err := dial(context.Background())
	if err != nil {
		return nil, err
	}
	pc := &pooledConn{c: c, br: bufio.NewReader(c), bw: bufio.NewWriter(c), lastUse: time.Now()}
	t.mu.Lock()
	t.conns[key]++
	t.mu.Unlock()
	t.metricCounter("httpx_client_conn_dial_total", 1)
	return pc, nil
}

func (t *BasicTransport) putConn(key string, pc *pooledConn) {
	if pc == nil || pc.c == nil {
		return
	}
	if t.IdleConnTimeout > 0 {
		_ = pc.c.SetReadDeadline(time.Now().Add(t.IdleConnTimeout))
	} else {
		_ = pc.c.SetReadDeadline(time.Time{})
	}
	pc.lastUse = time.Now()
	t.mu.Lock()
	t.idle[key] = append(t.idle[key], pc)
	t.mu.Unlock()
}

func (t *BasicTransport) closeConn(key string, pc *pooledConn) {
	if pc != nil && pc.c != nil {
		_ = pc.c.Close()
	}
	t.mu.Lock()
	if t.conns[key] > 0 {
		t.conns[key]--
	}
	t.mu.Unlock()
}

// clientBody ensures the connection is properly reused or closed on Close.
type clientBody struct {
	io.ReadCloser
	t        *BasicTransport
	addr     string
	pc       *pooledConn
	reusable bool
	closed   bool
}

func (b *clientBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.ReadCloser != nil {
		// Drain to EOF so that the connection can be reused where possible.
		_, _ = io.Copy(io.Discard, b.ReadCloser)
		_ = b.ReadCloser.Close()
	}
	if b.reusable && b.pc != nil {
		b.t.putConn(b.addr, b.pc)
	} else {
		b.t.closeConn(b.addr, b.pc)
	}
	return nil
}

// Response parsing helpers
func readStatusLine(br *bufio.Reader) (proto string, code int, reason string, err error) {
	line, err := readLine(br, 8<<10)
	if err != nil {
		return "", 0, "", err
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return "", 0, "", ErrBadRequest
	}
	proto = parts[0]
	if !strings.HasPrefix(proto, "HTTP/1.") {
		return "", 0, "", ErrProtocolViolation
	}
	code, err = strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, "", ErrBadRequest
	}
	if len(parts) == 3 {
		reason = parts[2]
	}
	return
}

func readHeaders(br *bufio.Reader) (map[string][]string, error) {
	h := make(map[string][]string)
	for {
		line, err := readLine(br, 8<<10)
		if err != nil {
			return nil, err
		}
		if line == "" {
			break
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			return nil, ErrBadRequest
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		addHeaderMap(h, k, v)
	}
	return h, nil
}

func readLine(br *bufio.Reader, limit int) (string, error) {
	var sb strings.Builder
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			break
		}
		if b != '\r' {
			sb.WriteByte(b)
		}
		if limit > 0 && sb.Len() > limit {
			return "", ErrHeaderTooLarge
		}
	}
	return sb.String(), nil
}

func addHeaderMap(h map[string][]string, k, v string) {
	hk := canonicalKey(k)
	h[hk] = append(h[hk], v)
}

func getHeaderMap(h map[string][]string, k string) string {
	hk := canonicalKey(k)
	if vv, ok := h[hk]; ok && len(vv) > 0 {
		return vv[0]
	}
	return ""
}

func hasChunkedTEMap(h map[string][]string) bool {
	hk := canonicalKey("Transfer-Encoding")
	if vv, ok := h[hk]; ok {
		for _, v := range vv {
			if strings.Contains(strings.ToLower(v), "chunked") {
				return true
			}
		}
	}
	return false
}

func canonicalKey(s string) string {
	b := []byte(strings.ToLower(s))
	upper := true
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			if upper {
				b[i] = byte(c - 'a' + 'A')
			}
			upper = false
		} else {
			upper = c == '-'
		}
	}
	return string(b)
}

func hostPort(u *url.URL) string {
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	return host
}

func hostNoPort(h string) string {
	if i := strings.LastIndex(h, ":"); i != -1 {
		// If it's an IPv6 literal, don't strip the colon inside [].
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			return strings.Trim(h, "[]")
		}
		return h[:i]
	}
	return h
}

func absoluteURL(u *url.URL) string {
	// Build a full absolute URL without userinfo
	var b strings.Builder
	b.WriteString(u.Scheme)
	b.WriteString("://")
	b.WriteString(u.Host)
	if u.Opaque != "" {
		b.WriteString(u.Opaque)
	} else if u.RawPath != "" {
		b.WriteString(u.RawPath)
	} else if u.Path != "" {
		if !strings.HasPrefix(u.Path, "/") {
			b.WriteString("/")
		}
		b.WriteString(u.Path)
	} else {
		b.WriteString("/")
	}
	if u.RawQuery != "" {
		b.WriteString("?")
		b.WriteString(u.RawQuery)
	}
	if u.Fragment != "" {
		b.WriteString("#")
		b.WriteString(u.Fragment)
	}
	return b.String()
}

func proxyAuthHeader(u *url.URL) string {
	if u == nil || u.User == nil {
		return ""
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	token := user + ":" + pass
	// base64 encode
	enc := encodeBase64([]byte(token))
	return "Basic " + enc
}

// Minimal base64 encoding (URL-safe not needed) to avoid importing encoding/base64 across the file
func encodeBase64(b []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out []byte
	n := len(b)
	for i := 0; i < n; i += 3 {
		var c0, c1, c2 byte
		c0 = b[i]
		var c1ok, c2ok bool
		if i+1 < n {
			c1 = b[i+1]
			c1ok = true
		}
		if i+2 < n {
			c2 = b[i+2]
			c2ok = true
		}
		out = append(out, table[c0>>2])
		out = append(out, table[((c0&0x03)<<4)|((c1&0xF0)>>4)])
		if c1ok {
			out = append(out, table[((c1&0x0F)<<2)|((c2&0xC0)>>6)])
		} else {
			out = append(out, '=')
		}
		if c2ok {
			out = append(out, table[c2&0x3F])
		} else {
			out = append(out, '=')
		}
	}
	return string(out)
}

// Helpers to apply deadlines from both explicit timeouts and request context
func setWriteDeadlineWithContext(c net.Conn, writeTO time.Duration, ctx context.Context) {
	var d time.Time
	if writeTO > 0 {
		d = time.Now().Add(writeTO)
	}
	if dl, ok := ctx.Deadline(); ok {
		if d.IsZero() || dl.Before(d) {
			d = dl
		}
	}
	if !d.IsZero() {
		_ = c.SetWriteDeadline(d)
	}
}

func setReadDeadlineWithContext(c net.Conn, readTO time.Duration, ctx context.Context) {
	var d time.Time
	if readTO > 0 {
		d = time.Now().Add(readTO)
	}
	if dl, ok := ctx.Deadline(); ok {
		if d.IsZero() || dl.Before(d) {
			d = dl
		}
	}
	if !d.IsZero() {
		_ = c.SetReadDeadline(d)
	}
}

func dlIsZero(ctx context.Context) bool {
	_, ok := ctx.Deadline()
	return !ok
}

func (t *BasicTransport) logf(level obs.Level, format string, args ...interface{}) {
	lg := t.Logger
	if lg == nil {
		lg = obs.NopLogger{}
	}
	lg.Logf(level, format, args...)
}

func (t *BasicTransport) metricCounter(name string, value float64, labels ...obs.Label) {
	m := t.getMeter()
	m.Counter(name, value, labels...)
}

func (t *BasicTransport) metricHistogram(name string, value float64, labels ...obs.Label) {
	m := t.getMeter()
	m.Histogram(name, value, labels...)
}

func (t *BasicTransport) getMeter() obs.Meter {
	if t.Meter != nil {
		return t.Meter
	}
	return obs.NopMeter{}
}

// startCleanup launches a goroutine to close expired idle connections.
func (t *BasicTransport) startCleanup() {
	if t.stop == nil {
		t.stop = make(chan struct{})
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.pruneIdle()
			case <-t.stop:
				return
			}
		}
	}()
}

func (t *BasicTransport) pruneIdle() {
	if t.IdleConnTimeout <= 0 {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, list := range t.idle {
		kept := list[:0]
		for _, pc := range list {
			if now.Sub(pc.lastUse) > t.IdleConnTimeout {
				_ = pc.c.Close()
				if t.conns[key] > 0 {
					t.conns[key]--
				}
				t.metricCounter("httpx_client_conn_idle_closed_total", 1)
				continue
			}
			kept = append(kept, pc)
		}
		if len(kept) == 0 {
			delete(t.idle, key)
		} else {
			t.idle[key] = kept
		}
	}
}

// CloseIdleConnections closes all idle pooled connections immediately.
func (t *BasicTransport) CloseIdleConnections() {
	t.mu.Lock()
	for key, list := range t.idle {
		for _, pc := range list {
			_ = pc.c.Close()
			if t.conns[key] > 0 {
				t.conns[key]--
			}
		}
		delete(t.idle, key)
	}
	t.mu.Unlock()
}

// Close stops background cleanup and closes idle connections. Active
// connections are not forcibly closed.
func (t *BasicTransport) Close() {
    if t.stop != nil {
        close(t.stop)
        t.stop = nil
    }
    t.CloseIdleConnections()
}

// ProxyFromEnvironment resolves a proxy URL from environment variables
// HTTP_PROXY/HTTPS_PROXY/ALL_PROXY and honors NO_PROXY. Behaves similarly
// to net/http.ProxyFromEnvironment for common cases.
func ProxyFromEnvironment(r *Request) (*url.URL, error) {
	if r == nil || r.URL == nil {
		return nil, nil
	}
	full := r.URL.Host
	host := hostOnly(full)
	port := ""
	if i := strings.LastIndex(full, ":"); i != -1 && !(strings.HasPrefix(full, "[") && strings.HasSuffix(full, "]")) {
		port = full[i+1:]
	} else if r.URL.Scheme == "https" {
		port = "443"
	} else {
		port = "80"
	}
	// NO_PROXY disables proxy for matching hosts
	if noProxyMatch(host, port) {
		return nil, nil
	}
	scheme := r.URL.Scheme
	if scheme == "" {
		scheme = "http"
	}
	var proxyStr string
	if scheme == "https" {
		proxyStr = firstEnv("HTTPS_PROXY", "https_proxy")
	} else {
		proxyStr = firstEnv("HTTP_PROXY", "http_proxy")
	}
	if proxyStr == "" {
		proxyStr = firstEnv("ALL_PROXY", "all_proxy")
	}
	if proxyStr == "" {
		return nil, nil
	}
	return url.Parse(proxyStr)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func noProxyMatch(host, port string) bool {
	v := firstEnv("NO_PROXY", "no_proxy")
	if v == "" {
		return false
	}
	host = strings.ToLower(host)
	parts := strings.Split(v, ",")
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		// Scheme prefix: ignore if provided
		if i := strings.Index(p, "://"); i >= 0 {
			p = p[i+3:]
		}
		// CIDR match
		if strings.Contains(p, "/") {
			// only if host is an IP
			ip := net.ParseIP(host)
			if ip == nil {
				// try bracket IPv6 form
				if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
					ip = net.ParseIP(strings.Trim(host, "[]"))
				}
			}
			if ip != nil {
				if _, cidr, err := net.ParseCIDR(p); err == nil && cidr.Contains(ip) {
					return true
				}
			}
			continue
		}
		// Port specific pattern
		patPort := ""
		if i := strings.LastIndex(p, ":"); i != -1 && !strings.HasPrefix(p, "[") {
			patPort = p[i+1:]
			p = p[:i]
		}
		if patPort != "" && port != patPort {
			continue
		}
		// IPv6 bracket form
		if strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]") {
			p = strings.Trim(p, "[]")
			if hostOnly("["+host+"]") == p {
				return true
			}
			if h := hostOnly(host); h == p {
				return true
			}
			continue
		}
		// Exact host
		if host == p {
			return true
		}
		// Domain suffix match
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(host, p) {
				return true
			}
		} else {
			if strings.HasSuffix(host, "."+p) {
				return true
			}
		}
	}
	return false
}

// clientLimitedBody drains remaining bytes on Close for reuse.
type clientLimitedBody struct{ lr *io.LimitedReader }

func (b *clientLimitedBody) Read(p []byte) (int, error) { return b.lr.Read(p) }
func (b *clientLimitedBody) Close() error {
	buf := make([]byte, 1024)
	for b.lr.N > 0 {
		n := int64(len(buf))
		if n > b.lr.N {
			n = b.lr.N
		}
		if n <= 0 {
			break
		}
		if _, err := io.ReadFull(b.lr, buf[:n]); err != nil {
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				break
			}
			return err
		}
	}
	return nil
}

// Minimal chunked reader for client
type clientChunkedBody struct {
	br       *bufio.Reader
	remain   int64
	finished bool
}

func newClientChunkedBody(br *bufio.Reader) io.ReadCloser {
	return &clientChunkedBody{br: br, remain: -1}
}

func (c *clientChunkedBody) Read(p []byte) (int, error) {
	if c.finished {
		return 0, io.EOF
	}
	if c.remain <= 0 {
		// read new chunk size
		line, err := readLine(c.br, 8<<10)
		if err != nil {
			return 0, err
		}
		if i := strings.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return 0, ErrBadRequest
		}
		n, err := strconv.ParseInt(line, 16, 64)
		if err != nil || n < 0 {
			return 0, ErrBadRequest
		}
		if n == 0 {
			// consume trailers
			for {
				l, err := readLine(c.br, 8<<10)
				if err != nil {
					return 0, err
				}
				if l == "" {
					break
				}
			}
			c.finished = true
			return 0, io.EOF
		}
		c.remain = n
	}
	toRead := int64(len(p))
	if toRead > c.remain {
		toRead = c.remain
	}
	n, err := io.ReadFull(c.br, p[:toRead])
	c.remain -= int64(n)
	if err != nil {
		return n, err
	}
	if c.remain == 0 {
		// expect CRLF
		b1, err := c.br.ReadByte()
		if err != nil {
			return n, err
		}
		b2, err := c.br.ReadByte()
		if err != nil {
			return n, err
		}
		if b1 != '\r' || b2 != '\n' {
			return n, ErrBadRequest
		}
	}
	return n, nil
}

func (c *clientChunkedBody) Close() error {
	// drain
	buf := make([]byte, 1024)
	for !c.finished {
		_, err := c.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}
