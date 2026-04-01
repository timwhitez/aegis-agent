package events

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const SchemaVersion = 1

type Event struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	SessionID     string         `json:"session_id"`
	Type          string         `json:"type"`
	Time          string         `json:"time"`
	Phase         string         `json:"phase"`
	Data          map[string]any `json:"data,omitempty"`
}

func New(sessionID, eventType, phase string, data map[string]any) Event {
	return Event{
		SchemaVersion: SchemaVersion,
		ID:            newID("evt"),
		SessionID:     sessionID,
		Type:          eventType,
		Time:          time.Now().UTC().Format(time.RFC3339Nano),
		Phase:         phase,
		Data:          data,
	}
}

type Bus struct {
	mu   sync.RWMutex
	subs []chan Event
}

func NewBus() *Bus {
	return &Bus{}
}

func (b *Bus) Subscribe(buffer int) <-chan Event {
	ch := make(chan Event, buffer)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, ch)
	return ch
}

func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

func newID(prefix string) string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return prefix + "-fallback"
	}
	return prefix + "_" + hex.EncodeToString(buf)
}
