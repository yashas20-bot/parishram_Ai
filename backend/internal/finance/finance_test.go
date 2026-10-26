package finance

import (
	"errors"
	"math"
	"testing"
)

func TestSentinelErrors_ErrorsIs(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		target   error
		expected bool
	}{
		{"ErrMissingData direct", ErrMissingData, ErrMissingData, true},
		{"ErrInvalidAmount direct", ErrInvalidAmount, ErrInvalidAmount, true},
		{"ErrInvalidCurrency direct", ErrInvalidCurrency, ErrInvalidCurrency, true},
		{"ErrCurrencyMismatch direct", ErrCurrencyMismatch, ErrCurrencyMismatch, true},
		{"ErrOverflow direct", ErrOverflow, ErrOverflow, true},
		{"Mismatched sentinels", ErrInvalidAmount, ErrMissingData, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, tt.target); got != tt.expected {
				t.Errorf("errors.Is(%v, %v) = %v; want %v", tt.err, tt.target, got, tt.expected)
			}
		})
	}
}

func TestCalculateUtilization_Standard(t *testing.T) {
	tests := []struct {
		name     string
		spent    int64
		limit    int64
		expected int64
	}{
		{"Zero spent", 0, 10000, 0},
		{"Quarter spent", 2500, 10000, 25},
		{"Half spent", 5000, 10000, 50},
		{"Full spent", 10000, 10000, 100},
		{"Over budget 150%", 15000, 10000, 150},
		{"Floor rounding 33%", 1, 3, 33},
		{"Floor rounding 66%", 2, 3, 66},
		{"Prime limit floor", 777, 1000, 77},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CalculateUtilization(tt.spent, tt.limit)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("CalculateUtilization(%d, %d) = %d; want %d", tt.spent, tt.limit, got, tt.expected)
			}
		})
	}
}

func TestCalculateUtilization_InvalidInputs(t *testing.T) {
	// Zero limit
	_, err := CalculateUtilization(100, 0)
	if !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for limit=0, got %v", err)
	}

	// Negative limit
	_, err = CalculateUtilization(100, -500)
	if !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for limit < 0, got %v", err)
	}

	// Negative spent
	_, err = CalculateUtilization(-50, 1000)
	if !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for spent < 0, got %v", err)
	}
}

func TestCalculateUtilization_MaxInt64Boundaries(t *testing.T) {
	// 1. Equal MaxInt64: 100% representable
	got, err := CalculateUtilization(math.MaxInt64, math.MaxInt64)
	if err != nil {
		t.Fatalf("unexpected error for MaxInt64/MaxInt64: %v", err)
	}
	if got != 100 {
		t.Errorf("expected 100, got %d", got)
	}

	// 2. Double: spent = MaxInt64, limit = MaxInt64/2 => ~200%
	halfMax := int64(math.MaxInt64 / 2)
	got, err = CalculateUtilization(math.MaxInt64, halfMax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 200 {
		t.Errorf("expected 200, got %d", got)
	}

	// 3. Overflow: q = MaxInt64 / 1 > MaxInt64 / 100 => ErrOverflow
	_, err = CalculateUtilization(math.MaxInt64, 1)
	if !errors.Is(err, ErrOverflow) {
		t.Errorf("expected ErrOverflow when spent=MaxInt64 and limit=1, got %v", err)
	}

	// 4. Overflow: q > MaxInt64/100
	_, err = CalculateUtilization(math.MaxInt64, 50)
	if !errors.Is(err, ErrOverflow) {
		t.Errorf("expected ErrOverflow when q exceeds MaxInt64/100, got %v", err)
	}

	// 5. Overflow on base + fractional addition
	// Construct a case where q = MaxInt64 / 100 (so base = (MaxInt64/100)*100), but fractional causes addition overflow
	limit := int64(1000)
	maxQ := int64(math.MaxInt64 / 100)
	// spent = maxQ * limit + 999. Then q = maxQ, r = 999.
	// fractional = floor(999 * 100 / 1000) = 99.
	// base = maxQ * 100 = 9223372036854775800.
	// base + 99 = 9223372036854775899 > math.MaxInt64 (9223372036854775807).
	if maxQ <= math.MaxInt64/limit {
		spentWithBigFrac := maxQ*limit + 999
		_, err = CalculateUtilization(spentWithBigFrac, limit)
		if !errors.Is(err, ErrOverflow) {
			t.Errorf("expected ErrOverflow on base+fractional overflow, got %v", err)
		}
	}
}

func TestCurrency_AlwaysRequiredEvenZeroSpent(t *testing.T) {
	budget := &BudgetInput{Limit: 500000, Currency: "INR"}
	purchase := &PurchaseInput{Amount: 100000, Currency: "INR"}

	// Zero spent with MISSING currency must fail with ErrMissingData
	zeroSpentNoCurr := &SpendingInput{Spent: 0, Currency: ""}
	_, err := CalculateBudgetImpact(budget, zeroSpentNoCurr, purchase)
	if !errors.Is(err, ErrMissingData) {
		t.Errorf("expected ErrMissingData for empty currency on zero spent, got %v", err)
	}

	// Zero spent with whitespace currency must fail with ErrMissingData
	zeroSpentSpaceCurr := &SpendingInput{Spent: 0, Currency: "   "}
	_, err = CalculateBudgetImpact(budget, zeroSpentSpaceCurr, purchase)
	if !errors.Is(err, ErrMissingData) {
		t.Errorf("expected ErrMissingData for whitespace currency on zero spent, got %v", err)
	}

	// Zero spent with VALID matching currency must succeed
	zeroSpentValidCurr := &SpendingInput{Spent: 0, Currency: "INR"}
	res, err := CalculateBudgetImpact(budget, zeroSpentValidCurr, purchase)
	if err != nil {
		t.Fatalf("unexpected error with valid currency on zero spent: %v", err)
	}
	if res.SpentBeforeMinor != 0 || res.UtilizationBeforePercent != 0 {
		t.Errorf("expected 0 spent and 0%% utilization, got %d and %d%%", res.SpentBeforeMinor, res.UtilizationBeforePercent)
	}
}

func TestCurrency_MalformedVsMismatch(t *testing.T) {
	budget := &BudgetInput{Limit: 500000, Currency: "INR"}
	spending := &SpendingInput{Spent: 100000, Currency: "INR"}

	// 1. Malformed currencies (must produce ErrInvalidCurrency, NOT ErrCurrencyMismatch)
	malformedCases := []string{
		"inr",  // lowercase
		"Inr",  // mixed case
		"IN",   // 2 characters
		"INRR", // 4 characters
		"123",  // digits
		"IN!",  // special character
	}

	for _, malformed := range malformedCases {
		t.Run("Malformed "+malformed, func(t *testing.T) {
			purchase := &PurchaseInput{Amount: 50000, Currency: malformed}
			_, err := CalculateBudgetImpact(budget, spending, purchase)
			if !errors.Is(err, ErrInvalidCurrency) {
				t.Errorf("expected ErrInvalidCurrency for %q, got %v", malformed, err)
			}
			if errors.Is(err, ErrCurrencyMismatch) {
				t.Errorf("did not expect ErrCurrencyMismatch for malformed code %q", malformed)
			}
		})
	}

	// 2. Valid but mismatched currency (must produce ErrCurrencyMismatch)
	t.Run("Valid but mismatched USD vs INR", func(t *testing.T) {
		purchaseUSD := &PurchaseInput{Amount: 50000, Currency: "USD"}
		_, err := CalculateBudgetImpact(budget, spending, purchaseUSD)
		if !errors.Is(err, ErrCurrencyMismatch) {
			t.Errorf("expected ErrCurrencyMismatch for USD vs INR, got %v", err)
		}
		if errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("did not expect ErrInvalidCurrency for valid code USD")
		}
	})
}

func TestCalculateBudgetImpact_Calculations(t *testing.T) {
	budget := &BudgetInput{Limit: 500000, Currency: "INR"}     // 5000.00
	spending := &SpendingInput{Spent: 200000, Currency: "INR"} // 2000.00

	// Case 1: Affordable purchase (1000.00)
	purchase := &PurchaseInput{Amount: 100000, Currency: "INR"}
	result, err := CalculateBudgetImpact(budget, spending, purchase)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.RemainingBeforeMinor != 300000 {
		t.Errorf("expected remaining before 300000, got %d", result.RemainingBeforeMinor)
	}
	if result.RemainingAfterMinor != 200000 {
		t.Errorf("expected remaining after 200000, got %d", result.RemainingAfterMinor)
	}
	if result.UtilizationBeforePercent != 40 {
		t.Errorf("expected 40%% util before, got %d", result.UtilizationBeforePercent)
	}
	if result.UtilizationAfterPercent != 60 {
		t.Errorf("expected 60%% util after, got %d", result.UtilizationAfterPercent)
	}
	if result.ExceedsBudget != false || result.CanAfford != true {
		t.Errorf("expected affordable, got exceeds=%v, canAfford=%v", result.ExceedsBudget, result.CanAfford)
	}
	if result.ExceededAmountMinor != 0 {
		t.Errorf("expected 0 exceeded amount, got %d", result.ExceededAmountMinor)
	}

	// Case 2: Over budget purchase (3500.00)
	bigPurchase := &PurchaseInput{Amount: 350000, Currency: "INR"}
	overResult, err := CalculateBudgetImpact(budget, spending, bigPurchase)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if overResult.RemainingAfterMinor != -50000 {
		t.Errorf("expected remaining after -50000, got %d", overResult.RemainingAfterMinor)
	}
	if overResult.ExceedsBudget != true || overResult.CanAfford != false {
		t.Errorf("expected over budget, got exceeds=%v, canAfford=%v", overResult.ExceedsBudget, overResult.CanAfford)
	}
	if overResult.ExceededAmountMinor != 50000 {
		t.Errorf("expected 50000 exceeded amount, got %d", overResult.ExceededAmountMinor)
	}
	if overResult.UtilizationAfterPercent != 110 {
		t.Errorf("expected 110%% util after, got %d", overResult.UtilizationAfterPercent)
	}
}

func TestCalculateBudgetImpact_TotalSpentOverflow(t *testing.T) {
	budget := &BudgetInput{Limit: math.MaxInt64, Currency: "INR"}
	spending := &SpendingInput{Spent: math.MaxInt64 - 10, Currency: "INR"}
	purchase := &PurchaseInput{Amount: 20, Currency: "INR"}

	_, err := CalculateBudgetImpact(budget, spending, purchase)
	if !errors.Is(err, ErrOverflow) {
		t.Errorf("expected ErrOverflow when spent + purchase exceeds MaxInt64, got %v", err)
	}
}

func TestEvaluatePrePaymentRisk_Scenarios(t *testing.T) {
	budget := &BudgetInput{Limit: 1000000, Currency: "INR"}              // 10,000.00
	spending := &SpendingInput{Spent: 200000, Currency: "INR"}           // 2,000.00
	spendingLimit := &SpendingLimitInput{Limit: 500000, Currency: "INR"} // 5,000.00 max per purchase
	threshold80 := 80

	// Scenario 1: Clean, safe purchase (3,000.00)
	safePurchase := &PurchaseInput{Amount: 300000, Currency: "INR"}
	res, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:                budget,
		Spending:              spending,
		Purchase:              safePurchase,
		SpendingLimit:         spendingLimit,
		AlertThresholdPercent: &threshold80,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.CanProceed {
		t.Errorf("expected CanProceed=true for safe purchase")
	}
	if res.RiskScore != 0 {
		t.Errorf("expected RiskScore=0, got %d", res.RiskScore)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", res.Warnings)
	}

	// Scenario 2: Exceeds Alert Threshold (pushes to 85% util, 6,500.00 purchase)
	alertPurchase := &PurchaseInput{Amount: 650000, Currency: "INR"}
	noLimitInput := RiskCheckInput{
		Budget:                budget,
		Spending:              spending,
		Purchase:              alertPurchase,
		SpendingLimit:         nil, // no limit
		AlertThresholdPercent: &threshold80,
	}
	alertRes, err := EvaluatePrePaymentRisk(noLimitInput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alertRes.CanProceed {
		t.Errorf("alert threshold alone should still allow proceed (non-hard limit)")
	}
	if !alertRes.ExceedsAlertThreshold {
		t.Errorf("expected ExceedsAlertThreshold=true")
	}
	if alertRes.RiskScore == 0 {
		t.Errorf("expected non-zero risk score for alert threshold breach")
	}

	// Scenario 3: Exceeds Single-Transaction Spending Limit (6,000.00 > 5,000.00 limit)
	limitRes, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:                budget,
		Spending:              spending,
		Purchase:              &PurchaseInput{Amount: 600000, Currency: "INR"},
		SpendingLimit:         spendingLimit,
		AlertThresholdPercent: &threshold80,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limitRes.CanProceed {
		t.Errorf("expected CanProceed=false when spending limit exceeded")
	}
	if !limitRes.ExceedsSpendingLimit {
		t.Errorf("expected ExceedsSpendingLimit=true")
	}
	if limitRes.SpendingLimitExcessMinor != 100000 {
		t.Errorf("expected 100000 excess, got %d", limitRes.SpendingLimitExcessMinor)
	}

	// Scenario 4: Exceeds Total Budget (9,000.00 purchase + 2,000.00 spent = 11,000.00 > 10,000.00)
	budgetRes, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:                budget,
		Spending:              spending,
		Purchase:              &PurchaseInput{Amount: 900000, Currency: "INR"},
		SpendingLimit:         nil,
		AlertThresholdPercent: &threshold80,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if budgetRes.CanProceed {
		t.Errorf("expected CanProceed=false when budget exceeded")
	}
	if !budgetRes.BudgetImpact.ExceedsBudget {
		t.Errorf("expected ExceedsBudget=true")
	}
}

func TestInvalidInputs_NeverProduceSuccessResult(t *testing.T) {
	validBudget := &BudgetInput{Limit: 1000, Currency: "INR"}
	validSpending := &SpendingInput{Spent: 100, Currency: "INR"}
	validPurchase := &PurchaseInput{Amount: 50, Currency: "INR"}

	// Test nil inputs
	_, err := CalculateBudgetImpact(nil, validSpending, validPurchase)
	if err == nil || !errors.Is(err, ErrMissingData) {
		t.Errorf("expected ErrMissingData for nil budget, got %v", err)
	}

	_, err = CalculateBudgetImpact(validBudget, nil, validPurchase)
	if err == nil || !errors.Is(err, ErrMissingData) {
		t.Errorf("expected ErrMissingData for nil spending, got %v", err)
	}

	_, err = CalculateBudgetImpact(validBudget, validSpending, nil)
	if err == nil || !errors.Is(err, ErrMissingData) {
		t.Errorf("expected ErrMissingData for nil purchase, got %v", err)
	}

	// Test negative/zero amounts
	_, err = CalculateBudgetImpact(&BudgetInput{Limit: 0, Currency: "INR"}, validSpending, validPurchase)
	if err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for 0 budget, got %v", err)
	}

	_, err = CalculateBudgetImpact(validBudget, &SpendingInput{Spent: -1, Currency: "INR"}, validPurchase)
	if err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for negative spending, got %v", err)
	}

	_, err = CalculateBudgetImpact(validBudget, validSpending, &PurchaseInput{Amount: 0, Currency: "INR"})
	if err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for zero purchase amount, got %v", err)
	}

	// Test risk check with invalid alert threshold
	badThreshold := 105
	_, err = EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:                validBudget,
		Spending:              validSpending,
		Purchase:              validPurchase,
		AlertThresholdPercent: &badThreshold,
	})
	if err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for 105%% threshold, got %v", err)
	}
}
