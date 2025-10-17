package httpx_test

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"dqx0.com/go/protcols/httpx"
)

// ExampleHeader shows basic header operations.
func ExampleHeader() {
	h := httpx.Header{}
	h.Add("X-Foo", "a")
	h.Add("X-Foo", "b")
	h.Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Println(h.Get("x-foo"))  // canonical lookup
	fmt.Println(len(h["X-Foo"])) // two values
	h.Del("X-Foo")
	fmt.Println(h.Get("X-Foo"))
	// Output:
	// a
	// 2
	//
}

// ExampleTraceStateBuilder builds a tracestate value safely.
func ExampleTraceStateBuilder() {
	b := httpx.NewTraceStateBuilder("vendor1=abc")
	b.Set("vendor2", "xyz")
	b.Set("vendor1", "def") // moves to front
	fmt.Println(b.String())
	// Output:
	// vendor1=def,vendor2=xyz
}

// ExampleTrace_context shows storing and retrieving trace info via context.
func ExampleTrace_context() {
	tr := httpx.Trace{TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", Flags: "01"}
	ctx := httpx.WithTrace(context.Background(), tr)
	got, ok := httpx.TraceFrom(ctx)
	fmt.Println(ok && got.TraceID == tr.TraceID)
	// Output:
	// true
}

// ExampleClient_build illustrates preparing a request for sending.
func ExampleClient_build() {
	c := &httpx.Client{}
	req := &httpx.Request{Method: "POST"}
	req.Header = httpx.Header{"Content-Type": {"text/plain"}}
	req.Body = io.NopCloser(strings.NewReader("hello"))
	req.ContentLength = 5
	_ = c // use with c.Do(req)
}

// ExampleServer_SSE shows a minimal Server‑Sent Events style handler.
func Example_flusherSSE() {
	h := httpx.HandlerFunc(func(w httpx.ResponseWriter, r *httpx.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)
		for i := 0; i < 3; i++ {
			// SSE format: lines beginning with "data:"
			w.Write([]byte(fmt.Sprintf("data: %d\n\n", i)))
			if f, ok := w.(httpx.Flusher); ok {
				_ = f.Flush()
			}
		}
	})
	_ = h // attach to httpx.Server in real usage
}

// ExampleClient_redirectPolicy customizes redirect handling.
func ExampleClient_redirectPolicy() {
	c := &httpx.Client{MaxRedirects: 5}
	c.RedirectPolicy = func(prev *httpx.Request, res *httpx.Response, n int) (*httpx.Request, error) {
		// Disallow leaving original host
		if prev.URL != nil {
			loc := res.Header.Get("Location")
			if loc != "" {
				u, _ := url.Parse(loc)
				if !u.IsAbs() && prev.URL != nil {
					u = prev.URL.ResolveReference(u)
				}
				if u != nil && u.Host != prev.URL.Host {
					return nil, fmt.Errorf("redirect to different host blocked: %s", u.Host)
				}
				// default follow with GET for 303
				if res.StatusCode == 303 {
					return &httpx.Request{Method: "GET", URL: u, Header: httpx.Header{}}, nil
				}
				return &httpx.Request{Method: prev.Method, URL: u, Header: httpx.Header{}}, nil
			}
		}
		return nil, nil
	}
}

// ExampleClient_expectContinue demonstrates setting Expect: 100-continue.
// Note: BasicTransport does not (yet) implement automatic 100 wait on client side;
// the server side in httpx supports delayed 100 upon first Body.Read.
func ExampleClient_expectContinue() {
	req := &httpx.Request{Method: "POST"}
	req.Header = httpx.Header{"Content-Type": {"text/plain"}}
	req.Header.Set("Expect", "100-continue")
	req.Body = io.NopCloser(strings.NewReader("large-body"))
	req.ContentLength = int64(len("large-body"))
}

// ExampleTraceState_limit shows trimming tracestate to a size budget.
func ExampleTraceStateBuilder_limit() {
	b := httpx.NewTraceStateBuilder("")
	// add entries...
	b.Set("vendor", strings.Repeat("x", 600))
	ts := b.String()
	// enforce 512‑byte budget
	if len(ts) > 512 {
		ts = ts[:512]
	}
	fmt.Println(len(ts) <= 512)
	// Output:
	// true
}
