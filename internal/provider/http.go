package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type JSONClient struct {
	Client   *http.Client
	BaseURL  string
	Provider string
	Retry    RetryConfig
}

func (c JSONClient) DoJSON(ctx context.Context, method, path string, headers map[string]string, body any, out any, emit EmitFunc) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := c.Client
	if client == nil {
		client = &http.Client{}
	}
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
		cancelAttempt()
		if err == nil {
			return nil
		}
		if attempt == maxAttempts || !shouldRetry(err, c.Retry) {
			return err
		}
		delay := retryDelay(c.Retry.BaseDelay, attempt)
		if emit != nil {
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
			emit("provider.retry", data)
		}
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
	data, err := readAllWithIdleTimeout(ctx, resp.Body, c.Retry.StreamIdleTimeout)
	if err != nil {
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
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyHTTPError(providerName, resp.StatusCode, string(data))
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

func readAllWithIdleTimeout(ctx context.Context, body io.ReadCloser, idle time.Duration) ([]byte, error) {
	if idle <= 0 {
		return io.ReadAll(body)
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
