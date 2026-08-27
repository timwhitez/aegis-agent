package events

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

// newTestSubscriber builds a subscriber directly because Bus.Subscribe only
// hands back the read side of the channel, while the drop accounting under test
// lives on the subscriber itself.
func newTestSubscriber(buffer int) *subscriber {
	return &subscriber{ch: make(chan Event, buffer)}
}

func probeEvent() Event {
	return New("sess-test", "unit.probe", "test", nil)
}

// drainSubscriber empties the subscriber channel without blocking and splits
// what it saw into drop reports (with the counts they carried) and plain events.
func drainSubscriber(t *testing.T, s *subscriber) (reports int, reported int64, plain int) {
	t.Helper()
	for {
		select {
		case evt := <-s.ch:
			if evt.Type != EventEventsDropped {
				plain++
				continue
			}
			reports++
			count, ok := evt.Data["dropped"].(int64)
			if !ok {
				t.Fatalf("dropped report carries %T, want int64: %#v", evt.Data["dropped"], evt.Data)
			}
			reported += count
		default:
			return reports, reported, plain
		}
	}
}

// TestSubscriberDropClaimIsAtomicUnderConcurrentSend pins the invariant that a
// pending drop batch is claimed exactly once. Publish only holds an RLock, so
// several publishers can run send concurrently on the same subscriber; reading
// pending and subtracting it in two steps lets each of them report the same
// batch and subtract it again, driving the counter negative.
func TestSubscriberDropClaimIsAtomicUnderConcurrentSend(t *testing.T) {
	const (
		rounds      = 200
		publishers  = 4
		outstanding = int64(3)
	)
	for round := 0; round < rounds; round++ {
		// Buffer is large enough that no send in this round can drop, so every
		// count observed afterwards comes from the pre-seeded batch alone.
		sub := newTestSubscriber(16)
		sub.pending.Store(outstanding)
		sub.total.Store(outstanding)

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < publishers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				sub.send(probeEvent())
			}()
		}
		close(start)
		wg.Wait()

		if got := sub.pending.Load(); got < 0 {
			t.Fatalf("round %d: pending went negative (%d); real drops would never be reported again", round, got)
		}
		reports, reported, plain := drainSubscriber(t, sub)
		if reports != 1 {
			t.Fatalf("round %d: got %d drop reports totalling %d, want the batch reported exactly once", round, reports, reported)
		}
		if reported != outstanding {
			t.Fatalf("round %d: reported %d drops, want %d", round, reported, outstanding)
		}
		if plain != publishers {
			t.Fatalf("round %d: got %d plain events, want %d", round, plain, publishers)
		}
		if got := sub.pending.Load(); got != 0 {
			t.Fatalf("round %d: pending is %d after the batch was reported, want 0", round, got)
		}
		if reported+sub.pending.Load() != sub.total.Load() {
			t.Fatalf("round %d: accounting broken: reported %d + pending %d != total %d", round, reported, sub.pending.Load(), sub.total.Load())
		}
	}
}

// TestSubscriberReportsDrainedBatchExactlyOnce reproduces the user-visible
// sequence: a subscriber falls behind, loses a known number of events, catches
// up, and then several publishers hit it at once. The lost batch must surface
// once with the exact count, not once per publisher.
func TestSubscriberReportsDrainedBatchExactlyOnce(t *testing.T) {
	const (
		rounds     = 200
		buffer     = 8
		drops      = int64(5)
		publishers = 4
	)
	for round := 0; round < rounds; round++ {
		sub := newTestSubscriber(buffer)
		for i := 0; i < buffer; i++ {
			sub.send(probeEvent())
		}
		for i := int64(0); i < drops; i++ {
			sub.send(probeEvent())
		}
		if got := sub.pending.Load(); got != drops {
			t.Fatalf("round %d: setup expected %d pending drops, got %d", round, drops, got)
		}
		if _, reported, _ := drainSubscriber(t, sub); reported != 0 {
			t.Fatalf("round %d: setup reported %d drops before the buffer drained", round, reported)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < publishers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				sub.send(probeEvent())
			}()
		}
		close(start)
		wg.Wait()

		reports, reported, plain := drainSubscriber(t, sub)
		if reports != 1 || reported != drops {
			t.Fatalf("round %d: got %d drop reports totalling %d, want 1 report of %d", round, reports, reported, drops)
		}
		if plain != publishers {
			t.Fatalf("round %d: got %d plain events, want %d", round, plain, publishers)
		}
		if got := sub.pending.Load(); got != 0 {
			t.Fatalf("round %d: pending is %d after the batch was reported, want 0", round, got)
		}
		if got := sub.total.Load(); got != drops {
			t.Fatalf("round %d: cumulative total is %d, want %d", round, got, drops)
		}
	}
}

// TestSubscriberPendingReturnedWhenReportCannotBeEnqueued covers the
// compensation path: if the buffer is still full when the claimed batch is about
// to be reported, the claim must be handed back instead of vanishing.
func TestSubscriberPendingReturnedWhenReportCannotBeEnqueued(t *testing.T) {
	sub := newTestSubscriber(1)
	sub.ch <- probeEvent() // buffer is now full
	sub.pending.Store(3)
	sub.total.Store(3)

	sub.send(probeEvent())

	if got := sub.pending.Load(); got != 4 {
		t.Fatalf("pending is %d, want 4 (3 handed back + 1 newly dropped)", got)
	}
	if got := sub.total.Load(); got != 4 {
		t.Fatalf("total is %d, want 4", got)
	}
	reports, reported, plain := drainSubscriber(t, sub)
	if reports != 0 || reported != 0 {
		t.Fatalf("got %d drop reports totalling %d, want none while the buffer is full", reports, reported)
	}
	if plain != 1 {
		t.Fatalf("got %d plain events, want the single pre-filled one", plain)
	}
}

// TestSubscriberDropAccountingHoldsUnderSlowConsumer runs many publishers
// against one deliberately slow consumer and checks that the counters stay
// coherent: pending never dips below zero and reported + pending == total.
// The consumer drains in bursts rather than steadily, because it is the moment
// right after a burst - buffer empty, a batch still pending, several publishers
// arriving at once - that lets a non-atomic claim be taken twice.
func TestSubscriberDropAccountingHoldsUnderSlowConsumer(t *testing.T) {
	const (
		publishers   = 8
		perPublisher = 500
	)
	bus := NewBus()
	ch := bus.Subscribe(4)
	bus.mu.RLock()
	sub := bus.subs[0]
	bus.mu.RUnlock()

	// Watcher samples the counter while publishers race, so a transient dip
	// below zero is caught even if later drops mask it.
	stopWatch := make(chan struct{})
	watched := make(chan int64, 1)
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		lowest := int64(0)
		for {
			select {
			case <-stopWatch:
				watched <- lowest
				return
			default:
				if got := sub.pending.Load(); got < lowest {
					lowest = got
				}
				runtime.Gosched()
			}
		}
	}()

	stopConsume := make(chan struct{})
	consumed := make(chan struct{})
	var reports int
	var reported int64
	go func() {
		defer close(consumed)
		count := func(evt Event) {
			if evt.Type != EventEventsDropped {
				return
			}
			reports++
			reported += evt.Data["dropped"].(int64)
		}
		for {
			select {
			case evt := <-ch:
				count(evt)
				// Drain the rest of the buffer, then idle: publishers that
				// arrive during the idle window all see the same pending batch
				// with room in the buffer to report it.
				for draining := true; draining; {
					select {
					case more := <-ch:
						count(more)
					default:
						draining = false
					}
				}
				time.Sleep(50 * time.Microsecond) // consumer cannot keep up
			case <-stopConsume:
				for {
					select {
					case evt := <-ch:
						count(evt)
					default:
						return
					}
				}
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perPublisher; j++ {
				bus.Publish(probeEvent())
			}
		}()
	}
	wg.Wait()

	close(stopWatch)
	watcher.Wait()
	close(stopConsume)
	<-consumed

	if lowest := <-watched; lowest < 0 {
		t.Fatalf("pending reached %d during the run; real drops would stop being reported", lowest)
	}
	if got := sub.pending.Load(); got < 0 {
		t.Fatalf("pending is %d after the run, want >= 0", got)
	}
	total := sub.total.Load()
	if total == 0 {
		t.Fatal("no events were dropped; the test no longer exercises the drop path")
	}
	if got := bus.Dropped(); got != total {
		t.Fatalf("Bus.Dropped reported %d, want %d", got, total)
	}
	if reported > total {
		t.Fatalf("reported %d drops but only %d happened; batches were reported more than once", reported, total)
	}
	if reported+sub.pending.Load() != total {
		t.Fatalf("accounting broken: reported %d + pending %d != total %d", reported, sub.pending.Load(), total)
	}
	if reports == 0 {
		t.Fatal("drops happened but none were ever reported to the subscriber")
	}
}

// TestPublishNeverBlocksOnFullSubscriberBuffer guards the default observer
// contract that Publish never blocks the producer, including for a subscriber
// that never reads at all.
func TestPublishNeverBlocksOnFullSubscriberBuffer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		buffer int
	}{
		{name: "unbuffered", buffer: 0},
		{name: "tiny buffer", buffer: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := NewBus()
			bus.Subscribe(tc.buffer) // nobody ever reads this channel

			done := make(chan struct{})
			go func() {
				defer close(done)
				for i := 0; i < 1000; i++ {
					bus.Publish(probeEvent())
				}
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Publish blocked on a subscriber that never reads")
			}
			if got := bus.Dropped(); got == 0 {
				t.Fatalf("Bus.Dropped is %d, want > 0 for a subscriber that never reads", got)
			}
		})
	}
}

func TestLosslessSubscriberBackpressuresPublisherWithoutDropping(t *testing.T) {
	bus := NewBus()
	sub := bus.SubscribeLossless(1)
	first := New("sess-lossless", "first", "test", nil)
	second := New("sess-lossless", "second", "test", nil)
	bus.Publish(first)

	published := make(chan struct{})
	go func() {
		bus.Publish(second)
		close(published)
	}()

	select {
	case <-published:
		t.Fatal("lossless publisher completed while its subscriber buffer was full")
	case <-time.After(50 * time.Millisecond):
	}
	if got := bus.Dropped(); got != 0 {
		t.Fatalf("lossless subscription reported %d dropped events, want 0", got)
	}
	if got := <-sub; got.ID != first.ID {
		t.Fatalf("first delivery ID=%q, want %q", got.ID, first.ID)
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("lossless publisher did not resume after subscriber drained")
	}
	if got := <-sub; got.ID != second.ID {
		t.Fatalf("second delivery ID=%q, want %q", got.ID, second.ID)
	}
}
