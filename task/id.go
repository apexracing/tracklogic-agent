package task

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

var fallbackID uint64

// NewID returns an opaque process-local identifier suitable for Task protocol
// fields. Task IDs themselves remain caller-owned.
func NewID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(raw[:])
	}
	sequence := atomic.AddUint64(&fallbackID, 1)
	return fmt.Sprintf("%s_%x_%x", prefix, time.Now().UnixNano(), sequence)
}
