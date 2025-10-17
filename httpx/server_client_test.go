package httpx

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/url"
	"testing"
)

func startServer(t *testing.T, h Handler, cfg func(*Server)) (*Server, string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &Server{Handler: h}
	if cfg != nil {
		cfg(s)
	}
	go func() { _ = s.Serve(ln) }()
	addr := ln.Addr().String()
	shutdown := func() { _ = s.Shutdown(context.Background()) }
	return s, "http://" + addr + "/", shutdown
}

func TestServerClient_GET(t *testing.T) {
	h := HandlerFunc(func(w ResponseWriter, r *Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	_, base, stop := startServer(t, h, nil)
	defer stop()

	c := &Client{}
	res, err := c.Get(base)
	if err != nil {
		t.Fatalf("client get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	b, _ := io.ReadAll(res.Body)
	if string(b) != "ok" {
		t.Fatalf("body=%q", string(b))
	}
}

func TestServer_Gzip(t *testing.T) {
	long := make([]byte, 4096)
	for i := range long {
		long[i] = 'A'
	}
	h := HandlerFunc(func(w ResponseWriter, r *Request) {
		w.WriteHeader(200)
		w.Write(long)
	})
	_, base, stop := startServer(t, h, func(s *Server) { s.EnableGzip = true })
	defer stop()

	c := &Client{}
	// Build request with Accept-Encoding: gzip
	req := &Request{Method: "GET"}
	u, _ := url.Parse(base)
	req.URL = u
	req.Header = Header{"Accept-Encoding": {"gzip"}}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("client do: %v", err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q", got)
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	dec, _ := io.ReadAll(zr)
	if string(dec) != string(long) {
		t.Fatalf("decoded mismatch: %d vs %d", len(dec), len(long))
	}
}
