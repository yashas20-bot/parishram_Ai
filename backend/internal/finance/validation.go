package finance

import (
	"fmt"
	"strings"
)

// validateCurrency verifies that a currency string is present and consists of exactly 3 uppercase ASCII letters.
func validateCurrency(c string) error {
	trimmed := strings.TrimSpace(c)
	if trimmed == "" {
		return fmt.Errorf("%w: currency is required", ErrMissingData)
	}

	if len(trimmed) != 3 {
		return fmt.Errorf("%w: currency must be exactly 3 characters, got %q", ErrInvalidCurrency, trimmed)
	}

	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if ch < 'A' || ch > 'Z' {
			return fmt.Errorf("%w: currency must be uppercase ASCII letters [A-Z], got %q", ErrInvalidCurrency, trimmed)
		}
	}

	return nil
}

// validateMatchingCurrencies ensures all provided currency strings are valid and identical.
func validateMatchingCurrencies(currencies ...string) error {
	if len(currencies) == 0 {
		return nil
	}

	base := currencies[0]
	if err := validateCurrency(base); err != nil {
		return err
	}

	for i := 1; i < len(currencies); i++ {
		curr := currencies[i]
		if err := validateCurrency(curr); err != nil {
			return err
		}
		if curr != base {
			return fmt.Errorf("%w: expected %s, got %s", ErrCurrencyMismatch, base, curr)
		}
	}

	return nil
}

// validatePositiveAmount ensures an amount is strictly greater than zero.
func validatePositiveAmount(name string, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: %s must be greater than zero, got %d", ErrInvalidAmount, name, amount)
	}
	return nil
}

// validateNonNegativeAmount ensures an amount is non-negative (>= 0).
func validateNonNegativeAmount(name string, amount int64) error {
	if amount < 0 {
		return fmt.Errorf("%w: %s cannot be negative, got %d", ErrInvalidAmount, name, amount)
	}
	return nil
}

// validateBudget validates required budget fields.
func validateBudget(b *BudgetInput) error {
	if b == nil {
		return fmt.Errorf("%w: budget is required", ErrMissingData)
	}
	if err := validateCurrency(b.Currency); err != nil {
		return err
	}
	return validatePositiveAmount("budget limit", b.Limit)
}

// validateSpending validates spending fields, requiring currency even when spent is zero.
func validateSpending(s *SpendingInput) error {
	if s == nil {
		return fmt.Errorf("%w: spending data is required", ErrMissingData)
	}
	if err := validateCurrency(s.Currency); err != nil {
		return err
	}
	return validateNonNegativeAmount("spent amount", s.Spent)
}

// validatePurchase validates proposed purchase inputs.
func validatePurchase(p *PurchaseInput) error {
	if p == nil {
		return fmt.Errorf("%w: purchase data is required", ErrMissingData)
	}
	if err := validateCurrency(p.Currency); err != nil {
		return err
	}
	return validatePositiveAmount("purchase amount", p.Amount)
}

// validateSpendingLimit validates optional spending limit input.
func validateSpendingLimit(sl *SpendingLimitInput) error {
	if sl == nil {
		return nil
	}
	if err := validateCurrency(sl.Currency); err != nil {
		return err
	}
	return validateNonNegativeAmount("spending limit", sl.Limit)
}

// ValidateCurrency is an exported validator for currency strings.
func ValidateCurrency(c string) error {
	return validateCurrency(c)
}

// ValidateMonetaryAmount is an exported validator for monetary amounts.
func ValidateMonetaryAmount(amount int64, allowZero bool) error {
	if allowZero {
		return validateNonNegativeAmount("monetary amount", amount)
	}
	return validatePositiveAmount("monetary amount", amount)
}
