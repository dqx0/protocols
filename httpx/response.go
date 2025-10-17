package httpx

import "io"

type Response struct {
    Status        string
    StatusCode    int
    Proto         string
    Header        Header
    Body          io.ReadCloser
    ContentLength int64
}

