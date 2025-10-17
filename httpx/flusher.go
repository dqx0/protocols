package httpx

// Flusher allows a handler to flush buffered data to the client
// mid‑response (useful for streaming and server‑sent events).
type Flusher interface {
    Flush() error
}
