package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	db "parishram-ai/backend/internal/db/sqlc"
	"parishram-ai/backend/internal/finance"
	"parishram-ai/backend/internal/middleware"
)

// BudgetController handles budget-related HTTP endpoints.
type BudgetController struct {
	querier db.Querier
}

// NewBudgetController initializes a new BudgetController with the provided SQLC Querier.
func NewBudgetController(querier db.Querier) *BudgetController {
	return &BudgetController{
		querier: querier,
	}
}

// BudgetResponse represents the user-facing JSON structure of a budget.
type BudgetResponse struct {
	ID                    int64     `json:"id"`
	UserID                int64     `json:"user_id"`
	MonthlyLimit          int64     `json:"monthly_limit"`
	AlertThresholdPercent int32     `json:"alert_threshold_percent"`
	Currency              string    `json:"currency"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// UpdateBudgetRequest represents the expected payload for creating or updating a budget.
type UpdateBudgetRequest struct {
	MonthlyLimit          *int64  `json:"monthly_limit"`
	Currency              *string `json:"currency"`
	AlertThresholdPercent *int    `json:"alert_threshold_percent"`
}

func toBudgetResponse(b db.Budget) BudgetResponse {
	var createdAt, updatedAt time.Time
	if b.CreatedAt.Valid {
		createdAt = b.CreatedAt.Time
	}
	if b.UpdatedAt.Valid {
		updatedAt = b.UpdatedAt.Time
	}
	return BudgetResponse{
		ID:                    b.ID,
		UserID:                b.UserID,
		MonthlyLimit:          b.MonthlyLimit,
		AlertThresholdPercent: b.AlertThresholdPercent,
		Currency:              b.Currency,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
	}
}

// GetBudget retrieves the authenticated user's budget.
// GET /api/v1/budgets/me
func (ctrl *BudgetController) GetBudget(c *gin.Context) {
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

	budget, err := ctrl.querier.GetBudgetByUserID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "budget not found",
				"code":  "BUDGET_NOT_FOUND",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve budget",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	c.JSON(http.StatusOK, toBudgetResponse(budget))
}

// UpdateBudget creates or updates the authenticated user's budget.
// PUT /api/v1/budgets/me
func (ctrl *BudgetController) UpdateBudget(c *gin.Context) {
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

	var req UpdateBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request body",
			"code":    "INVALID_REQUEST",
			"details": err.Error(),
		})
		return
	}

	// 1. Validate MonthlyLimit: required and must be strictly positive (> 0)
	if req.MonthlyLimit == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "monthly_limit is required",
			"code":  "INVALID_AMOUNT",
		})
		return
	}
	if *req.MonthlyLimit <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "monthly_limit must be greater than zero",
			"code":  "INVALID_AMOUNT",
		})
		return
	}

	// 2. Validate Currency: required and must be an uppercase 3-letter ASCII code
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

	// 3. Validate AlertThresholdPercent if provided: 1 <= threshold <= 100
	if req.AlertThresholdPercent != nil {
		if *req.AlertThresholdPercent < 1 || *req.AlertThresholdPercent > 100 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "alert_threshold_percent must be between 1 and 100",
				"code":  "INVALID_THRESHOLD",
			})
			return
		}
	}

	// 4. Check existing budget to maintain consistency and prevent accidental overrides
	existingBudget, err := ctrl.querier.GetBudgetByUserID(c.Request.Context(), userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to verify existing budget",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	hasExisting := (err == nil)

	var threshold int32
	if hasExisting {
		// Reject inconsistent currency updates on existing budgets
		if trimmedCurr != existingBudget.Currency {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("currency cannot be changed from %s to %s for an existing budget", existingBudget.Currency, trimmedCurr),
				"code":  "CURRENCY_MISMATCH",
			})
			return
		}

		// Retain existing alert threshold if omitted; do not silently replace with default
		if req.AlertThresholdPercent != nil {
			threshold = int32(*req.AlertThresholdPercent)
		} else {
			threshold = existingBudget.AlertThresholdPercent
		}
	} else {
		// New budget creation: verify against user's registered account currency
		user, uErr := ctrl.querier.GetUserByID(c.Request.Context(), userID)
		if uErr != nil {
			if errors.Is(uErr, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{
					"error": "user record not found",
					"code":  "USER_NOT_FOUND",
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to verify user profile",
				"code":  "DATABASE_ERROR",
			})
			return
		}

		if trimmedCurr != user.Currency {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("currency %s does not match user account currency %s", trimmedCurr, user.Currency),
				"code":  "CURRENCY_MISMATCH",
			})
			return
		}

		if req.AlertThresholdPercent != nil {
			threshold = int32(*req.AlertThresholdPercent)
		} else {
			threshold = 80 // Default per DB schema
		}
	}

	// 5. Save budget atomically using SQLC-generated upsert query
	savedBudget, err := ctrl.querier.CreateOrUpdateBudget(c.Request.Context(), db.CreateOrUpdateBudgetParams{
		UserID:                userID,
		MonthlyLimit:          *req.MonthlyLimit,
		AlertThresholdPercent: threshold,
		Currency:              trimmedCurr,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to save budget",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	c.JSON(http.StatusOK, toBudgetResponse(savedBudget))
}
