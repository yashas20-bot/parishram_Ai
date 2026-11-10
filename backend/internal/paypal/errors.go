package paypal

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidConfig indicates missing or malformed PayPal configuration.
	ErrInvalidConfig = errors.New("invalid paypal configuration")

	// ErrAuthFailed indicates failure to authenticate with PayPal OAuth 2.0.
	ErrAuthFailed = errors.New("paypal authentication failed")

	// ErrAPIFailure indicates an unexpected non-2xx HTTP status from PayPal.
	ErrAPIFailure = errors.New("paypal api returned an error")

	// ErrTimeout indicates a network or context deadline exceeded during PayPal communication.
	ErrTimeout = errors.New("paypal request timed out")
)

// APIError wraps a non-2xx response from PayPal without exposing sensitive authorization details.
type APIError struct {
	StatusCode int    `json:"status_code"`
	Name       string `json:"name,omitempty"`
	Message    string `json:"message,omitempty"`
	DebugID    string `json:"debug_id,omitempty"`
}

func (e *APIError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("paypal api error (status %d, %s: %s, debug_id: %s)", e.StatusCode, e.Name, e.Message, e.DebugID)
	}
	return fmt.Sprintf("paypal api error (status %d: %s, debug_id: %s)", e.StatusCode, e.Message, e.DebugID)
}

func (e *APIError) Is(target error) bool {
	return target == ErrAPIFailure
}
