package httpx

import "errors"

var (
    ErrBadRequest         = errors.New("httpx: bad request")
    ErrHeaderTooLarge     = errors.New("httpx: header too large")
    ErrBodyTooLarge       = errors.New("httpx: body too large")
    ErrTimeout            = errors.New("httpx: timeout")
    ErrProtocolViolation  = errors.New("httpx: protocol violation")
)

