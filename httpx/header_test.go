package httpx

import "testing"

func TestHeaderCanonicalization(t *testing.T) {
	h := Header{}
	h.Add("x-foo", "a")
	h.Add("X-Foo", "b")
	if got := h.Get("X-FOO"); got != "a" {
		t.Fatalf("Get canonical = %q, want %q", got, "a")
	}
	if got := len(h["X-Foo"]); got != 2 {
		t.Fatalf("len values = %d, want 2", got)
	}
	h.Set("content-type", "text/plain")
	if got := h.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("content-type = %q", got)
	}
	h.Del("x-foo")
	if got := h.Get("X-Foo"); got != "" {
		t.Fatalf("after Del, got %q, want empty", got)
	}
}
