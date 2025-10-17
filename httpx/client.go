package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"
)

// Transport performs a single HTTP round trip.
//
// Implementations must be safe for concurrent use by multiple goroutines.
type Transport interface {
	RoundTrip(*Request) (*Response, error)
}

// Client issues HTTP requests via a Transport.
//
// Fields provide common knobs for library usage. When Transport is nil,
// a new BasicTransport is created with sane defaults per client.
type Client struct {
	Transport             Transport
	Timeout               time.Duration
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	ExpectContinueTimeout time.Duration
	MaxRedirects          int
	// RedirectPolicy, if set, overrides the default redirect behavior.
	RedirectPolicy func(prev *Request, res *Response, count int) (*Request, error)
}

// Do sends an HTTP request and returns an HTTP response.
func (c *Client) Do(r *Request) (*Response, error) {
	if r == nil {
		return nil, errors.New("httpx: nil request")
	}
	if c.Transport == nil {
		bt := NewBasicTransport()
		if c.MaxConnsPerHost > 0 {
			bt.MaxConnsPerHost = c.MaxConnsPerHost
		}
		if c.IdleConnTimeout > 0 {
			bt.IdleConnTimeout = c.IdleConnTimeout
		}
		c.Transport = bt
	}
	max := c.MaxRedirects
	if max <= 0 {
		max = 10
	}
	// Apply client timeout to request context
	ctx := r.Context()
	var cancel context.CancelFunc
	if c.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	req := WithContext(r, ctx)
	for i := 0; ; i++ {
		res, err := c.Transport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if !isRedirect(res.StatusCode) {
			return res, nil
		}
		// Compute next request
		var next *Request
		if c.RedirectPolicy != nil {
			n, err := c.RedirectPolicy(req, res, i)
			if err != nil {
				return res, err
			}
			next = n
		} else {
			n, err := defaultRedirectPolicy(req, res, i)
			if err != nil {
				return res, err
			}
			next = n
		}
		if next == nil {
			return res, nil
		}
		// Close response to reuse connection before next request
		_ = res.Body.Close()
		if i+1 > max {
			return nil, errors.New("httpx: too many redirects")
		}
		req = WithContext(next, ctx)
	}
}

// Get issues a GET to the provided URL.
func (c *Client) Get(rawurl string) (*Response, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	req := &Request{Method: "GET", URL: u, Header: Header{}, ContentLength: 0}
	return c.Do(req)
}

// Post issues a POST with content type ctype and body.
func (c *Client) Post(rawurl, ctype string, body io.Reader) (*Response, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	// Buffer body to enable safe resend via GetBody for redirects.
	var buf bytes.Buffer
	if body != nil {
		if _, err := io.Copy(&buf, body); err != nil {
			return nil, err
		}
	}
	makeBody := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil }
	rc, _ := makeBody()
	req := &Request{Method: "POST", URL: u, Header: Header{"Content-Type": {ctype}}, Body: rc, GetBody: makeBody, ContentLength: int64(buf.Len())}
	return c.Do(req)
}

// CloseIdleConnections closes any idle connections on the underlying transport
// when it is a *BasicTransport.
// CloseIdleConnections closes any idle connections when the underlying
// Transport is a *BasicTransport. Active connections are not affected.
func (c *Client) CloseIdleConnections() {
	if bt, ok := c.Transport.(*BasicTransport); ok {
		bt.CloseIdleConnections()
	}
}

func isRedirect(code int) bool {
	switch code {
	case 301, 302, 303, 307, 308:
		return true
	default:
		return false
	}
}

func defaultRedirectPolicy(req *Request, res *Response, count int) (*Request, error) {
	loc := res.Header.Get("Location")
	if loc == "" {
		return nil, nil
	}
	u, err := url.Parse(loc)
	if err != nil {
		return nil, nil
	}
	if !u.IsAbs() && req.URL != nil {
		u = req.URL.ResolveReference(u)
	}
	// Shallow copy headers
	hdr := cloneHeader(req.Header)
	// If host changes, remove Authorization for safety
	if req.URL != nil && !sameHost(req.URL, u) {
		hdr.Del("Authorization")
	}
	method := req.Method
	var body io.ReadCloser
	var cl int64 = 0
	switch res.StatusCode {
	case 301, 302:
		if method == "GET" || method == "HEAD" {
			// preserve method
		} else {
			method = "GET"
		}
		body = nil
		cl = 0
		hdr.Del("Content-Length")
		hdr.Del("Content-Type")
	case 303:
		method = "GET"
		body = nil
		cl = 0
		hdr.Del("Content-Length")
		hdr.Del("Content-Type")
	case 307, 308:
		// Preserve method and body. Use GetBody to safely recreate the body.
		if req.Body != nil && req.ContentLength != 0 {
			if req.GetBody == nil {
				return nil, errors.New("httpx: redirect requires resending body but GetBody is nil")
			}
			rc, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			body = rc
			cl = req.ContentLength
		} else {
			body = nil
			cl = 0
			hdr.Del("Content-Length")
			hdr.Del("Content-Type")
		}
	}
	return &Request{Method: method, URL: u, Header: hdr, Body: body, ContentLength: cl}, nil
}

func cloneHeader(h Header) Header {
	if h == nil {
		return Header{}
	}
	cp := make(Header, len(h))
	for k, v := range h {
		vv := make([]string, len(v))
		copy(vv, v)
		cp[k] = vv
	}
	return cp
}

func sameHost(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return hostOnly(a.Host) == hostOnly(b.Host)
}

func hostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i != -1 {
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			return strings.Trim(h, "[]")
		}
		return h[:i]
	}
	return h
}
