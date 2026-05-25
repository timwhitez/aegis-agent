package runtime

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
)

func recordProviderRetry(store *session.Store, meta session.SessionMetadata, turn int, data map[string]any) error {
	attempt := baseProviderAttempt(meta, turn)
	attempt.Outcome = "retry"
	attempt.Retryable = true
	attempt.Attempt = intFromAny(data["attempt"])
	attempt.BackoffMS = int64FromAny(firstAny(data["delay_ms"], data["backoff_ms"]))
	attempt.Error = stringFromAny(data["error"])
	attempt.ErrorClass = stringFromAny(data["class"])
	attempt.TimeoutKind = stringFromAny(data["timeout_kind"])
	attempt.StatusCode = intFromAny(data["status_code"])
	return store.AppendProviderAttempt(meta.ID, attempt)
}

func recordProviderFailure(store *session.Store, meta session.SessionMetadata, turn int, err error, responseCommitted bool) error {
	if err == nil {
		return nil
	}
	attempt := baseProviderAttempt(meta, turn)
	attempt.Outcome = "failure"
	attempt.Attempt = terminalProviderAttempt(store, meta.ID, turn)
	attempt.ResponseCommitted = responseCommitted
	attempt.Error = err.Error()
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		attempt.ErrorClass = httpErr.Class
		attempt.TimeoutKind = httpErr.TimeoutKind
		attempt.StatusCode = httpErr.StatusCode
	}
	return store.AppendProviderAttempt(meta.ID, attempt)
}

func recordProviderSuccess(store *session.Store, meta session.SessionMetadata, turn int, result provider.TurnResult) error {
	attempt := baseProviderAttempt(meta, turn)
	attempt.Outcome = "success"
	attempt.Attempt = terminalProviderAttempt(store, meta.ID, turn)
	attempt.ResponseCommitted = true
	attempt.ProviderResponseID = result.ProviderResponseID
	attempt.CacheCreationInputTokens = result.Usage.CacheCreationInputTokens
	attempt.CacheReadInputTokens = result.Usage.CacheReadInputTokens
	return store.AppendProviderAttempt(meta.ID, attempt)
}

func terminalProviderAttempt(store *session.Store, sessionID string, turn int) int {
	attempts, err := store.LoadProviderAttempts(sessionID)
	if err != nil {
		return 1
	}
	maxAttempt := 0
	for _, attempt := range attempts {
		if attempt.Turn != turn || attempt.Outcome != "retry" {
			continue
		}
		if attempt.Attempt > maxAttempt {
			maxAttempt = attempt.Attempt
		}
	}
	return maxAttempt + 1
}

func recordProviderAutoResumeAttempt(store *session.Store, meta session.SessionMetadata, turn int, err error, attemptNo int) error {
	attempt := baseProviderAttempt(meta, turn)
	attempt.Outcome = "auto_resume"
	attempt.Retryable = true
	attempt.Attempt = attemptNo
	attempt.Error = err.Error()
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		attempt.ErrorClass = httpErr.Class
		attempt.TimeoutKind = httpErr.TimeoutKind
		attempt.StatusCode = httpErr.StatusCode
	}
	return store.AppendProviderAttempt(meta.ID, attempt)
}

func baseProviderAttempt(meta session.SessionMetadata, turn int) session.ProviderAttempt {
	attempt := session.ProviderAttempt{
		Turn:      turn,
		Provider:  meta.Provider,
		Model:     meta.Model,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if meta.ProviderOptions.TimeoutPolicy != nil {
		attempt.RequestTimeoutSec = meta.ProviderOptions.TimeoutPolicy.RequestTimeoutSec
		attempt.StreamIdleTimeoutMS = meta.ProviderOptions.TimeoutPolicy.StreamIdleTimeoutMS
	}
	return attempt
}

func firstAny(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		n, _ := strconv.Atoi(string(typed))
		return n
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(typed))
		return n
	default:
		return 0
	}
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		n, _ := strconv.ParseInt(string(typed), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return n
	default:
		return 0
	}
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}
