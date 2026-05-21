package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"go-cli-agent/internal/session"
)

type ToolSchema struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type TurnRequest struct {
	SessionID        string
	Model            string
	SystemPrompt     string
	Messages         []session.Message
	Tools            []ToolSchema
	Metadata         map[string]any
	Temperature      *float64
	TopP             *float64
	MaxOutputTokens  int
	ProviderProfile  string
	APIProvider      string
	ReasoningEffort  string
	ReasoningSummary string
	TextVerbosity    string
	ThinkingBudget   int
	IncludeThoughts  *bool
	PromptCache      *bool
	Store            *bool
}

type ToolCall struct {
	ID             string
	Name           string
	Arguments      json.RawMessage
	ProviderCallID string
}

type Usage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type TurnResult struct {
	Text                  string
	Thinking              string
	ProviderContentBlocks []session.ProviderContentBlock
	ToolCalls             []ToolCall
	StopReason            string
	Usage                 Usage
	ProviderResponseID    string
	RawProvider           map[string]any
}

func rawProviderEnvelope(sourceKey, stopReason string, extras map[string]any) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(sourceKey) != "" {
		out["provider_stop_reason_source"] = sourceKey
	}
	if strings.TrimSpace(stopReason) != "" {
		out["provider_stop_reason"] = stopReason
		if strings.TrimSpace(sourceKey) != "" {
			out[sourceKey] = stopReason
		}
	}
	for key, value := range extras {
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) == "" {
				continue
			}
		case nil:
			continue
		}
		out[key] = value
	}
	return out
}

type EmitFunc func(eventType string, data map[string]any)

type Adapter interface {
	Name() string
	RunTurn(context.Context, TurnRequest, EmitFunc) (TurnResult, error)
}

type RetryConfig struct {
	MaxAttempts       int
	BaseDelay         time.Duration
	Retry429          bool
	Retry5xx          bool
	RetryTransport    bool
	RequestTimeout    time.Duration
	StreamIdleTimeout time.Duration
}

type HTTPError struct {
	Provider    string
	Class       string
	Message     string
	StatusCode  int
	TimeoutKind string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}

func classifyHTTPError(provider string, status int, message string) error {
	class := "upstream_unavailable"
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		class = "auth_error"
	case http.StatusTooManyRequests:
		class = "rate_limit"
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		class = "invalid_request"
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		class = "upstream_timeout"
	}
	out := &HTTPError{
		Provider:   provider,
		Class:      class,
		Message:    strings.TrimSpace(message),
		StatusCode: status,
	}
	if class == "upstream_timeout" {
		out.TimeoutKind = "http_timeout_status"
	}
	return out
}

func classifyTransportError(ctx context.Context, provider string, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &HTTPError{
				Provider:    provider,
				Class:       "upstream_timeout",
				Message:     strings.TrimSpace(ctx.Err().Error()),
				TimeoutKind: "request_timeout",
			}
		}
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		timeoutKind := "transport_timeout"
		if strings.Contains(strings.ToLower(err.Error()), "awaiting headers") {
			timeoutKind = "awaiting_headers_timeout"
		}
		return &HTTPError{
			Provider:    provider,
			Class:       "upstream_timeout",
			Message:     strings.TrimSpace(err.Error()),
			TimeoutKind: timeoutKind,
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		class := "upstream_unavailable"
		if netErr.Timeout() {
			class = "upstream_timeout"
		}
		out := &HTTPError{
			Provider: provider,
			Class:    class,
			Message:  strings.TrimSpace(err.Error()),
		}
		if class == "upstream_timeout" {
			out.TimeoutKind = "transport_timeout"
		}
		return out
	}
	return &HTTPError{
		Provider: provider,
		Class:    "upstream_unavailable",
		Message:  strings.TrimSpace(err.Error()),
	}
}

func shouldRetry(err error, cfg RetryConfig) bool {
	if cfg.MaxAttempts <= 1 || err == nil {
		return false
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch {
	case httpErr.Class == "rate_limit":
		return cfg.Retry429
	case httpErr.StatusCode >= http.StatusInternalServerError:
		return cfg.Retry5xx
	case httpErr.Class == "upstream_timeout" || (httpErr.Class == "upstream_unavailable" && httpErr.StatusCode == 0):
		return cfg.RetryTransport
	default:
		return false
	}
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if attempt <= 1 {
		return base
	}
	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= 30*time.Second {
			return 30 * time.Second
		}
		delay *= 2
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}
