package runtime

import "errors"

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
