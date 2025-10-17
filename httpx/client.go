package httpx

import (
    "errors"
    "time"
)

type Transport interface {
    RoundTrip(*Request) (*Response, error)
}

type Client struct {
    Transport            Transport
    Timeout              time.Duration
    MaxConnsPerHost      int
    IdleConnTimeout      time.Duration
    ExpectContinueTimeout time.Duration
}

func (c *Client) Do(r *Request) (*Response, error) {
    return nil, errors.New("httpx: client not implemented")
}

