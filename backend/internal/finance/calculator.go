package finance

import (
	"fmt"
	"math"
	"math/bits"
)

// CalculateUtilization computes the integer percentage floor((spent / limit) * 100)
// using quotient and remainder arithmetic with 128-bit unsigned intermediate math to
// prevent 64-bit integer overflow. No floating-point operations are performed.
func CalculateUtilization(spent int64, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("%w: budget limit must be greater than zero, got %d", ErrInvalidAmount, limit)
	}
	if spent < 0 {
		return 0, fmt.Errorf("%w: spent amount cannot be negative, got %d", ErrInvalidAmount, spent)
	}

	q := spent / limit
	r := spent % limit

	// Prevent overflow on base multiplication
	if q > math.MaxInt64/100 {
		return 0, fmt.Errorf("%w: utilization quotient %d exceeds safe multiplication limit", ErrOverflow, q)
	}

	base := q * 100

	// Safe 128-bit calculation of floor((r * 100) / limit) using math/bits.
	// Since 0 <= r < limit <= math.MaxInt64, r is non-negative and fits in uint64.
	// bits.Mul64 computes (r * 100) into a 128-bit unsigned pair (hi, lo) without 64-bit overflow.
	// Because r < limit, (r * 100) / limit is strictly < 100, so hi < limit and bits.Div64 will not overflow or panic.
	hi, lo := bits.Mul64(uint64(r), 100)
	fracUint, _ := bits.Div64(hi, lo, uint64(limit))
	fractional := int64(fracUint)

	// Check for overflow when combining base and fractional parts
	if base > math.MaxInt64-fractional {
		return 0, fmt.Errorf("%w: utilization addition %d + %d exceeds math.MaxInt64", ErrOverflow, base, fractional)
	}

	return base + fractional, nil
}

// CalculateBudgetImpact computes the affordability, remaining budget, and utilization change
// for a proposed purchase against a user's budget and spending history.
func CalculateBudgetImpact(budget *BudgetInput, spending *SpendingInput, purchase *PurchaseInput) (*BudgetImpactResult, error) {
	if err := validateBudget(budget); err != nil {
		return nil, err
	}
	if err := validateSpending(spending); err != nil {
		return nil, err
	}
	if err := validatePurchase(purchase); err != nil {
		return nil, err
	}
	if err := validateMatchingCurrencies(budget.Currency, spending.Currency, purchase.Currency); err != nil {
		return nil, err
	}

	// Safe addition to prevent overflow on total spent
	if spending.Spent > math.MaxInt64-purchase.Amount {
		return nil, fmt.Errorf("%w: adding purchase %d to spent %d exceeds math.MaxInt64", ErrOverflow, purchase.Amount, spending.Spent)
	}
	totalSpentAfter := spending.Spent + purchase.Amount

	utilBefore, err := CalculateUtilization(spending.Spent, budget.Limit)
	if err != nil {
		return nil, err
	}

	utilAfter, err := CalculateUtilization(totalSpentAfter, budget.Limit)
	if err != nil {
		return nil, err
	}

	remainingBefore := budget.Limit - spending.Spent
	remainingAfter := budget.Limit - totalSpentAfter

	exceedsBudget := totalSpentAfter > budget.Limit
	var exceededAmount int64
	if exceedsBudget {
		exceededAmount = totalSpentAfter - budget.Limit
	}

	return &BudgetImpactResult{
		BudgetLimitMinor:         budget.Limit,
		SpentBeforeMinor:         spending.Spent,
		PurchaseAmountMinor:      purchase.Amount,
		Currency:                 budget.Currency,
		RemainingBeforeMinor:     remainingBefore,
		RemainingAfterMinor:      remainingAfter,
		UtilizationBeforePercent: utilBefore,
		UtilizationAfterPercent:  utilAfter,
		TotalSpentAfterMinor:     totalSpentAfter,
		ExceedsBudget:            exceedsBudget,
		ExceededAmountMinor:      exceededAmount,
		CanAfford:                !exceedsBudget,
	}, nil
}

// EvaluatePrePaymentRisk executes deterministic risk checks on a proposed purchase,
// checking budget excess, single-purchase spending limits, and utilization alert thresholds.
func EvaluatePrePaymentRisk(input RiskCheckInput) (*RiskCheckResult, error) {
	if input.Budget == nil {
		return nil, fmt.Errorf("%w: budget is required", ErrMissingData)
	}
	if input.Spending == nil {
		return nil, fmt.Errorf("%w: spending data is required", ErrMissingData)
	}
	if input.Purchase == nil {
		return nil, fmt.Errorf("%w: purchase data is required", ErrMissingData)
	}

	// Validate spending limit if provided
	if input.SpendingLimit != nil {
		if err := validateSpendingLimit(input.SpendingLimit); err != nil {
			return nil, err
		}
		if err := validateMatchingCurrencies(input.Budget.Currency, input.Spending.Currency, input.Purchase.Currency, input.SpendingLimit.Currency); err != nil {
			return nil, err
		}
	} else {
		if err := validateMatchingCurrencies(input.Budget.Currency, input.Spending.Currency, input.Purchase.Currency); err != nil {
			return nil, err
		}
	}

	// Validate alert threshold percent if provided
	threshold := 80
	if input.AlertThresholdPercent != nil {
		t := *input.AlertThresholdPercent
		if t < 1 || t > 100 {
			return nil, fmt.Errorf("%w: alert threshold percent must be between 1 and 100, got %d", ErrInvalidAmount, t)
		}
		threshold = t
	}

	// Calculate deterministic budget impact
	impact, err := CalculateBudgetImpact(input.Budget, input.Spending, input.Purchase)
	if err != nil {
		return nil, err
	}

	var warnings []string
	riskScore := 0

	// Check 1: Budget Exceeded
	if impact.ExceedsBudget {
		riskScore += 45
		warnings = append(warnings, fmt.Sprintf(
			"Purchase exceeds remaining budget by %s %d (in minor units).",
			impact.Currency, impact.ExceededAmountMinor,
		))
	}

	// Check 2: Spending Limit Exceeded
	exceedsSpendingLimit := false
	var limitExcess int64
	if input.SpendingLimit != nil && input.SpendingLimit.Limit > 0 {
		if input.Purchase.Amount > input.SpendingLimit.Limit {
			exceedsSpendingLimit = true
			limitExcess = input.Purchase.Amount - input.SpendingLimit.Limit
			riskScore += 35
			warnings = append(warnings, fmt.Sprintf(
				"Purchase amount (%s %d) exceeds configured spending limit of %s %d.",
				input.Purchase.Currency, input.Purchase.Amount,
				input.SpendingLimit.Currency, input.SpendingLimit.Limit,
			))
		}
	}

	// Check 3: Alert Threshold Exceeded (if not already over budget)
	exceedsAlertThreshold := false
	if impact.UtilizationAfterPercent >= int64(threshold) {
		exceedsAlertThreshold = true
		if !impact.ExceedsBudget {
			riskScore += 20
			warnings = append(warnings, fmt.Sprintf(
				"Purchase pushes budget utilization to %d%%, exceeding alert threshold of %d%%.",
				impact.UtilizationAfterPercent, threshold,
			))
		}
	}

	if riskScore > 100 {
		riskScore = 100
	}

	canProceed := !impact.ExceedsBudget && !exceedsSpendingLimit

	return &RiskCheckResult{
		CanProceed:               canProceed,
		RiskScore:                riskScore,
		Warnings:                 warnings,
		BudgetImpact:             impact,
		ExceedsSpendingLimit:     exceedsSpendingLimit,
		SpendingLimitExcessMinor: limitExcess,
		ExceedsAlertThreshold:    exceedsAlertThreshold,
	}, nil
}
