package provider

import (
	"net/http"
	"testing"
	"time"
)

// retryAfterDelay must keep a random spread at every attempt. The spread used to
// be min(jittered, maxRetryAfterJitter), which collapses to the constant
// maxRetryAfterJitter once the local backoff ceiling grows past it (attempt >= 4
// at the default base), making the total wait the deterministic
// maxRetryAfterDelay + maxRetryAfterJitter for every concurrent agent.
func TestRetryAfterDelayKeepsJitterAtEveryAttempt(t *testing.T) {
	const samples = 20000
	upper := maxRetryAfterDelay + maxRetryAfterJitter
	for _, base := range []time.Duration{time.Second, 1500 * time.Millisecond} {
		for attempt := 1; attempt <= 5; attempt++ {
			seen := make(map[time.Duration]struct{}, samples)
			upperHits := 0
			for i := 0; i < samples; i++ {
				delay := retryAfterDelay(maxRetryAfterDelay, retryDelay(base, attempt))
				if delay < maxRetryAfterDelay {
					t.Fatalf("base=%v attempt=%d: delay %v below Retry-After floor %v", base, attempt, delay, maxRetryAfterDelay)
				}
				if delay > upper {
					t.Fatalf("base=%v attempt=%d: delay %v above documented bound %v", base, attempt, delay, upper)
				}
				if delay == upper {
					upperHits++
				}
				seen[delay] = struct{}{}
			}
			// A healthy spread lands on the exact upper bound essentially never
			// (it is one point of a nanosecond-resolution range). The pre-fix
			// implementation hit it 6-69% of the time on attempt 4/5.
			if ratio := float64(upperHits) / float64(samples); ratio > 0.01 {
				t.Fatalf("base=%v attempt=%d: %.2f%% of delays equal the constant upper bound %v; the spread is not random",
					base, attempt, 100*ratio, upper)
			}
			// Full-jitter sampling over a >=1s range must not degenerate to a
			// handful of values.
			if len(seen) < samples*9/10 {
				t.Fatalf("base=%v attempt=%d: only %d distinct delays out of %d samples; the spread is degenerate",
					base, attempt, len(seen), samples)
			}
		}
	}
}

// The upstream Retry-After is a floor: the returned wait must never dip below
// it, no matter how small (or negative) the local jittered backoff is, and the
// local backoff must still win when it is the larger of the two.
func TestRetryAfterDelayKeepsFloorAndLocalBackoff(t *testing.T) {
	tiny := []time.Duration{-time.Second, -1, 0, 1, time.Nanosecond, time.Microsecond, time.Millisecond}
	for _, floor := range []time.Duration{time.Second, 2 * time.Second, maxRetryAfterDelay} {
		for _, jittered := range tiny {
			if got := retryAfterDelay(floor, jittered); got < floor {
				t.Fatalf("retryAfterDelay(%v, %v) = %v, below the floor", floor, jittered, got)
			}
		}
		for i := 0; i < 2000; i++ {
			jittered := retryDelay(time.Second, 3)
			got := retryAfterDelay(floor, jittered)
			if got < floor {
				t.Fatalf("retryAfterDelay(%v, %v) = %v, below the floor", floor, jittered, got)
			}
			if got < jittered {
				t.Fatalf("retryAfterDelay(%v, %v) = %v, below the local backoff", floor, jittered, got)
			}
			if got > floor+maxRetryAfterJitter && got != jittered {
				t.Fatalf("retryAfterDelay(%v, %v) = %v, above floor+maxRetryAfterJitter", floor, jittered, got)
			}
		}
	}
}

// Without a usable Retry-After the local backoff must pass through untouched,
// value for value — the header path must not perturb the no-header path.
func TestRetryAfterDelayWithoutHeaderIsIdentity(t *testing.T) {
	jitters := []time.Duration{-5 * time.Second, -1, 0, 1, time.Nanosecond, 3 * time.Millisecond, time.Second, 7 * time.Second, maxRetryAfterDelay, 90 * time.Second}
	for _, retryAfter := range []time.Duration{-2 * time.Second, -1, 0} {
		for _, jittered := range jitters {
			if got := retryAfterDelay(retryAfter, jittered); got != jittered {
				t.Fatalf("retryAfterDelay(%v, %v) = %v, want %v unchanged", retryAfter, jittered, got, jittered)
			}
		}
	}
}

// parseRetryAfter boundary behaviour, pinned so the jitter work above cannot
// silently move the clamp or the malformed-header handling.
func TestParseRetryAfterBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"absent", "", 0},
		{"blank", "   ", 0},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"delta seconds", "2", 2 * time.Second},
		{"just above clamp", "31", maxRetryAfterDelay},
		{"far above clamp", "3600", maxRetryAfterDelay},
		{"int64 max", "9223372036854775807", maxRetryAfterDelay},
		{"malformed", "abc", 0},
		{"fractional", "1.5", 0},
		{"http date future", now.Add(5 * time.Second).UTC().Format(http.TimeFormat), 5 * time.Second},
		{"http date past", now.Add(-5 * time.Second).UTC().Format(http.TimeFormat), 0},
		{"http date beyond clamp", now.Add(time.Hour).UTC().Format(http.TimeFormat), maxRetryAfterDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.value, now); got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
