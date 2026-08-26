package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"aegis-agent/internal/session"
)

type ToolSchema struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type TurnRequest struct {
	SessionID        string
	RequestID        string
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
	Reported                 bool `json:"reported"`
	InputTokens              int  `json:"input_tokens,omitempty"`
	OutputTokens             int  `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int  `json:"cache_read_input_tokens,omitempty"`
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

func providerStopReasonIsCancelled(value string) bool {
	value = strings.TrimSpace(value)
	return strings.EqualFold(value, "cancel") || strings.EqualFold(value, "cancelled")
}

type EmitFunc func(eventType string, data map[string]any)

func emitEvent(emit EmitFunc, eventType string, data map[string]any) {
	if emit != nil {
		emit(eventType, data)
	}
}

type Adapter interface {
	Name() string
	RunTurn(context.Context, TurnRequest, EmitFunc) (TurnResult, error)
}

const wireRequestEstimateSchemaVersion = 1

type WireRequestEstimate struct {
	SchemaVersion        int `json:"schema_version"`
	WireBodyBytes        int `json:"wire_body_bytes"`
	EstimatedInputTokens int `json:"estimated_input_tokens"`
	SystemChars          int `json:"system_chars"`
	MessageCount         int `json:"message_count"`
	MessagesBytes        int `json:"messages_bytes"`
	ToolCount            int `json:"tool_count"`
	ToolSchemaBytes      int `json:"tool_schema_bytes"`
	MetadataKeyCount     int `json:"metadata_key_count"`
	MetadataBytes        int `json:"metadata_bytes"`
}

type RequestEstimator interface {
	EstimateRequest(TurnRequest) (WireRequestEstimate, error)
}

var ErrRequestEstimatorUnavailable = errors.New("provider request estimator unavailable")

func EstimateAdapterRequest(adapter Adapter, req TurnRequest) (WireRequestEstimate, error) {
	estimator, ok := adapter.(RequestEstimator)
	if !ok {
		return WireRequestEstimate{}, fmt.Errorf("%w: %T", ErrRequestEstimatorUnavailable, adapter)
	}
	return estimator.EstimateRequest(req)
}

func EstimateWireRequest(body any, req TurnRequest) (WireRequestEstimate, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return WireRequestEstimate{}, err
	}
	messages, err := json.Marshal(req.Messages)
	if err != nil {
		return WireRequestEstimate{}, err
	}
	toolSchemas, err := json.Marshal(req.Tools)
	if err != nil {
		return WireRequestEstimate{}, err
	}
	metadata, err := json.Marshal(req.Metadata)
	if err != nil {
		return WireRequestEstimate{}, err
	}
	return WireRequestEstimate{
		SchemaVersion:        wireRequestEstimateSchemaVersion,
		WireBodyBytes:        len(payload),
		EstimatedInputTokens: (len(payload) + 3) / 4,
		SystemChars:          len(req.SystemPrompt),
		MessageCount:         len(req.Messages),
		MessagesBytes:        len(messages),
		ToolCount:            len(req.Tools),
		ToolSchemaBytes:      len(toolSchemas),
		MetadataKeyCount:     len(req.Metadata),
		MetadataBytes:        len(metadata),
	}, nil
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
	// RetryAfter carries the wait requested by the upstream Retry-After
	// response header (0 when absent or unparseable). It is optional: only
	// status-bearing responses populate it, and the retry loop in http.go
	// treats it as a floor for the local backoff.
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}

// maxHTTPErrorMessageBytes bounds the provider response body kept in
// HTTPError.Message. The raw body may be up to maxProviderResponseBytes (16 MiB,
// http.go), and the message flows verbatim into durable JSONL records
// (events.jsonl, provider-attempts.jsonl) and state.json whose per-record read
// limit is also 16 MiB (fileutil.MaxRegularFileReadBytes) — an oversized single
// record makes those files unreadable and un-appendable. A few KiB is enough to
// diagnose while staying far below that limit.
const maxHTTPErrorMessageBytes = 8 << 10

// truncateErrorMessage keeps at most maxHTTPErrorMessageBytes of message and
// records the original size when it had to cut.
func truncateErrorMessage(message string) string {
	if len(message) <= maxHTTPErrorMessageBytes {
		return message
	}
	// The byte cap can land mid-rune (CJK / emoji error bodies), so back off to
	// the nearest rune boundary. Otherwise the retained prefix ends with a
	// partial rune and encoding/json silently rewrites it to U+FFFD when the
	// message is persisted to events.jsonl / provider-attempts.jsonl, leaving a
	// record that no longer matches the upstream bytes.
	cut := maxHTTPErrorMessageBytes
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return fmt.Sprintf("%s...(truncated, total %d bytes)", message[:cut], len(message))
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
		Message:    truncateErrorMessage(strings.TrimSpace(message)),
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

// retryDelayCeiling is the deterministic exponential backoff bound for a given
// attempt: base * 2^(attempt-1), capped at 30s.
func retryDelayCeiling(base time.Duration, attempt int) time.Duration {
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

func retryDelay(base time.Duration, attempt int) time.Duration {
	ceiling := retryDelayCeiling(base, attempt)
	if ceiling <= 0 {
		return ceiling
	}
	// Full jitter: sample uniformly from (0, ceiling]. Without jitter concurrent
	// agents (child / queue profiles) re-send at the identical 1s/2s/4s instants
	// and turn an upstream 5xx or rate limit into a self-synchronised retry
	// spike. math/rand is sufficient here: this only spreads load and is not a
	// security primitive.
	return time.Duration(rand.Int63n(int64(ceiling)) + 1)
}
