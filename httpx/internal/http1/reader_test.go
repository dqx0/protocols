package http1

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func readReq(t *testing.T, raw string, maxLine, maxTotal int) (*ParsedRequest, error) {
	t.Helper()
	r := &Reader{BR: bufio.NewReader(strings.NewReader(raw)), MaxHeaderBytes: maxLine, MaxTotalHeaderBytes: maxTotal}
	return r.ReadRequest()
}

func TestReader_ContentLengthBody(t *testing.T) {
	raw := "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\nhello"
	pr, err := readReq(t, raw, 8<<10, 64<<10)
	if err != nil {
		t.Fatalf("ReadRequest error: %v", err)
	}
	if pr.ContentLength != 5 {
		t.Fatalf("ContentLength=%d", pr.ContentLength)
	}
	b, _ := io.ReadAll(pr.Body)
	if string(b) != "hello" {
		t.Fatalf("body=%q", string(b))
	}
}

func TestReader_ChunkedBody(t *testing.T) {
	raw := "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nhey\r\n2\r\n!!\r\n0\r\n\r\n"
	pr, err := readReq(t, raw, 8<<10, 64<<10)
	if err != nil {
		t.Fatalf("ReadRequest error: %v", err)
	}
	if pr.ContentLength != -1 {
		t.Fatalf("ContentLength=%d", pr.ContentLength)
	}
	b, _ := io.ReadAll(pr.Body)
	if string(b) != "hey!!" {
		t.Fatalf("body=%q", string(b))
	}
}

func TestReader_CLTEConflict(t *testing.T) {
	raw := "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\nContent-Length: 5\r\n\r\n"
	if _, err := readReq(t, raw, 8<<10, 64<<10); err == nil {
		t.Fatal("expected error for CL/TE conflict")
	}
}

func TestReader_MultipleContentLengthMismatch(t *testing.T) {
	raw := "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 5, 6\r\n\r\n"
	if _, err := readReq(t, raw, 8<<10, 64<<10); err == nil {
		t.Fatal("expected error for mismatched Content-Length")
	}
}

func TestReader_InvalidHeaderName(t *testing.T) {
	raw := "GET / HTTP/1.1\r\nBad( : v\r\n\r\n"
	if _, err := readReq(t, raw, 8<<10, 64<<10); err == nil {
		t.Fatal("expected error for invalid header name")
	}
}

func TestReader_MaxTotalHeaderBytes(t *testing.T) {
	raw := "GET / HTTP/1.1\r\nA: b\r\nC: d\r\nE: f\r\n\r\n"
	if _, err := readReq(t, raw, 8<<10, 6); err == nil { // 3 lines exceed total (approx)
		t.Fatal("expected error for MaxTotalHeaderBytes")
	}
}
