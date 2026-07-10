package runtime

import (
	"errors"
	"fmt"
	"strings"
)

type ConfigError struct {
	Err error
}

func (e *ConfigError) Error() string {
	return e.Err.Error()
}

func (e *ConfigError) Unwrap() error {
	return e.Err
}

type ProviderError struct {
	Err error
}

func (e *ProviderError) Error() string {
	return e.Err.Error()
}

func (e *ProviderError) Unwrap() error {
	return e.Err
}

func WrapConfigError(err error) error {
	if err == nil {
		return nil
	}
	var target *ConfigError
	if errors.As(err, &target) {
		return err
	}
	return &ConfigError{Err: err}
}

func WrapProviderError(err error) error {
	if err == nil {
		return nil
	}
	var target *ProviderError
	if errors.As(err, &target) {
		return err
	}
	return &ProviderError{Err: err}
}

type SessionNotResumableError struct {
	SessionID string
	Status    string
	Detail    string
	Action    string
}

func (e *SessionNotResumableError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = fmt.Sprintf("session %s cannot continue from status %s", e.SessionID, e.Status)
	}
	if action := strings.TrimSpace(e.Action); action != "" {
		return "session is not resumable: " + detail + "; " + action
	}
	return "session is not resumable: " + detail
}
