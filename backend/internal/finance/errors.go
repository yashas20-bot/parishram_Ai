package finance

import "errors"

// Sentinel errors for deterministic financial operations.
var (
	// ErrMissingData indicates that a required financial input or field is absent.
	ErrMissingData = errors.New("missing required financial data")

	// ErrInvalidAmount indicates that a monetary value is negative, zero where not allowed, or out of range.
	ErrInvalidAmount = errors.New("invalid monetary amount")

	// ErrInvalidCurrency indicates that a currency code is not a valid 3-character uppercase ASCII string.
	ErrInvalidCurrency = errors.New("invalid currency code")

	// ErrCurrencyMismatch indicates that two or more participating monetary amounts have different currencies.
	ErrCurrencyMismatch = errors.New("currency mismatch between monetary inputs")

	// ErrOverflow indicates that an integer arithmetic operation exceeded the representable bounds of int64.
	ErrOverflow = errors.New("financial calculation integer overflow")
)
