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
	limit := int64(1000)
	maxQ := int64(math.MaxInt64 / 100)
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
	if !res.HasFinding(FindingWithinBudget) {
		t.Errorf("expected FindingWithinBudget")
	}
	if !res.HasFinding(FindingWithinSpendingLimit) {
		t.Errorf("expected FindingWithinSpendingLimit")
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
	if !alertRes.HasFinding(FindingAlertThresholdExceeded) {
		t.Errorf("expected FindingAlertThresholdExceeded")
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
	if !limitRes.HasFinding(FindingSpendingLimitExceeded) {
		t.Errorf("expected FindingSpendingLimitExceeded")
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
	if !budgetRes.HasFinding(FindingBudgetExceeded) {
		t.Errorf("expected FindingBudgetExceeded")
	}
}

func TestEvaluatePrePaymentRisk_ExactBudgetBoundaries(t *testing.T) {
	budget := &BudgetInput{Limit: 100000, Currency: "INR"}    // 1,000.00
	spending := &SpendingInput{Spent: 60000, Currency: "INR"} // 600.00

	// 1. Exact zero remaining budget (purchase = 400.00)
	exactPurchase := &PurchaseInput{Amount: 40000, Currency: "INR"}
	res, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:   budget,
		Spending: spending,
		Purchase: exactPurchase,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.BudgetImpact.RemainingAfterMinor != 0 {
		t.Errorf("expected RemainingAfterMinor=0, got %d", res.BudgetImpact.RemainingAfterMinor)
	}
	if res.BudgetImpact.ExceedsBudget {
		t.Errorf("exact budget should not be marked as ExceedsBudget")
	}
	if !res.CanProceed {
		t.Errorf("exact budget should allow CanProceed=true")
	}
	if !res.HasFinding(FindingWithinBudget) {
		t.Errorf("expected FindingWithinBudget")
	}
	if res.HasFinding(FindingBudgetExceeded) {
		t.Errorf("did not expect FindingBudgetExceeded")
	}

	// 2. Exceeds budget by exactly one minor unit (purchase = 400.01 = 40001 minor units)
	overByOne := &PurchaseInput{Amount: 40001, Currency: "INR"}
	resOver, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:   budget,
		Spending: spending,
		Purchase: overByOne,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resOver.BudgetImpact.RemainingAfterMinor != -1 {
		t.Errorf("expected RemainingAfterMinor=-1, got %d", resOver.BudgetImpact.RemainingAfterMinor)
	}
	if !resOver.BudgetImpact.ExceedsBudget {
		t.Errorf("expected ExceedsBudget=true when over by 1 minor unit")
	}
	if resOver.BudgetImpact.ExceededAmountMinor != 1 {
		t.Errorf("expected ExceededAmountMinor=1, got %d", resOver.BudgetImpact.ExceededAmountMinor)
	}
	if resOver.CanProceed {
		t.Errorf("expected CanProceed=false when over budget")
	}
	if !resOver.HasFinding(FindingBudgetExceeded) {
		t.Errorf("expected FindingBudgetExceeded")
	}
}

func TestEvaluatePrePaymentRisk_SpendingLimitBoundaries(t *testing.T) {
	budget := &BudgetInput{Limit: 1000000, Currency: "INR"}
	spending := &SpendingInput{Spent: 0, Currency: "INR"}
	limit500 := &SpendingLimitInput{Limit: 50000, Currency: "INR"} // 500.00 limit

	// 1. Below spending limit (499.99 = 49999 minor units)
	resBelow, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:        budget,
		Spending:      spending,
		Purchase:      &PurchaseInput{Amount: 49999, Currency: "INR"},
		SpendingLimit: limit500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resBelow.ExceedsSpendingLimit {
		t.Errorf("expected ExceedsSpendingLimit=false for amount below limit")
	}
	if !resBelow.HasFinding(FindingWithinSpendingLimit) {
		t.Errorf("expected FindingWithinSpendingLimit")
	}

	// 2. Exactly at spending limit (500.00 = 50000 minor units)
	resExact, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:        budget,
		Spending:      spending,
		Purchase:      &PurchaseInput{Amount: 50000, Currency: "INR"},
		SpendingLimit: limit500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resExact.ExceedsSpendingLimit {
		t.Errorf("expected ExceedsSpendingLimit=false for amount exactly at limit")
	}
	if resExact.SpendingLimitExcessMinor != 0 {
		t.Errorf("expected SpendingLimitExcessMinor=0, got %d", resExact.SpendingLimitExcessMinor)
	}
	if !resExact.HasFinding(FindingWithinSpendingLimit) {
		t.Errorf("expected FindingWithinSpendingLimit")
	}

	// 3. Above spending limit by 1 minor unit (500.01 = 50001 minor units)
	resAbove, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:        budget,
		Spending:      spending,
		Purchase:      &PurchaseInput{Amount: 50001, Currency: "INR"},
		SpendingLimit: limit500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resAbove.ExceedsSpendingLimit {
		t.Errorf("expected ExceedsSpendingLimit=true for amount above limit")
	}
	if resAbove.SpendingLimitExcessMinor != 1 {
		t.Errorf("expected SpendingLimitExcessMinor=1, got %d", resAbove.SpendingLimitExcessMinor)
	}
	if !resAbove.HasFinding(FindingSpendingLimitExceeded) {
		t.Errorf("expected FindingSpendingLimitExceeded")
	}
	if resAbove.CanProceed {
		t.Errorf("expected CanProceed=false when spending limit exceeded")
	}

	// 4. Configured spending limit of zero (freeze)
	limitZero := &SpendingLimitInput{Limit: 0, Currency: "INR"}
	resZero, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:        budget,
		Spending:      spending,
		Purchase:      &PurchaseInput{Amount: 1, Currency: "INR"},
		SpendingLimit: limitZero,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resZero.ExceedsSpendingLimit {
		t.Errorf("expected ExceedsSpendingLimit=true for configured limit of 0 and amount 1")
	}
	if resZero.SpendingLimitExcessMinor != 1 {
		t.Errorf("expected excess=1, got %d", resZero.SpendingLimitExcessMinor)
	}
	if !resZero.HasFinding(FindingSpendingLimitExceeded) {
		t.Errorf("expected FindingSpendingLimitExceeded for zero limit")
	}
	if resZero.CanProceed {
		t.Errorf("expected CanProceed=false for zero limit breach")
	}

	// 5. Unconfigured spending limit (nil)
	resUnconfigured, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:        budget,
		Spending:      spending,
		Purchase:      &PurchaseInput{Amount: 100000, Currency: "INR"},
		SpendingLimit: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resUnconfigured.ExceedsSpendingLimit {
		t.Errorf("unconfigured spending limit should not trigger ExceedsSpendingLimit")
	}
	if resUnconfigured.HasFinding(FindingSpendingLimitExceeded) || resUnconfigured.HasFinding(FindingWithinSpendingLimit) {
		t.Errorf("unconfigured limit should not emit spending limit findings")
	}
}

func TestEvaluatePrePaymentRisk_DeterminismAndConsistency(t *testing.T) {
	input := RiskCheckInput{
		Budget:        &BudgetInput{Limit: 500000, Currency: "INR"},
		Spending:      &SpendingInput{Spent: 100000, Currency: "INR"},
		Purchase:      &PurchaseInput{Amount: 200000, Currency: "INR"},
		SpendingLimit: &SpendingLimitInput{Limit: 300000, Currency: "INR"},
	}

	res1, err1 := EvaluatePrePaymentRisk(input)
	if err1 != nil {
		t.Fatalf("unexpected error run 1: %v", err1)
	}

	res2, err2 := EvaluatePrePaymentRisk(input)
	if err2 != nil {
		t.Fatalf("unexpected error run 2: %v", err2)
	}

	if res1.CanProceed != res2.CanProceed {
		t.Errorf("CanProceed inconsistent: %v vs %v", res1.CanProceed, res2.CanProceed)
	}
	if res1.RiskScore != res2.RiskScore {
		t.Errorf("RiskScore inconsistent: %d vs %d", res1.RiskScore, res2.RiskScore)
	}
	if len(res1.Findings) != len(res2.Findings) {
		t.Errorf("Findings count inconsistent: %d vs %d", len(res1.Findings), len(res2.Findings))
	}
	for i := range res1.Findings {
		if res1.Findings[i].Code != res2.Findings[i].Code {
			t.Errorf("Finding [%d] code inconsistent: %s vs %s", i, res1.Findings[i].Code, res2.Findings[i].Code)
		}
	}
}

func TestInvalidInputs_NeverProduceSuccessResult(t *testing.T) {
	validBudget := &BudgetInput{Limit: 1000, Currency: "INR"}
	validSpending := &SpendingInput{Spent: 100, Currency: "INR"}
	validPurchase := &PurchaseInput{Amount: 50, Currency: "INR"}

	// Test nil inputs
	res, err := CalculateBudgetImpact(nil, validSpending, validPurchase)
	if res != nil || err == nil || !errors.Is(err, ErrMissingData) {
		t.Errorf("expected (nil, ErrMissingData) for nil budget, got (%v, %v)", res, err)
	}

	res, err = CalculateBudgetImpact(validBudget, nil, validPurchase)
	if res != nil || err == nil || !errors.Is(err, ErrMissingData) {
		t.Errorf("expected (nil, ErrMissingData) for nil spending, got (%v, %v)", res, err)
	}

	res, err = CalculateBudgetImpact(validBudget, validSpending, nil)
	if res != nil || err == nil || !errors.Is(err, ErrMissingData) {
		t.Errorf("expected (nil, ErrMissingData) for nil purchase, got (%v, %v)", res, err)
	}

	// Test negative/zero amounts
	res, err = CalculateBudgetImpact(&BudgetInput{Limit: 0, Currency: "INR"}, validSpending, validPurchase)
	if res != nil || err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected (nil, ErrInvalidAmount) for 0 budget, got (%v, %v)", res, err)
	}

	res, err = CalculateBudgetImpact(validBudget, &SpendingInput{Spent: -1, Currency: "INR"}, validPurchase)
	if res != nil || err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected (nil, ErrInvalidAmount) for negative spending, got (%v, %v)", res, err)
	}

	res, err = CalculateBudgetImpact(validBudget, validSpending, &PurchaseInput{Amount: 0, Currency: "INR"})
	if res != nil || err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected (nil, ErrInvalidAmount) for zero purchase amount, got (%v, %v)", res, err)
	}

	res, err = CalculateBudgetImpact(validBudget, validSpending, &PurchaseInput{Amount: -10, Currency: "INR"})
	if res != nil || err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected (nil, ErrInvalidAmount) for negative purchase amount, got (%v, %v)", res, err)
	}

	// Test risk check with invalid alert threshold
	badThreshold := 105
	resRisk, err := EvaluatePrePaymentRisk(RiskCheckInput{
		Budget:                validBudget,
		Spending:              validSpending,
		Purchase:              validPurchase,
		AlertThresholdPercent: &badThreshold,
	})
	if resRisk != nil || err == nil || !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("expected (nil, ErrInvalidAmount) for 105%% threshold, got (%v, %v)", resRisk, err)
	}
}
