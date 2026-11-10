package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "parishram-ai/backend/internal/db/sqlc"
	"parishram-ai/backend/internal/finance"
	"parishram-ai/backend/internal/middleware"
)

// PurchaseController handles purchase proposal and risk evaluation endpoints.
type PurchaseController struct {
	querier db.Querier
}

// NewPurchaseController initializes a new PurchaseController with the provided SQLC Querier.
func NewPurchaseController(querier db.Querier) *PurchaseController {
	return &PurchaseController{
		querier: querier,
	}
}

// EvaluatePurchaseRequest represents the payload submitted by the client to evaluate a proposed purchase.
type EvaluatePurchaseRequest struct {
	ItemName     string  `json:"item_name"`
	Merchant     string  `json:"merchant"`
	MerchantName string  `json:"merchant_name"` // Alternative alias for merchant
	Amount       *int64  `json:"amount"`
	Currency     *string `json:"currency"`
	Description  string  `json:"description"`
}

// PurchaseEvaluationResponse represents the detailed evaluation result returned to the caller.
type PurchaseEvaluationResponse struct {
	PurchaseID               int64                 `json:"purchase_id"`
	Status                   string                `json:"status"` // Strictly "proposed", never approved or paid
	ItemName                 string                `json:"item_name"`
	Merchant                 string                `json:"merchant"`
	Amount                   int64                 `json:"amount"`
	Currency                 string                `json:"currency"`
	MonthlyBudget            int64                 `json:"monthly_budget"`
	CurrentMonthlySpending   int64                 `json:"current_monthly_spending"`
	RemainingBefore          int64                 `json:"remaining_before"`
	RemainingAfter           int64                 `json:"remaining_after"`
	UtilizationBeforePercent int64                 `json:"utilization_before_percent"`
	UtilizationAfterPercent  int64                 `json:"utilization_after_percent"`
	ExceedsBudget            bool                  `json:"exceeds_budget"`
	ExceedsSpendingLimit     bool                  `json:"exceeds_spending_limit"`
	SpendingLimit            *int64                `json:"spending_limit,omitempty"`
	RiskScore                int                   `json:"risk_score"`
	CanProceed               bool                  `json:"can_proceed"`
	Warnings                 []string              `json:"warnings"`
	Findings                 []finance.RiskFinding `json:"findings"`
	CreatedAt                time.Time             `json:"created_at"`
}

// PurchaseDetailsResponse represents a persisted purchase record.
type PurchaseDetailsResponse struct {
	ID          int64                 `json:"id"`
	UserID      int64                 `json:"user_id"`
	Merchant    string                `json:"merchant"`
	ItemName    string                `json:"item_name"`
	Amount      int64                 `json:"amount"`
	Currency    string                `json:"currency"`
	Status      string                `json:"status"`
	CanProceed  bool                  `json:"can_proceed"`
	RiskScore   int32                 `json:"risk_score"`
	RiskFactors []finance.RiskFinding `json:"risk_factors"`
	Notes       string                `json:"notes"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

// CanFindingsProceed evaluates whether risk findings permit a purchase proposal to proceed.
func CanFindingsProceed(findings []finance.RiskFinding) bool {
	for _, f := range findings {
		if f.Code == finance.FindingBudgetExceeded || f.Code == finance.FindingSpendingLimitExceeded {
			return false
		}
	}
	return true
}

// ListPurchasesResponse represents a paginated list of purchase records.
type ListPurchasesResponse struct {
	Purchases []PurchaseDetailsResponse `json:"purchases"`
	Count     int                       `json:"count"`
	Limit     int32                     `json:"limit"`
	Offset    int32                     `json:"offset"`
}

// GetCurrentISTMonthlyPeriod returns the start and end boundaries of the monthly period
// in the Asia/Kolkata timezone (IST: UTC+05:30).
// Interval is start-inclusive [start, end) where:
// start: 1st of current month at 00:00:00 IST (converted to UTC absolute instant)
// end:   1st of following month at 00:00:00 IST (converted to UTC absolute instant)
func GetCurrentISTMonthlyPeriod(refTime time.Time) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		// Fallback to fixed zone UTC+05:30 if system IANA database is absent
		loc = time.FixedZone("IST", 5*3600+30*60)
	}

	tInIST := refTime.In(loc)
	year := tInIST.Year()
	month := tInIST.Month()

	startIST := time.Date(year, month, 1, 0, 0, 0, 0, loc)

	nextMonth := month + 1
	nextYear := year
	if nextMonth > 12 {
		nextMonth = 1
		nextYear++
	}
	endIST := time.Date(nextYear, nextMonth, 1, 0, 0, 0, 0, loc)

	return startIST.UTC(), endIST.UTC(), nil
}

// EvaluatePurchase evaluates pre-payment risk for a proposed purchase against the user's
// configured budget, spending limit, and confirmed monthly expense history.
// POST /api/v1/purchases/evaluate
func (ctrl *PurchaseController) EvaluatePurchase(c *gin.Context) {
	if ctrl.querier == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "database not configured",
			"code":  "SERVICE_UNAVAILABLE",
		})
		return
	}

	userID, err := middleware.GetUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "unauthorized",
			"code":  "UNAUTHORIZED",
		})
		return
	}

	var req EvaluatePurchaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request body",
			"code":    "INVALID_REQUEST",
			"details": err.Error(),
		})
		return
	}

	// 1. Validate ItemName
	itemName := strings.TrimSpace(req.ItemName)
	if itemName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "item_name is required",
			"code":  "INVALID_INPUT",
		})
		return
	}
	if len(itemName) > 255 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "item_name cannot exceed 255 characters",
			"code":  "INVALID_INPUT",
		})
		return
	}

	// 2. Validate Merchant
	merchant := strings.TrimSpace(req.Merchant)
	if merchant == "" {
		merchant = strings.TrimSpace(req.MerchantName)
	}
	if merchant == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "merchant is required",
			"code":  "INVALID_INPUT",
		})
		return
	}
	if len(merchant) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "merchant cannot exceed 128 characters",
			"code":  "INVALID_INPUT",
		})
		return
	}

	// 3. Validate Amount (must be strictly positive int64 minor units)
	if req.Amount == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "amount is required",
			"code":  "INVALID_AMOUNT",
		})
		return
	}
	if *req.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "amount must be greater than zero",
			"code":  "INVALID_AMOUNT",
		})
		return
	}

	// 4. Validate Currency format
	if req.Currency == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "currency is required",
			"code":  "INVALID_CURRENCY",
		})
		return
	}
	trimmedCurr := strings.TrimSpace(*req.Currency)
	if err := finance.ValidateCurrency(trimmedCurr); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid currency: %v", err),
			"code":  "INVALID_CURRENCY",
		})
		return
	}

	// 5. Retrieve User Profile to check account currency and spending limit
	user, err := ctrl.querier.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "user record not found",
				"code":  "USER_NOT_FOUND",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve user profile",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	if trimmedCurr != user.Currency {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("purchase currency %s does not match user account currency %s", trimmedCurr, user.Currency),
			"code":  "CURRENCY_MISMATCH",
		})
		return
	}

	// 6. Retrieve User Budget (mandatory: do not invent a fake budget if not configured)
	budget, err := ctrl.querier.GetBudgetByUserID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "budget is not configured; user must configure a monthly budget before evaluating purchases",
				"code":  "BUDGET_NOT_CONFIGURED",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve user budget",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	if trimmedCurr != budget.Currency {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("purchase currency %s does not match user budget currency %s", trimmedCurr, budget.Currency),
			"code":  "CURRENCY_MISMATCH",
		})
		return
	}

	// 7. Calculate Monthly Period Boundaries (Asia/Kolkata IST month converted to UTC)
	startUTC, endUTC, _ := GetCurrentISTMonthlyPeriod(time.Now().UTC())

	// 8. Retrieve Confirmed Monthly Spending
	currentSpending, err := ctrl.querier.GetMonthlyExpenseTotalForUser(c.Request.Context(), db.GetMonthlyExpenseTotalForUserParams{
		UserID:    userID,
		Currency:  trimmedCurr,
		SpentAt:   pgtype.Timestamptz{Time: startUTC, Valid: true},
		SpentAt_2: pgtype.Timestamptz{Time: endUTC, Valid: true},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to aggregate confirmed expenses",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	// 9. Prepare SpendingLimitInput if configured (spending_limit > 0)
	var spendingLimitInput *finance.SpendingLimitInput
	var spendingLimitVal *int64
	if user.SpendingLimit > 0 {
		val := user.SpendingLimit
		spendingLimitVal = &val
		spendingLimitInput = &finance.SpendingLimitInput{
			Limit:    user.SpendingLimit,
			Currency: user.Currency,
		}
	}

	threshold := int(budget.AlertThresholdPercent)
	if threshold < 1 || threshold > 100 {
		threshold = 80
	}

	// 10. Call Deterministic Finance Engine
	riskInput := finance.RiskCheckInput{
		Budget: &finance.BudgetInput{
			Limit:    budget.MonthlyLimit,
			Currency: budget.Currency,
		},
		Spending: &finance.SpendingInput{
			Spent:    currentSpending,
			Currency: trimmedCurr,
		},
		Purchase: &finance.PurchaseInput{
			Amount:       *req.Amount,
			Currency:     trimmedCurr,
			MerchantName: merchant,
			ItemName:     itemName,
		},
		SpendingLimit:         spendingLimitInput,
		AlertThresholdPercent: &threshold,
	}

	evalResult, err := finance.EvaluatePrePaymentRisk(riskInput)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("financial evaluation failed: %v", err),
			"code":  "EVALUATION_ERROR",
		})
		return
	}

	// 11. Serialize structured findings into JSON string for risk_factors storage
	findingsBytes, _ := json.Marshal(evalResult.Findings)
	riskFactorsJSON := string(findingsBytes)

	// 12. Persist Purchase Proposal in "proposed" status (never marked approved or paid)
	persistedPurchase, err := ctrl.querier.CreatePurchaseProposal(c.Request.Context(), db.CreatePurchaseProposalParams{
		UserID:       userID,
		MerchantName: merchant,
		ItemName:     itemName,
		Amount:       *req.Amount,
		Currency:     trimmedCurr,
		Status:       "proposed",
		RiskScore:    int32(evalResult.RiskScore),
		RiskFactors:  riskFactorsJSON,
		Notes:        strings.TrimSpace(req.Description),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to persist purchase proposal",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	var createdAt time.Time
	if persistedPurchase.CreatedAt.Valid {
		createdAt = persistedPurchase.CreatedAt.Time
	} else {
		createdAt = time.Now().UTC()
	}

	// 13. Construct and return response
	c.JSON(http.StatusCreated, PurchaseEvaluationResponse{
		PurchaseID:               persistedPurchase.ID,
		Status:                   persistedPurchase.Status,
		ItemName:                 persistedPurchase.ItemName,
		Merchant:                 persistedPurchase.MerchantName,
		Amount:                   persistedPurchase.Amount,
		Currency:                 persistedPurchase.Currency,
		MonthlyBudget:            budget.MonthlyLimit,
		CurrentMonthlySpending:   currentSpending,
		RemainingBefore:          evalResult.BudgetImpact.RemainingBeforeMinor,
		RemainingAfter:           evalResult.BudgetImpact.RemainingAfterMinor,
		UtilizationBeforePercent: evalResult.BudgetImpact.UtilizationBeforePercent,
		UtilizationAfterPercent:  evalResult.BudgetImpact.UtilizationAfterPercent,
		ExceedsBudget:            evalResult.BudgetImpact.ExceedsBudget,
		ExceedsSpendingLimit:     evalResult.ExceedsSpendingLimit,
		SpendingLimit:            spendingLimitVal,
		RiskScore:                evalResult.RiskScore,
		CanProceed:               evalResult.CanProceed,
		Warnings:                 evalResult.Warnings,
		Findings:                 evalResult.Findings,
		CreatedAt:                createdAt,
	})
}

// GetPurchase retrieves an authenticated user's purchase proposal by ID.
// GET /api/v1/purchases/:id
func (ctrl *PurchaseController) GetPurchase(c *gin.Context) {
	if ctrl.querier == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "database not configured",
			"code":  "SERVICE_UNAVAILABLE",
		})
		return
	}

	userID, err := middleware.GetUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "unauthorized",
			"code":  "UNAUTHORIZED",
		})
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid purchase id",
			"code":  "INVALID_ID",
		})
		return
	}

	purchase, err := ctrl.querier.GetPurchaseByIDAndUser(c.Request.Context(), db.GetPurchaseByIDAndUserParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "purchase not found",
				"code":  "PURCHASE_NOT_FOUND",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve purchase",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	var findings []finance.RiskFinding
	if purchase.RiskFactors != "" {
		_ = json.Unmarshal([]byte(purchase.RiskFactors), &findings)
	}

	var createdAt, updatedAt time.Time
	if purchase.CreatedAt.Valid {
		createdAt = purchase.CreatedAt.Time
	}
	if purchase.UpdatedAt.Valid {
		updatedAt = purchase.UpdatedAt.Time
	}

	c.JSON(http.StatusOK, PurchaseDetailsResponse{
		ID:          purchase.ID,
		UserID:      purchase.UserID,
		Merchant:    purchase.MerchantName,
		ItemName:    purchase.ItemName,
		Amount:      purchase.Amount,
		Currency:    purchase.Currency,
		Status:      purchase.Status,
		CanProceed:  CanFindingsProceed(findings),
		RiskScore:   purchase.RiskScore,
		RiskFactors: findings,
		Notes:       purchase.Notes,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	})
}

// ListPurchases retrieves a paginated list of purchase proposals for the authenticated user.
// GET /api/v1/purchases
func (ctrl *PurchaseController) ListPurchases(c *gin.Context) {
	if ctrl.querier == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "database not configured",
			"code":  "SERVICE_UNAVAILABLE",
		})
		return
	}

	userID, err := middleware.GetUserID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "unauthorized",
			"code":  "UNAUTHORIZED",
		})
		return
	}

	limitStr := c.DefaultQuery("limit", "20")
	offsetStr := c.DefaultQuery("offset", "0")

	limit64, err := strconv.ParseInt(limitStr, 10, 32)
	if err != nil || limit64 < 1 || limit64 > 100 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "limit must be an integer between 1 and 100",
			"code":  "INVALID_PAGINATION",
		})
		return
	}

	offset64, err := strconv.ParseInt(offsetStr, 10, 32)
	if err != nil || offset64 < 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "offset must be a non-negative integer",
			"code":  "INVALID_PAGINATION",
		})
		return
	}

	purchases, err := ctrl.querier.ListPurchasesByUserID(c.Request.Context(), db.ListPurchasesByUserIDParams{
		UserID: userID,
		Limit:  int32(limit64),
		Offset: int32(offset64),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to list purchases",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	items := make([]PurchaseDetailsResponse, 0, len(purchases))
	for _, p := range purchases {
		var findings []finance.RiskFinding
		if p.RiskFactors != "" {
			_ = json.Unmarshal([]byte(p.RiskFactors), &findings)
		}
		var createdAt, updatedAt time.Time
		if p.CreatedAt.Valid {
			createdAt = p.CreatedAt.Time
		}
		if p.UpdatedAt.Valid {
			updatedAt = p.UpdatedAt.Time
		}
		items = append(items, PurchaseDetailsResponse{
			ID:          p.ID,
			UserID:      p.UserID,
			Merchant:    p.MerchantName,
			ItemName:    p.ItemName,
			Amount:      p.Amount,
			Currency:    p.Currency,
			Status:      p.Status,
			CanProceed:  CanFindingsProceed(findings),
			RiskScore:   p.RiskScore,
			RiskFactors: findings,
			Notes:       p.Notes,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		})
	}

	c.JSON(http.StatusOK, ListPurchasesResponse{
		Purchases: items,
		Count:     len(items),
		Limit:     int32(limit64),
		Offset:    int32(offset64),
	})
}
