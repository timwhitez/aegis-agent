package task

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

var idCounter uint64

func NewID(prefix string) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	n := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("%s-%s-%04d-%s", prefix, time.Now().UTC().Format("20060102T150405"), n%10000, hex.EncodeToString(b[:]))
}

func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
