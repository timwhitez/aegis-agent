package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryDelayForErrorRateLimitUsesEqualJitterAndScaledFloor(t *testing.T) {
	rateLimitErr := &HTTPError{
		Provider:   "anthropic",
		Class:      "rate_limit",
		StatusCode: http.StatusTooManyRequests,
		Message:    "user rate limit exceeded (rpm)",
	}
	base := time.Second
	// Attempt 1: effectiveBase=2s -> ceiling=2s -> equal jitter in (1s, 2s].
	// Attempt 3: effectiveBase=2s -> ceiling=8s -> equal jitter in (4s, 8s].
	for _, tc := range []struct {
		attempt int
		minWant time.Duration
		maxWant time.Duration
	}{
		{attempt: 1, minWant: time.Second, maxWant: 2 * time.Second},
		{attempt: 2, minWant: 2 * time.Second, maxWant: 4 * time.Second},
		{attempt: 3, minWant: 4 * time.Second, maxWant: 8 * time.Second},
		{attempt: 4, minWant: 8 * time.Second, maxWant: 16 * time.Second},
	} {
		for sample := 0; sample < 20; sample++ {
			got := retryDelayForError(base, tc.attempt, rateLimitErr)
			if got <= tc.minWant || got > tc.maxWant {
				t.Fatalf("attempt %d: rate_limit delay %s out of expected equal-jitter range (%s, %s]", tc.attempt, got, tc.minWant, tc.maxWant)
			}
		}
	}

	// When Retry-After is already set, fallback stays on standard retryDelay so
	// admitRetryAfter adds spread on top of the server floor cleanly.
	withHeader := &HTTPError{
		Provider:   "anthropic",
		Class:      "rate_limit",
		StatusCode: http.StatusTooManyRequests,
		RetryAfter: 5 * time.Second,
	}
	got := retryDelayForError(base, 1, withHeader)
	if got <= 0 || got > base {
		t.Fatalf("expected standard retryDelay <= %s when Retry-After is present, got %s", base, got)
	}
}
