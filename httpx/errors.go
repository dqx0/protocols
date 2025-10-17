package httpx

import "errors"

// Common error values returned by client/server operations.
var (
	// ErrBadRequest indicates a malformed request or response.
	ErrBadRequest = errors.New("httpx: bad request")
	// ErrHeaderTooLarge indicates headers exceeded configured limits.
	ErrHeaderTooLarge = errors.New("httpx: header too large")
	// ErrBodyTooLarge indicates a body exceeded configured limits.
	ErrBodyTooLarge = errors.New("httpx: body too large")
	// ErrTimeout indicates an operation timed out.
	ErrTimeout = errors.New("httpx: timeout")
	// ErrProtocolViolation indicates an HTTP protocol violation was detected.
	ErrProtocolViolation = errors.New("httpx: protocol violation")
)
