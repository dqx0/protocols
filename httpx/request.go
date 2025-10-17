package httpx

import (
    "io"
    "net/url"
)

type Request struct {
    Method        string
    URL           *url.URL
    RequestURI    string
    Proto         string
    Header        Header
    Body          io.ReadCloser
    Host          string
    ContentLength int64
}

