package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type JSONClient struct {
	Client   *http.Client
	BaseURL  string
	Provider string
	Retry    RetryConfig
}

const maxProviderResponseBytes = 16 << 20

var errProviderResponseTooLarge = errors.New("provider response body exceeds maximum size")

func (c JSONClient) DoJSON(ctx context.Context, method, path string, headers map[string]string, body any, out any, emit EmitFunc) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := providerHTTPClient(c.Client)
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
	providerName := c.providerName("")
	maxAttempts := c.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptCtx := ctx
		cancelAttempt := func() {}
		if c.Retry.RequestTimeout > 0 {
			attemptCtx, cancelAttempt = context.WithTimeout(ctx, c.Retry.RequestTimeout)
		}
		req, err := http.NewRequestWithContext(attemptCtx, method, endpoint, bytes.NewReader(payload))
		if err != nil {
			cancelAttempt()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		if req.URL != nil && req.URL.Host != "" {
			providerName = c.providerName(req.URL.Host)
		}
		resp, err := client.Do(req)
		if err == nil {
			err = c.decodeResponse(attemptCtx, resp, out, providerName)
		} else {
			err = classifyTransportError(attemptCtx, providerName, err)
		}
		if err != nil && ctx != nil && ctx.Err() != nil {
			// The caller-owned context is the authoritative cancellation
			// boundary. A per-attempt timeout is a provider failure, but a
			// parent cancellation must not be rewritten as upstream_timeout or
			// retried by the transport layer.
			err = ctx.Err()
		}
		cancelAttempt()
		if err == nil {
			return nil
		}
		if attempt == maxAttempts || !shouldRetry(err, c.Retry) {
			return err
		}
		delay := retryDelay(c.Retry.BaseDelay, attempt)
		delay, admissionErr := admitRetryAfter(ctx, err, delay)
		if admissionErr != nil {
			return admissionErr
		}
		data := map[string]any{
			"provider":     providerName,
			"attempt":      attempt,
			"next_attempt": attempt + 1,
			"max_attempts": maxAttempts,
			"delay_ms":     delay.Milliseconds(),
			"error":        err.Error(),
		}
		var httpErr *HTTPError
		if errors.As(err, &httpErr) {
			data["class"] = httpErr.Class
			if httpErr.StatusCode > 0 {
				data["status_code"] = httpErr.StatusCode
			}
			if strings.TrimSpace(httpErr.TimeoutKind) != "" {
				data["timeout_kind"] = httpErr.TimeoutKind
			}
		}
		emitEvent(emit, "provider.retry", data)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (c JSONClient) providerName(fallback string) string {
	if strings.TrimSpace(c.Provider) != "" {
		return c.Provider
	}
	return fallback
}

func (c JSONClient) decodeResponse(ctx context.Context, resp *http.Response, out any, providerName string) error {
	defer resp.Body.Close()
	// Never return a redirect body or Location in errors: either can contain
	// credentials. Returning the original response also prevents net/http from
	// copying custom API-key headers or replaying POST bodies to a new endpoint.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return &HTTPError{
			Provider:   providerName,
			Class:      "invalid_request",
			Message:    "provider API redirects are disabled; configure the canonical endpoint",
			StatusCode: resp.StatusCode,
		}
	}
	data, err := readAllWithIdleTimeout(ctx, resp.Body, c.Retry.StreamIdleTimeout)
	if err != nil {
		if errors.Is(err, errProviderResponseTooLarge) {
			return &HTTPError{
				Provider:   providerName,
				Class:      "response_parse_error",
				Message:    fmt.Sprintf("%s (%d bytes)", errProviderResponseTooLarge.Error(), maxProviderResponseBytes),
				StatusCode: resp.StatusCode,
			}
		}
		if errors.Is(err, errStreamIdleTimeout) {
			return &HTTPError{
				Provider:    providerName,
				Class:       "upstream_timeout",
				Message:     err.Error(),
				StatusCode:  resp.StatusCode,
				TimeoutKind: "stream_idle_timeout",
			}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return &HTTPError{
				Provider:    providerName,
				Class:       "upstream_timeout",
				Message:     strings.TrimSpace(err.Error()),
				StatusCode:  resp.StatusCode,
				TimeoutKind: "request_timeout",
			}
		}
		// Remaining body-read failures are transport faults that surfaced after
		// the response headers arrived (mid-body connection reset, unexpected
		// EOF, ...). They must be classified like client.Do failures so
		// shouldRetry/RetryTransport can act on them and durable provider
		// attempts record a non-empty error_class.
		return classifyTransportError(ctx, providerName, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := classifyHTTPError(providerName, resp.StatusCode, string(data))
		var httpErr *HTTPError
		if errors.As(err, &httpErr) {
			httpErr.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &HTTPError{
			Provider:   providerName,
			Class:      "response_parse_error",
			Message:    err.Error(),
			StatusCode: resp.StatusCode,
		}
	}
	return nil
}

var errStreamIdleTimeout = errors.New("stream idle timeout")

// maxRetryAfterDelay is the automatic wait admission budget. Longer upstream
// waits stop automatic retry instead of being shortened to this budget.
const maxRetryAfterDelay = 30 * time.Second

// maxRetryAfterJitter bounds the random spread layered on top of an upstream
// Retry-After floor, so the worst-case wait stays within a documented
// maxRetryAfterDelay + maxRetryAfterJitter window.
const maxRetryAfterJitter = maxRetryAfterDelay / 4

// admitRetryAfter keeps parsing separate from the local interactive wait policy.
// It runs only after the status and remaining attempts permit a retry.
func admitRetryAfter(ctx context.Context, err error, local time.Duration) (time.Duration, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.RetryAfter <= 0 {
		return local, nil
	}
	wait := httpErr.RetryAfter
	if wait > maxRetryAfterDelay {
		label := ""
		if wait == time.Duration(1<<63-1) {
			label = " (saturated duration maximum)"
		}
		return 0, fmt.Errorf("automatic retry not scheduled: Retry-After wait=%s%s exceeds automatic wait budget=%s: %w", wait, label, maxRetryAfterDelay, err)
	}
	delay := retryAfterDelay(wait, local)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if delay >= remaining {
			return 0, fmt.Errorf("automatic retry not scheduled: Retry-After wait=%s, planned wait=%s exceeds caller deadline budget=%s: %w", wait, delay, remaining, err)
		}
	}
	return delay, nil
}

// retryAfterDelay combines an upstream Retry-After wait with the locally
// jittered backoff. Our policy treats the server wait as a floor and adds a
// bounded random spread to avoid synchronising concurrent callers. Admitted
// server waits stay inside maxRetryAfterDelay + maxRetryAfterJitter unless the
// local backoff is larger.
func retryAfterDelay(retryAfter, jittered time.Duration) time.Duration {
	if retryAfter <= 0 {
		return jittered
	}
	// Sample the spread instead of clamping the already-jittered local delay:
	// min(jittered, maxRetryAfterJitter) collapses to exactly maxRetryAfterJitter
	// as soon as the local backoff ceiling grows past it (attempt >= 4 at the
	// default base), which degrades the "bounded random spread" into the constant
	// retryAfter + maxRetryAfterJitter. Since retryAfter is the same server wait
	// for every concurrent agent, that constant recreates the self-synchronised
	// spike this function exists to break up — precisely on the last attempts,
	// when the upstream overload signal is strongest. Drawing from (0, bound]
	// keeps a random component at every attempt while still scaling the spread
	// with the local backoff.
	spread := time.Duration(0)
	if bound := min(jittered, maxRetryAfterJitter); bound > 0 {
		spread = time.Duration(rand.Int63n(int64(bound) + 1))
	}
	spread = min(spread, time.Duration(1<<63-1)-retryAfter)
	return max(retryAfter+spread, jittered)
}

// parseRetryAfter reads the two RFC 9110 Retry-After forms — delta-seconds and
// HTTP-date — and returns 0 when the header is absent, malformed or already in
// the past. Unrepresentable positive waits saturate at the duration maximum;
// admission decides whether to wait, without shortening the parsed value.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		digits := strings.TrimLeft(value, "0")
		if digits == "" {
			return 0
		}
		seconds, err := strconv.ParseUint(digits, 10, 64)
		// Check before multiplication, including valid decimals beyond uint64.
		if err != nil || seconds > uint64(time.Duration(1<<63-1)/time.Second) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	deadline, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	wait := deadline.Sub(now)
	if wait <= 0 {
		return 0
	}
	return wait
}

func readAllWithIdleTimeout(ctx context.Context, body io.ReadCloser, idle time.Duration) ([]byte, error) {
	if idle <= 0 {
		return readAllLimited(body, maxProviderResponseBytes)
	}
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		type readResult struct {
			n   int
			err error
		}
		readDone := make(chan readResult, 1)
		go func() {
			n, err := body.Read(buf)
			readDone <- readResult{n: n, err: err}
		}()
		timer := time.NewTimer(idle)
		select {
		case result := <-readDone:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if result.n > 0 {
				if out.Len()+result.n > maxProviderResponseBytes {
					_ = body.Close()
					return nil, errProviderResponseTooLarge
				}
				out.Write(buf[:result.n])
			}
			if errors.Is(result.err, io.EOF) {
				return out.Bytes(), nil
			}
			if result.err != nil {
				return nil, result.err
			}
		case <-timer.C:
			_ = body.Close()
			return nil, fmt.Errorf("%w after %s without response bytes", errStreamIdleTimeout, idle)
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = body.Close()
			return nil, ctx.Err()
		}
	}
}

func readAllLimited(reader io.Reader, limit int) ([]byte, error) {
	var out bytes.Buffer
	limited := io.LimitReader(reader, int64(limit)+1)
	if _, err := out.ReadFrom(limited); err != nil {
		return nil, err
	}
	if out.Len() > limit {
		return nil, errProviderResponseTooLarge
	}
	return out.Bytes(), nil
}
