package http1

import (
    "bufio"
    "errors"
    "fmt"
    "io"
    "strconv"
    "strings"
)

var (
    errChunkFormat = errors.New("http1: invalid chunk format")
)

// chunkedBody implements io.ReadCloser for Transfer-Encoding: chunked.
type chunkedBody struct {
    br         *bufio.Reader
    remain     int64
    finished   bool
    maxLine    int // line limit for chunk header and trailer lines
}

func newChunkedBody(br *bufio.Reader, maxLine int) io.ReadCloser {
    return &chunkedBody{br: br, remain: -1, maxLine: maxLine}
}

func (c *chunkedBody) Read(p []byte) (int, error) {
    if c.finished {
        return 0, io.EOF
    }
    // If no remaining bytes in current chunk, read next chunk size
    if c.remain == -1 || c.remain == 0 {
        size, err := c.readChunkSize()
        if err != nil {
            return 0, err
        }
        if size == 0 {
            // Read and discard trailers until empty line
            if err := c.readTrailers(); err != nil {
                return 0, err
            }
            c.finished = true
            return 0, io.EOF
        }
        c.remain = size
    }
    if c.remain < 0 {
        return 0, errChunkFormat
    }
    if len(p) == 0 {
        return 0, nil
    }
    // Read up to remaining bytes in this chunk
    toRead := int64(len(p))
    if toRead > c.remain {
        toRead = c.remain
    }
    n, err := io.ReadFull(c.br, p[:toRead])
    c.remain -= int64(n)
    if err != nil {
        return n, err
    }
    // If we consumed this chunk, expect CRLF boundary
    if c.remain == 0 {
        if err := c.expectCRLF(); err != nil {
            return n, err
        }
        // Mark to read the next chunk size on next call
    }
    return n, nil
}

func (c *chunkedBody) Close() error {
    // Drain to end so connection can be reused
    buf := make([]byte, 1024)
    for !c.finished {
        _, err := c.Read(buf)
        if err == io.EOF {
            break
        }
        if err != nil {
            return err
        }
    }
    return nil
}

func (c *chunkedBody) readChunkSize() (int64, error) {
    line, err := readLineLimit(c.br, c.maxLine)
    if err != nil {
        return 0, err
    }
    // Strip chunk extensions if any: "<hex>;<ext>"
    if i := strings.IndexByte(line, ';'); i >= 0 {
        line = line[:i]
    }
    line = strings.TrimSpace(line)
    if line == "" {
        return 0, errChunkFormat
    }
    // Parse hex number
    n, err := strconv.ParseInt(line, 16, 64)
    if err != nil || n < 0 {
        return 0, errChunkFormat
    }
    return n, nil
}

func (c *chunkedBody) expectCRLF() error {
    b1, err := c.br.ReadByte()
    if err != nil {
        return err
    }
    b2, err := c.br.ReadByte()
    if err != nil {
        return err
    }
    if b1 != '\r' || b2 != '\n' {
        return fmt.Errorf("http1: expected CRLF after chunk, got %q%q", b1, b2)
    }
    return nil
}

func (c *chunkedBody) readTrailers() error {
    for {
        line, err := readLineLimit(c.br, c.maxLine)
        if err != nil {
            return err
        }
        if line == "" {
            return nil
        }
        // We ignore trailer headers for now.
    }
}

func readLineLimit(br *bufio.Reader, limit int) (string, error) {
    var sb strings.Builder
    for {
        b, err := br.ReadByte()
        if err != nil {
            return "", err
        }
        if b == '\n' {
            break
        }
        if b != '\r' {
            sb.WriteByte(b)
        }
        if limit > 0 && sb.Len() > limit {
            return "", io.ErrShortBuffer
        }
    }
    return sb.String(), nil
}

