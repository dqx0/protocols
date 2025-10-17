package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

func genID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// Fallback to timestamp-based ID if rand fails (unlikely)
	t := time.Now().UnixNano()
	var fb [16]byte
	for i := 0; i < 16; i++ {
		fb[i] = byte(t >> (uint(i%8) * 8))
	}
	return hex.EncodeToString(fb[:])
}
