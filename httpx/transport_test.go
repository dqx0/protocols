package httpx

import (
	"net/url"
	"testing"
)

func TestProxyFromEnvironment_NO_PROXY_CIDR(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:8080")
	t.Setenv("NO_PROXY", "10.0.0.0/8,localhost")

	u1, _ := url.Parse("http://10.10.10.10/")
	r1 := &Request{Method: "GET", URL: u1}
	if got, _ := ProxyFromEnvironment(r1); got != nil {
		t.Fatalf("expected no proxy for CIDR match, got %v", got)
	}

	u2, _ := url.Parse("http://example.com/")
	r2 := &Request{Method: "GET", URL: u2}
	if got, _ := ProxyFromEnvironment(r2); got == nil {
		t.Fatalf("expected proxy for example.com")
	}
}
