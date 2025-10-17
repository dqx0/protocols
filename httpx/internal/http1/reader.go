package http1

import (
    "bufio"
    "io"
    "strconv"
    "strings"
)

// ParsedRequest is a minimal representation parsed from the wire.
type ParsedRequest struct {
    Method        string
    RequestURI    string
    Proto         string
    Header        map[string][]string
    ContentLength int64
    Body          io.ReadCloser
}

type Reader struct {
    BR             *bufio.Reader
    MaxHeaderBytes int
}

func (r *Reader) ReadRequest() (*ParsedRequest, error) {
    line, err := r.readLine()
    if err != nil {
        return nil, err
    }
    parts := strings.SplitN(line, " ", 3)
    if len(parts) != 3 {
        return nil, io.ErrUnexpectedEOF
    }
    method, uri, proto := parts[0], parts[1], parts[2]
    if !strings.HasPrefix(proto, "HTTP/1.") {
        return nil, io.ErrUnexpectedEOF
    }
    hdr, err := r.readHeaders()
    if err != nil {
        return nil, err
    }
    // Decide body source: chunked TE, else Content-Length, else empty
    var cl int64 = 0
    var body io.ReadCloser
    if hasChunkedTE(hdr) {
        cl = -1
        body = newChunkedBody(r.BR, r.MaxHeaderBytes)
    } else if v := getHeader(hdr, "Content-Length"); v != "" {
        n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
        if err != nil || n < 0 {
            return nil, io.ErrUnexpectedEOF
        }
        cl = n
        if cl > 0 {
            lr := &io.LimitedReader{R: r.BR, N: cl}
            body = &limitedBody{lr: lr}
        } else {
            body = io.NopCloser(strings.NewReader(""))
        }
    } else {
        body = io.NopCloser(strings.NewReader(""))
    }
    return &ParsedRequest{
        Method:        method,
        RequestURI:    uri,
        Proto:         proto,
        Header:        hdr,
        ContentLength: cl,
        Body:          body,
    }, nil
}

func (r *Reader) readHeaders() (map[string][]string, error) {
    h := make(map[string][]string)
    for {
        line, err := r.readLine()
        if err != nil {
            return nil, err
        }
        if line == "" {
            break
        }
        i := strings.IndexByte(line, ':')
        if i <= 0 {
            return nil, io.ErrUnexpectedEOF
        }
        k := strings.TrimSpace(line[:i])
        v := strings.TrimSpace(line[i+1:])
        addHeader(h, k, v)
    }
    return h, nil
}

func (r *Reader) readLine() (string, error) {
    var sb strings.Builder
    for {
        b, err := r.BR.ReadByte()
        if err != nil {
            return "", err
        }
        if b == '\n' {
            break
        }
        if b != '\r' {
            sb.WriteByte(b)
        }
        if r.MaxHeaderBytes > 0 && sb.Len() > r.MaxHeaderBytes {
            return "", io.ErrShortBuffer
        }
    }
    return sb.String(), nil
}

type limitedBody struct {
    lr *io.LimitedReader
}

func (b *limitedBody) Read(p []byte) (int, error) { return b.lr.Read(p) }

func (b *limitedBody) Close() error {
    // Drain remaining bytes to allow next request on the same connection.
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

func addHeader(h map[string][]string, k, v string) {
    hk := canonicalHeaderKey(k)
    h[hk] = append(h[hk], v)
}

func getHeader(h map[string][]string, k string) string {
    hk := canonicalHeaderKey(k)
    if vv, ok := h[hk]; ok && len(vv) > 0 {
        return vv[0]
    }
    return ""
}

func hasChunkedTE(h map[string][]string) bool {
    hk := canonicalHeaderKey("Transfer-Encoding")
    if vv, ok := h[hk]; ok {
        for _, v := range vv {
            if strings.Contains(strings.ToLower(v), "chunked") {
                return true
            }
        }
    }
    return false
}

// Very small canonicalizer to avoid importing textproto here.
func canonicalHeaderKey(s string) string {
    b := []byte(strings.ToLower(s))
    upper := true
    for i, c := range b {
        if c >= 'a' && c <= 'z' {
            if upper {
                b[i] = byte(c - 'a' + 'A')
            }
            upper = false
            continue
        }
        upper = c == '-'
    }
    return string(b)
}
