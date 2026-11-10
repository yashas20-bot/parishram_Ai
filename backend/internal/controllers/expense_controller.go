package controllers

import (
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

// ExpenseController handles expense-related HTTP endpoints.
type ExpenseController struct {
	querier db.Querier
}

// NewExpenseController initializes a new ExpenseController with the provided SQLC Querier.
func NewExpenseController(querier db.Querier) *ExpenseController {
	return &ExpenseController{
		querier: querier,
	}
}

// CreateExpenseRequest represents the expected payload for creating an expense.
type CreateExpenseRequest struct {
	Category    string  `json:"category"`
	Amount      *int64  `json:"amount"`
	Currency    *string `json:"currency"`
	Description string  `json:"description"`
	SpentAt     *string `json:"spent_at"`
}

// ExpenseResponse represents the user-facing JSON structure of an expense.
type ExpenseResponse struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Category    string    `json:"category"`
	Amount      int64     `json:"amount"`
	Currency    string    `json:"currency"`
	Description string    `json:"description"`
	SpentAt     time.Time `json:"spent_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListExpensesResponse represents the paginated response for listing expenses.
type ListExpensesResponse struct {
	Expenses []ExpenseResponse `json:"expenses"`
	Count    int               `json:"count"`
	Limit    int32             `json:"limit"`
	Offset   int32             `json:"offset"`
}

func toExpenseResponse(e db.Expense) ExpenseResponse {
	var spentAt, createdAt time.Time
	if e.SpentAt.Valid {
		spentAt = e.SpentAt.Time
	}
	if e.CreatedAt.Valid {
		createdAt = e.CreatedAt.Time
	}
	return ExpenseResponse{
		ID:          e.ID,
		UserID:      e.UserID,
		Category:    e.Category,
		Amount:      e.Amount,
		Currency:    e.Currency,
		Description: e.Description,
		SpentAt:     spentAt,
		CreatedAt:   createdAt,
	}
}

// CreateExpense records a new confirmed expense for the authenticated user.
// POST /api/v1/expenses
func (ctrl *ExpenseController) CreateExpense(c *gin.Context) {
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

	var req CreateExpenseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request body",
			"code":    "INVALID_REQUEST",
			"details": err.Error(),
		})
		return
	}

	// 1. Validate Category: required, non-empty, max 64 chars
	trimmedCategory := strings.TrimSpace(req.Category)
	if trimmedCategory == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "category is required",
			"code":  "INVALID_CATEGORY",
		})
		return
	}
	if len(trimmedCategory) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "category cannot exceed 64 characters",
			"code":  "INVALID_CATEGORY",
		})
		return
	}

	// 2. Validate Amount: required, strictly positive (> 0) in minor units
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

	// 3. Validate Currency: required, 3 uppercase ASCII letters
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

	// 4. Validate Currency Consistency against User Profile and Budget
	user, uErr := ctrl.querier.GetUserByID(c.Request.Context(), userID)
	if uErr == nil {
		if trimmedCurr != user.Currency {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("expense currency %s does not match user account currency %s", trimmedCurr, user.Currency),
				"code":  "CURRENCY_MISMATCH",
			})
			return
		}
	}

	budget, bErr := ctrl.querier.GetBudgetByUserID(c.Request.Context(), userID)
	if bErr == nil {
		if trimmedCurr != budget.Currency {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("expense currency %s does not match user budget currency %s", trimmedCurr, budget.Currency),
				"code":  "CURRENCY_MISMATCH",
			})
			return
		}
	}

	// 5. Parse or default SpentAt timestamp
	var spentAtTime time.Time
	if req.SpentAt != nil && strings.TrimSpace(*req.SpentAt) != "" {
		t, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(*req.SpentAt))
		if parseErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("invalid spent_at format, expected RFC3339: %v", parseErr),
				"code":  "INVALID_DATE",
			})
			return
		}
		spentAtTime = t
	} else {
		spentAtTime = time.Now().UTC()
	}

	// 6. Execute SQLC CreateExpense query
	expense, err := ctrl.querier.CreateExpense(c.Request.Context(), db.CreateExpenseParams{
		UserID:      userID,
		Category:    trimmedCategory,
		Amount:      *req.Amount,
		Currency:    trimmedCurr,
		Description: strings.TrimSpace(req.Description),
		SpentAt:     pgtype.Timestamptz{Time: spentAtTime, Valid: true},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to record expense",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	c.JSON(http.StatusCreated, toExpenseResponse(expense))
}

// ListExpenses retrieves a paginated list of expenses for the authenticated user, newest first.
// GET /api/v1/expenses
func (ctrl *ExpenseController) ListExpenses(c *gin.Context) {
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

	expenses, err := ctrl.querier.ListExpensesByUserID(c.Request.Context(), db.ListExpensesByUserIDParams{
		UserID: userID,
		Limit:  int32(limit64),
		Offset: int32(offset64),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to list expenses",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	items := make([]ExpenseResponse, 0, len(expenses))
	for _, e := range expenses {
		items = append(items, toExpenseResponse(e))
	}

	c.JSON(http.StatusOK, ListExpensesResponse{
		Expenses: items,
		Count:    len(items),
		Limit:    int32(limit64),
		Offset:   int32(offset64),
	})
}

// GetExpense retrieves a single expense by ID belonging to the authenticated user.
// GET /api/v1/expenses/:id
func (ctrl *ExpenseController) GetExpense(c *gin.Context) {
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
			"error": "invalid expense id",
			"code":  "INVALID_ID",
		})
		return
	}

	expense, err := ctrl.querier.GetExpenseByIDAndUser(c.Request.Context(), db.GetExpenseByIDAndUserParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "expense not found",
				"code":  "EXPENSE_NOT_FOUND",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve expense",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	c.JSON(http.StatusOK, toExpenseResponse(expense))
}

// DeleteExpense deletes an expense by ID belonging to the authenticated user.
// DELETE /api/v1/expenses/:id
func (ctrl *ExpenseController) DeleteExpense(c *gin.Context) {
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
			"error": "invalid expense id",
			"code":  "INVALID_ID",
		})
		return
	}

	// 1. Ownership check: verify the expense exists and belongs to the authenticated user
	existing, err := ctrl.querier.GetExpenseByIDAndUser(c.Request.Context(), db.GetExpenseByIDAndUserParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "expense not found",
				"code":  "EXPENSE_NOT_FOUND",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to verify expense ownership",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	// 2. Perform user-scoped deletion
	err = ctrl.querier.DeleteExpenseForUser(c.Request.Context(), db.DeleteExpenseForUserParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to delete expense",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	// 3. Financial audit log: record the deletion event for compliance and traceability
	_, _ = ctrl.querier.CreateAuditLog(c.Request.Context(), db.CreateAuditLogParams{
		UserID:     pgtype.Int8{Int64: userID, Valid: true},
		Action:     "DELETE_EXPENSE",
		EntityType: "expense",
		EntityID:   strconv.FormatInt(id, 10),
		Details:    fmt.Sprintf("Deleted expense %d: %s %d (category: %s)", id, existing.Currency, existing.Amount, existing.Category),
		IpAddress:  c.ClientIP(),
	})

	c.JSON(http.StatusOK, gin.H{
		"message": "expense deleted successfully",
		"id":      id,
	})
}
