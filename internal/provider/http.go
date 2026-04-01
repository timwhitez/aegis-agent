package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		client = &http.Client{Timeout: 120 * time.Second}
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
	providerName := c.providerName("")
	maxAttempts := c.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
		if err != nil {
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
			err = c.decodeResponse(resp, out, providerName)
		} else {
			err = classifyTransportError(ctx, providerName, err)
		}
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

func (c JSONClient) decodeResponse(resp *http.Response, out any, providerName string) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
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
