package httpx

import (
    "bufio"
    "bytes"
    "net"
    "net/url"
    "strings"
    "time"

    "dqx0.com/go/protcols/httpx/internal/http1"
)

type Handler interface {
    ServeHTTP(ResponseWriter, *Request)
}

type HandlerFunc func(ResponseWriter, *Request)

func (f HandlerFunc) ServeHTTP(w ResponseWriter, r *Request) {
    f(w, r)
}

type ResponseWriter interface {
    Header() Header
    Write([]byte) (int, error)
    WriteHeader(status int)
}

type Server struct {
    Addr               string
    Handler            Handler
    ReadTimeout        time.Duration
    ReadHeaderTimeout  time.Duration
    WriteTimeout       time.Duration
    IdleTimeout        time.Duration
    MaxHeaderBytes     int
    MaxBodyBytes       int64
}

func (s *Server) ListenAndServe() error {
    addr := s.Addr
    if addr == "" {
        addr = ":8080"
    }
    ln, err := net.Listen("tcp", addr)
    if err != nil {
        return err
    }
    return s.Serve(ln)
}

func (s *Server) Serve(l net.Listener) error {
    defer l.Close()
    for {
        c, err := l.Accept()
        if err != nil {
            return err
        }
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
    defer c.Close()
    br := bufio.NewReader(c)
    bw := bufio.NewWriter(c)
    var alive = true
    for alive {
        if s.ReadHeaderTimeout > 0 {
            _ = c.SetReadDeadline(time.Now().Add(s.ReadHeaderTimeout))
        }
        rr := &http1.Reader{BR: br, MaxHeaderBytes: s.headerLimit()}
        pr, err := rr.ReadRequest()
        if err != nil {
            // best-effort 400
            _ = http1.WriteResponse(bw, 400, "", map[string][]string{"Content-Length": {"0"}}, nil, false)
            _ = bw.Flush()
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

        // If Expect: 100-continue present, send interim response so client sends body.
        if strings.EqualFold(Header(pr.Header).Get("Expect"), "100-continue") {
            // Ignore error; if it fails we'll fail later anyway.
            _ = http1.WriteContinue(bw)
            _ = bw.Flush()
        }

        // Use streaming response writer by default
        srw := &connResponseWriter{bw: bw, proto: pr.Proto, keepAlive: ka, hdr: Header{}}
        h := s.Handler
        if h == nil {
            h = HandlerFunc(func(w ResponseWriter, r *Request) {
                w.WriteHeader(404)
                w.Write([]byte("not found"))
            })
        }

        // Execute handler
        h.ServeHTTP(srw, r)

        // If handler didn't close/drain body, do it here for keep-alive.
        if r.Body != nil {
            _ = r.Body.Close()
        }

        // Finalize streamed response: if chunked, write terminator.
        if s.WriteTimeout > 0 {
            _ = c.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
        }
        if srw.chunked {
            if err := http1.EndChunked(bw); err != nil {
                return
            }
        }
        if err := bw.Flush(); err != nil {
            return
        }

        // Decide if connection remains alive based on final headers/state
        finalKA := srw.keepAlive && (srw.chunked || srw.hdr.Get("Content-Length") != "" || noResponseBody(srw.status, r.Method))
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

func noResponseBody(status int, method string) bool {
    if method == "HEAD" {
        return true
    }
    if status >= 100 && status < 200 {
        return true
    }
    return status == 204 || status == 304
}
