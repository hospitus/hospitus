package provider

import (
	"errors"
	"fmt"
)

// ProviderError wraps errors with provider context
type ProviderError struct {
	Provider   string
	Operation  string
	InstanceID string
	Err        error
}

func (e *ProviderError) Error() string {
	if e.InstanceID != "" {
		return fmt.Sprintf("provider %s: operation %s on instance %s failed: %v",
			e.Provider, e.Operation, e.InstanceID, e.Err)
	}
	return fmt.Sprintf("provider %s: operation %s failed: %v",
		e.Provider, e.Operation, e.Err)
}

func (e *ProviderError) Unwrap() error {
	return e.Err
}

// NewProviderError creates a new ProviderError
func NewProviderError(provider, operation, instanceID string, err error) *ProviderError {
	return &ProviderError{
		Provider:   provider,
		Operation:  operation,
		InstanceID: instanceID,
		Err:        err,
	}
}

// WrapError attaches provider context to err and returns it as a
// *ProviderError, so callers can tell a failure of the operation they asked for
// from an internal fault. A nil error stays nil, and an error that already
// carries provider context is returned unchanged.
//
// Providers apply it once per Provider-interface method, as a deferred wrapper
// on the named result, rather than at every return: the API layer decides what
// a client may see from the error's type, and a method that forgets to wrap
// silently withholds the reason an operation failed.
func WrapError(providerName, operation, instanceID string, err error) error {
	if err == nil {
		return nil
	}

	var alreadyWrapped *ProviderError
	if errors.As(err, &alreadyWrapped) {
		return err
	}

	return NewProviderError(providerName, operation, instanceID, err)
}

// Common errors
var (
	ErrInstanceNotFound     = errors.New("instance not found")
	ErrInstanceExists       = errors.New("instance already exists")
	ErrUnsupportedOperation = errors.New("operation not supported")
	ErrInvalidState         = errors.New("invalid instance state")
	ErrProviderNotAvailable = errors.New("provider not available")
)
