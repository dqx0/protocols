package httpx

import "io"

// Response represents an HTTP response.
//
// Body must be closed by the caller when done reading.
type Response struct {
	Status        string
	StatusCode    int
	Proto         string
	Header        Header
	Body          io.ReadCloser
	ContentLength int64
}
