package httpx

import (
    "bufio"
    "bytes"
    "crypto/tls"
    "compress/gzip"
    "fmt"
    "context"
    "runtime/debug"
    "net"
    "net/url"
    "strconv"
    "strings"
    "time"

    "dqx0.com/go/protcols/httpx/internal/http1"
    "dqx0.com/go/protcols/internal/obs"
    "io"
    "sync"
)

// Handler responds to an HTTP request.
type Handler interface {
    // ServeHTTP writes a response for r to w.
    ServeHTTP(ResponseWriter, *Request)
}

// HandlerFunc is an adapter to allow the use of ordinary
// functions as HTTP handlers.
type HandlerFunc func(ResponseWriter, *Request)

// ServeHTTP calls f(w, r).
func (f HandlerFunc) ServeHTTP(w ResponseWriter, r *Request) {
    f(w, r)
}

// ResponseWriter writes an HTTP response.
//
// WriteHeader sends an HTTP status code; Write sends response body bytes.
// Header returns the response headers to be sent.
type ResponseWriter interface {
    Header() Header
    Write([]byte) (int, error)
    WriteHeader(status int)
}

// Server is an HTTP/1.1 server.
//
// Set Handler to handle requests. Use ListenAndServe or Serve to run.
type Server struct {
    Addr               string
    Handler            Handler
    ReadTimeout        time.Duration
    ReadHeaderTimeout  time.Duration
    WriteTimeout       time.Duration
    IdleTimeout        time.Duration
    MaxHeaderBytes     int
    MaxTotalHeaderBytes int
    MaxBodyBytes       int64
    TLSConfig          *tls.Config
    Logger             obs.Logger
    Meter              obs.Meter
    // ExpectContinueTimeout bounds the wait before a request body is
    // sent after a 100-continue expectation. If zero, no explicit wait.
    ExpectContinueTimeout time.Duration
    // EnableGzip enables gzip compression for responses when the
    // client advertises Accept-Encoding: gzip.
    EnableGzip         bool
    // DecompressRequest transparently decompresses gzip-encoded request bodies.
    DecompressRequest  bool

    mu        sync.Mutex
    closing   bool
    listener  net.Listener
    conns     map[net.Conn]struct{}
    wg        sync.WaitGroup
}

// ListenAndServe listens on Server.Addr and serves HTTP/1.1 requests.
func (s *Server) ListenAndServe() error {
    addr := s.Addr
    if addr == "" {
        addr = ":8080"
    }
    ln, err := net.Listen("tcp", addr)
    if err != nil {
        s.logf(obs.Error, "listen on %s failed: %v", addr, err)
        return err
    }
    return s.Serve(ln)
}

// ListenAndServeTLS starts a TLS listener and serves HTTP/1.1 over TLS.
// If certFile/keyFile are empty, it uses s.TLSConfig.Certificates.
// ListenAndServeTLS listens on Server.Addr and serves HTTPS (HTTP/1.1 over TLS).
// If certFile and keyFile are empty, Server.TLSConfig.Certificates are used.
func (s *Server) ListenAndServeTLS(certFile, keyFile string) error {
    addr := s.Addr
    if addr == "" {
        addr = ":8443"
    }
    ln, err := net.Listen("tcp", addr)
    if err != nil { return err }
    var cfg *tls.Config
    if s.TLSConfig != nil {
        c := s.TLSConfig.Clone()
        if len(c.NextProtos) == 0 {
            c.NextProtos = []string{"http/1.1"}
        }
        cfg = c
    } else {
        cfg = &tls.Config{NextProtos: []string{"http/1.1"}}
    }
    if certFile != "" || keyFile != "" {
        cert, err := tls.LoadX509KeyPair(certFile, keyFile)
        if err != nil { _ = ln.Close(); return err }
        cfg.Certificates = []tls.Certificate{cert}
    }
    return s.ServeTLS(ln, cfg)
}

// ServeTLS serves HTTP over a TLS listener using the provided config.
// ServeTLS serves HTTPS on the given TLS listener.
func (s *Server) ServeTLS(l net.Listener, cfg *tls.Config) error {
    tlsLn := tls.NewListener(l, cfg)
    return s.Serve(tlsLn)
}

// Serve accepts incoming connections on l and serves requests.
func (s *Server) Serve(l net.Listener) error {
    s.mu.Lock()
    s.listener = l
    if s.conns == nil { s.conns = make(map[net.Conn]struct{}) }
    s.mu.Unlock()
    defer l.Close()
    for {
        c, err := l.Accept()
        if err != nil {
            s.mu.Lock()
            closing := s.closing
            s.mu.Unlock()
            if closing {
                return nil
            }
            s.logf(obs.Error, "accept failed: %v", err)
            return err
        }
        s.metricCounter("httpx_server_connections_accepted", 1)
        s.wg.Add(1)
        s.mu.Lock()
        s.conns[c] = struct{}{}
        s.mu.Unlock()
        go s.serveConn(c)
    }
}

type responseBuffer struct {
    h       Header
    status  int
    wroteH  bool
    bodyBuf bytes.Buffer
}

func (w *responseBuffer) Header() Header {
    if w.h == nil {
        w.h = Header{}
    }
    return w.h
}

func (w *responseBuffer) WriteHeader(status int) {
    if w.wroteH {
        return
    }
    if status == 0 {
        status = 200
    }
    w.status = status
    w.wroteH = true
}

func (w *responseBuffer) Write(p []byte) (int, error) {
    if !w.wroteH {
        w.WriteHeader(200)
    }
    return w.bodyBuf.Write(p)
}

// connResponseWriter streams the response to the client. If keepAlive is true
// and Content-Length is not set for HTTP/1.1, it enables chunked encoding.
type connResponseWriter struct {
    bw         *bufio.Writer
    proto      string
    keepAlive  bool
    status     int
    wroteHdr   bool
    chunked    bool
    hdr        Header
}

func (w *connResponseWriter) Header() Header {
    if w.hdr == nil {
        w.hdr = Header{}
    }
    return w.hdr
}

func (w *connResponseWriter) decideChunked() bool {
    if strings.EqualFold(w.hdr.Get("Connection"), "close") {
        w.keepAlive = false
    }
    hasCL := w.hdr.Get("Content-Length") != ""
    if w.proto == "HTTP/1.1" && w.keepAlive && !hasCL {
        return true
    }
    return false
}

func (w *connResponseWriter) startIfNeeded() error {
    if w.wroteHdr {
        return nil
    }
    if w.status == 0 {
        w.status = 200
    }
    // Decide chunked based on headers and keepAlive.
    w.chunked = w.decideChunked()
    // Remove any user Connection header to avoid duplicates.
    if w.hdr != nil {
        w.hdr.Del("Connection")
    }
    // Start headers
    hdrMap := map[string][]string(w.hdr)
    if err := http1.StartResponse(w.bw, w.status, "", hdrMap, w.chunked, w.keepAlive && (w.chunked || w.hdr.Get("Content-Length") != "")); err != nil {
        return err
    }
    w.wroteHdr = true
    return nil
}

func (w *connResponseWriter) WriteHeader(status int) {
    if w.wroteHdr {
        return
    }
    if status == 0 {
        status = 200
    }
    w.status = status
    _ = w.startIfNeeded() // best-effort; error will surface on Flush
}

func (w *connResponseWriter) Write(p []byte) (int, error) {
    if !w.wroteHdr {
        if err := w.startIfNeeded(); err != nil {
            return 0, err
        }
    }
    if w.chunked {
        n, err := http1.WriteChunked(w.bw, p)
        if err != nil {
            return n, err
        }
        // Flush each chunk to enable streaming to clients.
        if err := w.bw.Flush(); err != nil {
            return n, err
        }
        return n, nil
    }
    return w.bw.Write(p)
}

func (w *connResponseWriter) Flush() error {
    if !w.wroteHdr {
        if err := w.startIfNeeded(); err != nil {
            return err
        }
    }
    return w.bw.Flush()
}

func (s *Server) serveConn(c net.Conn) {
    defer func() {
        _ = c.Close()
        s.mu.Lock()
        delete(s.conns, c)
        s.mu.Unlock()
        s.wg.Done()
        s.metricCounter("httpx_server_connections_closed", 1)
    }()
    br := bufio.NewReader(c)
    bw := bufio.NewWriter(c)
    var alive = true
    for alive {
        if s.ReadHeaderTimeout > 0 {
            _ = c.SetReadDeadline(time.Now().Add(s.ReadHeaderTimeout))
        }
        rr := &http1.Reader{BR: br, MaxHeaderBytes: s.headerLimit(), MaxTotalHeaderBytes: s.totalHeaderLimit()}
        pr, err := rr.ReadRequest()
        if err != nil {
            // best-effort 400
            _ = http1.WriteResponse(bw, 400, "", map[string][]string{"Content-Length": {"0"}}, nil, false)
            _ = bw.Flush()
            s.logf(obs.Warn, "read request error from %s: %v", c.RemoteAddr(), err)
            return
        }

        // Decide keep-alive
        ka := false
        if pr.Proto == "HTTP/1.1" {
            ka = true
        }
        connVal := strings.ToLower(Header(pr.Header).Get("Connection"))
        if pr.Proto == "HTTP/1.1" {
            if connVal == "close" {
                ka = false
            }
        } else {
            if connVal == "keep-alive" {
                ka = true
            }
        }
        // Build httpx.Request
        var u *url.URL
        if strings.HasPrefix(pr.RequestURI, "http://") || strings.HasPrefix(pr.RequestURI, "https://") {
            u, _ = url.Parse(pr.RequestURI)
        } else {
            u, _ = url.ParseRequestURI(pr.RequestURI)
        }
        r := &Request{
            Method:        pr.Method,
            URL:           u,
            RequestURI:    pr.RequestURI,
            Proto:         pr.Proto,
            Header:        Header(pr.Header),
            Body:          pr.Body,
            Host:          Header(pr.Header).Get("Host"),
            ContentLength: pr.ContentLength,
        }
        // HTTP/1.1 requires Host header
        if pr.Proto == "HTTP/1.1" && r.Host == "" {
            _ = http1.WriteResponse(bw, 400, "", map[string][]string{"Content-Length": {"0"}}, nil, false)
            _ = bw.Flush()
            s.logf(obs.Warn, "missing Host header from %s", c.RemoteAddr())
            return
        }
        // Validate Host header format (reg-name / IPv4 / [IPv6]) with optional port
        if r.Host != "" && !isValidHostHeader(r.Host) {
            _ = http1.WriteResponse(bw, 400, "", map[string][]string{"Content-Length": {"0"}}, nil, false)
            _ = bw.Flush()
            s.logf(obs.Warn, "invalid Host header from %s: %q", c.RemoteAddr(), r.Host)
            return
        }
        // Correlation ID from headers (X-Request-ID, X-Correlation-ID, or traceparent)
        corr := r.Header.Get("X-Request-ID")
        if corr == "" { corr = r.Header.Get("X-Correlation-ID") }
        var tpTraceID, tpSpanID string
        if corr == "" {
            if tp := r.Header.Get("Traceparent"); tp != "" {
                if tid, sid, _, ok := parseTraceparent(tp); ok {
                    corr = tid
                    tpTraceID, tpSpanID = tid, sid
                }
            }
        } else {
            if tp := r.Header.Get("Traceparent"); tp != "" {
                if tid, sid, _, ok := parseTraceparent(tp); ok {
                    tpTraceID, tpSpanID = tid, sid
                }
            }
        }
        rid := genID()
        r.RequestID = rid
        r.CorrelationID = corr
        // Trace context: derive trace/span
        if tpTraceID == "" { tpTraceID = genTraceID() }
        r.TraceID = tpTraceID
        r.ParentSpanID = tpSpanID
        r.SpanID = genSpanID()
        // Also capture tracestate for propagation
        r.TraceState = r.Header.Get("Tracestate")
        // Attach ids to context for handlers
        ctx := context.Background()
        ctx = WithTrace(ctx, Trace{TraceID: r.TraceID, SpanID: r.SpanID, ParentSpanID: r.ParentSpanID, Flags: "01"})
        ctx = WithRequestID(ctx, rid)
        if corr != "" { ctx = WithCorrelationID(ctx, corr) }
        r.ctx = ctx

        // If Expect: 100-continue present, wrap body to send 100 upon first read with optional timeout.
        if strings.EqualFold(Header(pr.Header).Get("Expect"), "100-continue") {
            s.metricCounter("httpx_server_expect_present_total", 1)
            send := func() error {
                if err := http1.WriteContinue(bw); err != nil { return err }
                return bw.Flush()
            }
            r.Body = &expectBody{rc: r.Body, sendContinue: send, conn: c, timeout: s.ExpectContinueTimeout, s: s, rid: rid, wrapTime: time.Now()}
        }

        // Use streaming response writer by default
        srw := &connResponseWriter{bw: bw, proto: pr.Proto, keepAlive: ka, hdr: Header{}}
        var gzrw *gzipResponseWriter
        // Inject IDs to response headers if not set by handler
        if srw.Header().Get("X-Request-ID") == "" {
            srw.Header().Set("X-Request-ID", rid)
        }
        if corr != "" && srw.Header().Get("X-Correlation-ID") == "" {
            srw.Header().Set("X-Correlation-ID", corr)
        }
        h := s.Handler
        if h == nil {
            h = HandlerFunc(func(w ResponseWriter, r *Request) {
                w.WriteHeader(404)
                w.Write([]byte("not found"))
            })
        }

        // Execute handler
        s.logf(obs.Info, "req start id=%s method=%s uri=%s from=%s", rid, r.Method, r.RequestURI, c.RemoteAddr())
        start := time.Now()
        // Run handler with panic recovery
        func() {
            defer func() {
                if rec := recover(); rec != nil {
                    s.metricCounter("httpx_server_panics_total", 1, obs.Label{Key: "method", Value: r.Method})
                    s.logf(obs.Error, "panic recovered id=%s: %v\n%s", rid, rec, string(debug.Stack()))
                    // Ensure a 500 response is sent; close after
                    srw.keepAlive = false
                    if !srw.wroteHdr {
                        srw.Header().Set("Content-Type", "text/plain; charset=utf-8")
                        srw.WriteHeader(500)
                        _, _ = srw.Write([]byte("internal server error"))
                    }
                }
            }()

            // Optional request decompression (gzip)
            if s.DecompressRequest && strings.EqualFold(Header(pr.Header).Get("Content-Encoding"), "gzip") && r.Body != nil {
                gr, err := gzip.NewReader(r.Body)
                if err == nil {
                    r.Body = struct{ io.Reader; io.Closer }{Reader: gr, Closer: r.Body}
                    r.ContentLength = -1
                    r.Header.Del("Content-Encoding")
                } else {
                    s.logf(obs.Warn, "gzip reader init failed id=%s: %v", rid, err)
                }
            }
            // Optional response compression (gzip)
            if s.EnableGzip && acceptsGzip(r.Header) && !noResponseBody(0, r.Method) {
                // Ensure Vary header
                vary := srw.Header().Get("Vary")
                if vary == "" {
                    srw.Header().Set("Vary", "Accept-Encoding")
                } else if !strings.Contains(strings.ToLower(vary), "accept-encoding") {
                    srw.Header().Set("Vary", vary+", Accept-Encoding")
                }
                gzrw = &gzipResponseWriter{inner: srw, enabled: true}
                h.ServeHTTP(gzrw, r)
            } else {
                h.ServeHTTP(srw, r)
            }
        }()

        // If handler didn't close/drain body, do it here for keep-alive.
        if r.Body != nil {
            _ = r.Body.Close()
        }

        // Close gzip stream before finalizing
        if gzrw != nil { _ = gzrw.Close() }
        // Finalize streamed response: if chunked, write terminator.
        if s.WriteTimeout > 0 {
            _ = c.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
        }
        if srw.chunked {
            if err := http1.EndChunked(bw); err != nil {
                s.logf(obs.Warn, "end chunked failed to %s: %v", c.RemoteAddr(), err)
                return
            }
        }
        if err := bw.Flush(); err != nil {
            s.logf(obs.Warn, "flush failed to %s: %v", c.RemoteAddr(), err)
            return
        }

        // Decide if connection remains alive based on final headers/state
        finalKA := srw.keepAlive && (srw.chunked || srw.hdr.Get("Content-Length") != "" || noResponseBody(srw.status, r.Method))
        // Metrics per request
        s.metricCounter("httpx_server_requests_total", 1, obs.Label{Key: "method", Value: r.Method})
        s.metricCounter("httpx_server_responses_total", 1, obs.Label{Key: "status", Value: itoaStatus(srw.status)})
        s.metricHistogram("httpx_server_request_duration_ms", float64(time.Since(start).Milliseconds()),
            obs.Label{Key: "method", Value: r.Method}, obs.Label{Key: "status", Value: itoaStatus(srw.status)})
        s.logf(obs.Info, "req done id=%s status=%d dur_ms=%d", rid, srw.status, time.Since(start).Milliseconds())
        if !finalKA {
            alive = false
            break
        }
        // Reset deadlines for next request
        if s.IdleTimeout > 0 {
            _ = c.SetReadDeadline(time.Now().Add(s.IdleTimeout))
        } else if s.ReadTimeout > 0 {
            _ = c.SetReadDeadline(time.Now().Add(s.ReadTimeout))
        } else {
            _ = c.SetReadDeadline(time.Time{})
        }
    }
}

func (s *Server) headerLimit() int {
    if s.MaxHeaderBytes <= 0 {
        return 8 << 10
    }
    return s.MaxHeaderBytes
}

func (s *Server) totalHeaderLimit() int {
    if s.MaxTotalHeaderBytes <= 0 {
        return 64 << 10
    }
    return s.MaxTotalHeaderBytes
}

func isValidHostHeader(h string) bool {
    if h == "" { return false }
    if strings.IndexFunc(h, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) != -1 {
        return false
    }
    // IPv6 literal in brackets
    if strings.HasPrefix(h, "[") {
        i := strings.Index(h, "]")
        if i < 0 { return false }
        hostPart := h[1:i]
        if net.ParseIP(hostPart) == nil { return false }
        rest := h[i+1:]
        if rest == "" { return true }
        if !strings.HasPrefix(rest, ":") { return false }
        port := rest[1:]
        if port == "" { return false }
        return isValidPort(port)
    }
    // If more than one colon, it's invalid (IPv6 without brackets)
    if strings.Count(h, ":") > 1 {
        return false
    }
    if strings.Contains(h, ":") {
        host, port, err := net.SplitHostPort(h)
        if err != nil { return false }
        return (isValidHostNameOrIPv4(host) && isValidPort(port))
    }
    return isValidHostNameOrIPv4(h)
}

func isValidPort(p string) bool {
    if p == "" { return false }
    if len(p) > 5 { return false }
    for i := 0; i < len(p); i++ { if p[i] < '0' || p[i] > '9' { return false } }
    n, err := strconv.Atoi(p)
    if err != nil { return false }
    return n > 0 && n <= 65535
}

func isValidHostNameOrIPv4(s string) bool {
    if ip := net.ParseIP(s); ip != nil {
        // Must be IPv4 here; IPv6 should be bracketed
        return ip.To4() != nil
    }
    // Validate reg-name (simplified): labels of [A-Za-z0-9-], separated by '.', not starting/ending with '-'
    if s == "" { return false }
    // Disallow trailing dot for simplicity (could be allowed; tighten for MVP)
    if strings.HasSuffix(s, ".") { return false }
    labels := strings.Split(s, ".")
    for _, lab := range labels {
        if lab == "" { return false }
        if lab[0] == '-' || lab[len(lab)-1] == '-' { return false }
        for i := 0; i < len(lab); i++ {
            c := lab[i]
            if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
                continue
            }
            return false
        }
    }
    return true
}

// gzipResponseWriter wraps ResponseWriter to compress the response body.
type gzipResponseWriter struct {
    inner   ResponseWriter
    gz      *gzip.Writer
    enabled bool
    wroteH  bool
}

func (g *gzipResponseWriter) Header() Header { return g.inner.Header() }

func (g *gzipResponseWriter) WriteHeader(status int) {
    if g.wroteH { return }
    g.wroteH = true
    // If handler already set a different Content-Encoding, disable gzip
    ce := g.inner.Header().Get("Content-Encoding")
    if ce != "" && !strings.EqualFold(ce, "gzip") {
        g.enabled = false
        g.inner.WriteHeader(status)
        return
    }
    if g.enabled {
        g.inner.Header().Set("Content-Encoding", "gzip")
        g.inner.Header().Del("Content-Length")
    }
    g.inner.WriteHeader(status)
}

func (g *gzipResponseWriter) ensure() error {
    if g.gz != nil || !g.enabled { return nil }
    g.gz = gzip.NewWriter(&responseWriterAdapter{w: g.inner})
    return nil
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
    if !g.wroteH { g.WriteHeader(200) }
    if !g.enabled {
        return g.inner.Write(p)
    }
    if err := g.ensure(); err != nil { return 0, err }
    return g.gz.Write(p)
}

func (g *gzipResponseWriter) Flush() error {
    if fl, ok := g.inner.(Flusher); ok {
        return fl.Flush()
    }
    return nil
}

func (g *gzipResponseWriter) Close() error {
    if g.gz != nil {
        return g.gz.Close()
    }
    return nil
}

// responseWriterAdapter allows gzip.Writer to write to ResponseWriter.
type responseWriterAdapter struct { w ResponseWriter }

func (a *responseWriterAdapter) Write(p []byte) (int, error) { return a.w.Write(p) }

// acceptsGzip checks if client accepts gzip encoding.
func acceptsGzip(h Header) bool {
    v := h.Get("Accept-Encoding")
    if v == "" { return false }
    parts := strings.Split(v, ",")
    for _, p := range parts {
        p = strings.TrimSpace(strings.ToLower(p))
        if p == "" { continue }
        // strip parameters
        tok := p
        if i := strings.IndexByte(tok, ';'); i >= 0 { tok = tok[:i] }
        if tok == "gzip" {
            // Check for q=0 explicitly if provided
            if i := strings.IndexByte(p, ';'); i >= 0 {
                params := strings.TrimSpace(p[i+1:])
                if strings.Contains(params, "q=0") || strings.Contains(params, "q=0.0") {
                    return false
                }
            }
            return true
        }
    }
    return false
}

func noResponseBody(status int, method string) bool {
    if method == "HEAD" {
        return true
    }
    if status >= 100 && status < 200 {
        return true
    }
    return status == 204 || status == 304
}

func (s *Server) logf(level obs.Level, format string, args ...interface{}) {
    lg := s.Logger
    if lg == nil {
        lg = obs.NopLogger{}
    }
    lg.Logf(level, "%s", fmt.Sprintf(format, args...))
}

func (s *Server) metricCounter(name string, value float64, labels ...obs.Label) {
    m := s.Meter
    if m == nil {
        m = obs.NopMeter{}
    }
    m.Counter(name, value, labels...)
}

func (s *Server) metricHistogram(name string, value float64, labels ...obs.Label) {
    m := s.Meter
    if m == nil {
        m = obs.NopMeter{}
    }
    m.Histogram(name, value, labels...)
}

func itoaStatus(code int) string {
    // small helper without importing strconv to avoid churn; codes are small
    if code == 0 {
        return "0"
    }
    // fast path for common codes
    switch code {
    case 200:
        return "200"
    case 204:
        return "204"
    case 301:
        return "301"
    case 302:
        return "302"
    case 304:
        return "304"
    case 400:
        return "400"
    case 401:
        return "401"
    case 403:
        return "403"
    case 404:
        return "404"
    case 500:
        return "500"
    default:
        // generic int to string conversion
        return fmt.Sprintf("%d", code)
    }
}

// Shutdown gracefully stops accepting new connections and waits for ongoing
// connections to finish until ctx is done. It sets short deadlines on active
// connections to encourage handlers to return.
// Shutdown gracefully stops accepting new connections and waits for
// in‑flight requests to complete until ctx is done.
func (s *Server) Shutdown(ctx context.Context) error {
    s.mu.Lock()
    if s.closing {
        s.mu.Unlock()
        return nil
    }
    s.closing = true
    ln := s.listener
    conns := make([]net.Conn, 0, len(s.conns))
    for c := range s.conns { conns = append(conns, c) }
    s.mu.Unlock()
    if ln != nil {
        _ = ln.Close()
    }
    // Set short deadlines to interrupt blocking I/O
    for _, c := range conns {
        _ = c.SetDeadline(time.Now().Add(250 * time.Millisecond))
    }
    done := make(chan struct{})
    go func(){ s.wg.Wait(); close(done) }()
    select {
    case <-ctx.Done():
        return ctx.Err()
    case <-done:
        return nil
    }
}

// expectBody defers sending 100-continue until the first read.
type expectBody struct {
    rc            io.ReadCloser
    sendContinue  func() error
    sent          bool
    conn          net.Conn
    timeout       time.Duration
    s             *Server
    rid           string
    wrapTime      time.Time
}

func (b *expectBody) ensure() error {
    if b.sent { return nil }
    b.sent = true
    if b.timeout > 0 && b.conn != nil {
        _ = b.conn.SetReadDeadline(time.Now().Add(b.timeout))
    }
    if b.sendContinue != nil {
        if err := b.sendContinue(); err != nil {
            if b.s != nil { b.s.metricCounter("httpx_server_continue_error_total", 1) }
            return err
        }
        if b.s != nil {
            b.s.metricCounter("httpx_server_continue_sent_total", 1)
            if !b.wrapTime.IsZero() {
                b.s.metricHistogram("httpx_server_expect_wait_ms", float64(time.Since(b.wrapTime).Milliseconds()))
            }
            if b.rid != "" {
                b.s.logf(obs.Info, "continue sent id=%s wait_ms=%d", b.rid, time.Since(b.wrapTime).Milliseconds())
            }
        }
        return nil
    }
    return nil
}

func (b *expectBody) Read(p []byte) (int, error) {
    if err := b.ensure(); err != nil { return 0, err }
    return b.rc.Read(p)
}

func (b *expectBody) Close() error {
    return b.rc.Close()
}
