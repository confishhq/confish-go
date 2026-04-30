package confish

import (
	"errors"
	"fmt"
)

// APIError is the base type for every error returned by the SDK that originated
// from a confish API response. Network errors are wrapped in a *NetworkError.
type APIError struct {
	StatusCode int
	Message    string
	Body       []byte
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("confish: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("confish: HTTP %d: %s", e.StatusCode, e.Message)
}

// AuthError indicates the API key is missing or invalid (HTTP 401).
type AuthError struct{ *APIError }

// ForbiddenError indicates the API key doesn't match the environment, the application
// is disabled, or the action does not belong to this environment (HTTP 403).
type ForbiddenError struct{ *APIError }

// ConflictError indicates the action is no longer actionable — typically because it
// has already been acknowledged or has expired (HTTP 409). The action consumer
// silently skips actions that fail to acknowledge with this error.
type ConflictError struct{ *APIError }

// ValidationError indicates the request body failed validation (HTTP 422).
// Errors maps field paths to human-readable error messages, mirroring Laravel's
// validation response shape.
type ValidationError struct {
	*APIError
	Errors map[string][]string
}

// RateLimitError indicates the request was rate-limited (HTTP 429). RetryAfter,
// Limit, and Remaining are populated from the response headers when present.
type RateLimitError struct {
	*APIError
	RetryAfter int // seconds; 0 if header was missing
	Limit      int
	Remaining  int
}

// ServerError indicates a 5xx response from the server.
type ServerError struct{ *APIError }

// NetworkError wraps a transport-level failure (DNS, TCP, TLS, etc.).
type NetworkError struct {
	URL string
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("confish: network error reaching %s: %v", e.URL, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// IsConflict reports whether err is a *ConflictError. Useful when running multiple
// action consumers — an ack conflict means another consumer claimed the action first.
func IsConflict(err error) bool {
	var c *ConflictError
	return errors.As(err, &c)
}
