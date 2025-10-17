// Package httpx provides a small, security‑minded HTTP/1.1
// server and client implementation aimed at learning, control,
// and embeddability in libraries and tools.
//
// Highlights
//   - Server: streaming ResponseWriter, keep‑alive, chunked
//     transfer, Expect: 100‑continue, gzip (opt‑in), robust
//     parsing with CL/TE validation, header size limits, host
//     validation, graceful shutdown, logging/metrics hooks.
//   - Client: minimal HTTP/1.1 transport with connection pooling,
//     proxy (HTTP/HTTPS via CONNECT), TLS (SNI/ALPN), context
//     deadlines, redirects, and basic tracing headers.
//   - Observability: plug‑in Logger and Meter interfaces.
//
// Quick start (server):
//
//	s := &httpx.Server{Addr: ":8080"}
//	s.Handler = httpx.HandlerFunc(func(w httpx.ResponseWriter, r *httpx.Request) {
//	    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
//	    w.WriteHeader(200)
//	    w.Write([]byte("hello"))
//	})
//	if err := s.ListenAndServe(); err != nil { log.Fatal(err) }
//
// Quick start (client):
//
//	c := &httpx.Client{}
//	res, err := c.Get("http://127.0.0.1:8080/")
//	if err != nil { log.Fatal(err) }
//	defer res.Body.Close()
//	b, _ := io.ReadAll(res.Body)
//	fmt.Println(res.StatusCode, string(b))
package httpx
