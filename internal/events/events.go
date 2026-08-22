package events

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

const SchemaVersion = 1

const (
	EventRalphLoopTriggered = "ralph_loop.triggered"
	EventRalphLoopCompleted = "ralph_loop.completed"
	EventRalphLoopExhausted = "ralph_loop.exhausted"

	// EventEventsDropped is emitted into a subscriber's own channel when events
	// destined for it had to be dropped because its buffer was full.
	EventEventsDropped = "events.dropped"
)

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
	subs []*subscriber
}

type subscriber struct {
	ch chan Event
	// pending counts events dropped for this subscriber that have not been
	// reported to it yet; total is the cumulative drop count for observability.
	pending atomic.Int64
	total   atomic.Int64
}

func NewBus() *Bus {
	return &Bus{}
}

func (b *Bus) Subscribe(buffer int) <-chan Event {
	sub := &subscriber{ch: make(chan Event, buffer)}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, sub)
	return sub.ch
}

// Publish fans an event out to every subscriber and never blocks the producer.
// A subscriber that cannot keep up still loses events, but the loss is counted
// and surfaced to that subscriber as an EventEventsDropped event once its buffer
// has room again, so downstream protocols can tell "nothing happened" apart from
// "events were lost". Aggregate counts are readable via Dropped.
func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		sub.send(evt)
	}
}

// Dropped reports the cumulative number of events dropped across all
// subscribers because their buffers were full.
func (b *Bus) Dropped() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var total int64
	for _, sub := range b.subs {
		total += sub.total.Load()
	}
	return total
}

func (s *subscriber) send(evt Event) {
	if s.pending.Load() > 0 {
		// Claim the outstanding count with a single atomic swap: a Load followed
		// by Add(-pending) lets concurrent publishers (Publish only holds
		// RLock) each subtract the same value, driving the counter negative so
		// later real drops are never reported, and report the same batch more
		// than once.
		if pending := s.pending.Swap(0); pending > 0 {
			select {
			case s.ch <- droppedEvent(evt, pending):
			default:
				// Still no room: hand the claim back so the batch stays
				// pending, and count this event as dropped too.
				s.pending.Add(pending)
				s.drop()
				return
			}
		}
	}
	select {
	case s.ch <- evt:
	default:
		s.drop()
	}
}

func (s *subscriber) drop() {
	s.pending.Add(1)
	s.total.Add(1)
}

func droppedEvent(evt Event, dropped int64) Event {
	return New(evt.SessionID, EventEventsDropped, evt.Phase, map[string]any{
		"dropped": dropped,
		"reason":  "subscriber buffer full",
	})
}

func newID(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return prefix + "-fallback"
	}
	return prefix + "_" + hex.EncodeToString(buf)
}
