package finance

// BudgetInput represents the user's monthly budget.
type BudgetInput struct {
	Limit    int64  `json:"limit"`
	Currency string `json:"currency"`
}

// SpendingInput represents the user's current spending total.
type SpendingInput struct {
	Spent    int64  `json:"spent"`
	Currency string `json:"currency"`
}

// PurchaseInput represents a proposed purchase.
type PurchaseInput struct {
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
	MerchantName string `json:"merchant_name"`
	ItemName     string `json:"item_name"`
}

// SpendingLimitInput represents a user-configured spending limit.
type SpendingLimitInput struct {
	Limit    int64  `json:"limit"`
	Currency string `json:"currency"`
}

// RiskCheckInput contains all parameters required for a pre-payment risk evaluation.
type RiskCheckInput struct {
	Budget                *BudgetInput        `json:"budget"`
	Spending              *SpendingInput      `json:"spending"`
	Purchase              *PurchaseInput      `json:"purchase"`
	SpendingLimit         *SpendingLimitInput `json:"spending_limit"`          // Optional: nil if no limit set
	AlertThresholdPercent *int                `json:"alert_threshold_percent"` // Optional: nil defaults to 80%
}

// BudgetImpactResult contains the deterministic calculation results of a purchase against a budget.
type BudgetImpactResult struct {
	BudgetLimitMinor         int64  `json:"budget_limit_minor"`
	SpentBeforeMinor         int64  `json:"spent_before_minor"`
	PurchaseAmountMinor      int64  `json:"purchase_amount_minor"`
	Currency                 string `json:"currency"`
	RemainingBeforeMinor     int64  `json:"remaining_before_minor"`
	RemainingAfterMinor      int64  `json:"remaining_after_minor"`
	UtilizationBeforePercent int64  `json:"utilization_before_percent"`
	UtilizationAfterPercent  int64  `json:"utilization_after_percent"`
	TotalSpentAfterMinor     int64  `json:"total_spent_after_minor"`
	ExceedsBudget            bool   `json:"exceeds_budget"`
	ExceededAmountMinor      int64  `json:"exceeded_amount_minor"`
	CanAfford                bool   `json:"can_afford"`
}

// RiskCheckResult provides deterministic evaluation of purchase affordability and risk factors.
type RiskCheckResult struct {
	CanProceed               bool                `json:"can_proceed"`
	RiskScore                int                 `json:"risk_score"` // 0 (lowest risk) to 100 (highest risk)
	Warnings                 []string            `json:"warnings"`
	BudgetImpact             *BudgetImpactResult `json:"budget_impact"`
	ExceedsSpendingLimit     bool                `json:"exceeds_spending_limit"`
	SpendingLimitExcessMinor int64               `json:"spending_limit_excess_minor"`
	ExceedsAlertThreshold    bool                `json:"exceeds_alert_threshold"`
}
