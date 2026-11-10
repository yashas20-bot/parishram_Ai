package controllers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "parishram-ai/backend/internal/db/sqlc"
	"parishram-ai/backend/internal/finance"
	"parishram-ai/backend/internal/middleware"
	"parishram-ai/backend/internal/paypal"
)

// PaymentController handles payment lifecycle and PayPal order integration endpoints.
type PaymentController struct {
	querier      db.Querier
	paypalClient paypal.Client
}

// NewPaymentController creates a new PaymentController instance.
func NewPaymentController(querier db.Querier, paypalClient paypal.Client) *PaymentController {
	return &PaymentController{
		querier:      querier,
		paypalClient: paypalClient,
	}
}

// CreateOrderRequest represents the client payload for creating a PayPal payment order.
type CreateOrderRequest struct {
	PurchaseID int64 `json:"purchase_id"`
}

// CreateOrderResponse represents the structured order creation response returned to the client.
type CreateOrderResponse struct {
	PurchaseID    int64     `json:"purchase_id"`
	PaymentID     int64     `json:"payment_id"`
	PaypalOrderID string    `json:"paypal_order_id"`
	Status        string    `json:"status"` // Strictly "awaiting_approval"
	Amount        int64     `json:"amount"` // Integer minor currency units
	Currency      string    `json:"currency"`
	ApprovalURL   string    `json:"approval_url"`
	CreatedAt     time.Time `json:"created_at"`
}

// CreateOrder initiates a PayPal order for an evaluated purchase proposal.
// POST /api/v1/payments/create-order
func (ctrl *PaymentController) CreateOrder(c *gin.Context) {
	if ctrl.querier == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "database not configured",
			"code":  "SERVICE_UNAVAILABLE",
		})
		return
	}
	if ctrl.paypalClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "paypal client not configured",
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

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PurchaseID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "valid purchase_id is required",
			"code":  "INVALID_REQUEST",
		})
		return
	}

	// 1. Load purchase with ownership enforcement (scoped by user_id)
	purchase, err := ctrl.querier.GetPurchaseByIDAndUser(c.Request.Context(), db.GetPurchaseByIDAndUserParams{
		ID:     req.PurchaseID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "purchase proposal not found",
				"code":  "PURCHASE_NOT_FOUND",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve purchase proposal",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	// 2. Validate Purchase Status (must be strictly 'proposed')
	if purchase.Status != "proposed" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("purchase is in '%s' status; only 'proposed' purchases can initiate payment", purchase.Status),
			"code":  "INVALID_PURCHASE_STATUS",
		})
		return
	}

	// 3. Check for existing payment order on this purchase
	existingPayment, err := ctrl.querier.GetPaymentByPurchaseIDAndUser(c.Request.Context(), db.GetPaymentByPurchaseIDAndUserParams{
		PurchaseID: pgtype.Int8{Int64: purchase.ID, Valid: true},
		UserID:     userID,
	})
	if err == nil {
		// Existing payment record found
		if existingPayment.Status == "completed" {
			c.JSON(http.StatusConflict, gin.H{
				"error": "purchase has already been paid and completed",
				"code":  "PURCHASE_ALREADY_PAID",
			})
			return
		}
		if existingPayment.Status == "awaiting_approval" || existingPayment.Status == "approved" || existingPayment.Status == "capture_pending" || existingPayment.Status == "proposed" {
			c.JSON(http.StatusConflict, gin.H{
				"error":           fmt.Sprintf("an active payment order already exists for this purchase (status: %s)", existingPayment.Status),
				"code":            "CONFLICTING_PAYMENT_ORDER",
				"paypal_order_id": existingPayment.PaypalOrderID,
				"payment_id":      existingPayment.ID,
			})
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to check existing payments",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	// 4. Fresh Financial Revalidation
	// Never rely on old persisted risk scores; recheck against current live balances and limits
	user, err := ctrl.querier.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve user profile for validation",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	if purchase.Currency != user.Currency {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("purchase currency %s does not match user account currency %s", purchase.Currency, user.Currency),
			"code":  "CURRENCY_MISMATCH",
		})
		return
	}

	budget, err := ctrl.querier.GetBudgetByUserID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "budget is not configured; user must configure a monthly budget before creating payment orders",
				"code":  "BUDGET_NOT_CONFIGURED",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to retrieve user budget for validation",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	if purchase.Currency != budget.Currency {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("purchase currency %s does not match budget currency %s", purchase.Currency, budget.Currency),
			"code":  "CURRENCY_MISMATCH",
		})
		return
	}

	// Calculate monthly period in Asia/Kolkata IST converted to UTC
	startUTC, endUTC, _ := GetCurrentISTMonthlyPeriod(time.Now().UTC())

	currentSpending, err := ctrl.querier.GetMonthlyExpenseTotalForUser(c.Request.Context(), db.GetMonthlyExpenseTotalForUserParams{
		UserID:    userID,
		Currency:  purchase.Currency,
		SpentAt:   pgtype.Timestamptz{Time: startUTC, Valid: true},
		SpentAt_2: pgtype.Timestamptz{Time: endUTC, Valid: true},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to aggregate current monthly expenses",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	var spendingLimitInput *finance.SpendingLimitInput
	if user.SpendingLimit > 0 {
		spendingLimitInput = &finance.SpendingLimitInput{
			Limit:    user.SpendingLimit,
			Currency: user.Currency,
		}
	}

	threshold := int(budget.AlertThresholdPercent)
	if threshold < 1 || threshold > 100 {
		threshold = 80
	}

	riskInput := finance.RiskCheckInput{
		Budget: &finance.BudgetInput{
			Limit:    budget.MonthlyLimit,
			Currency: budget.Currency,
		},
		Spending: &finance.SpendingInput{
			Spent:    currentSpending,
			Currency: purchase.Currency,
		},
		Purchase: &finance.PurchaseInput{
			Amount:       purchase.Amount,
			Currency:     purchase.Currency,
			MerchantName: purchase.MerchantName,
			ItemName:     purchase.ItemName,
		},
		SpendingLimit:         spendingLimitInput,
		AlertThresholdPercent: &threshold,
	}

	evalResult, err := finance.EvaluatePrePaymentRisk(riskInput)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("financial revalidation failed: %v", err),
			"code":  "EVALUATION_ERROR",
		})
		return
	}

	// 5. Enforce CanProceed Gate
	if !evalResult.CanProceed {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":    "purchase exceeds current financial limits and cannot proceed to payment",
			"code":     "PURCHASE_LIMITS_EXCEEDED",
			"findings": evalResult.Findings,
			"warnings": evalResult.Warnings,
		})
		return
	}

	// 6. Format Decimal Amount for PayPal without float operations
	decimalAmount, err := paypal.FormatMinorToDecimal(purchase.Amount, purchase.Currency)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid purchase amount formatting: %v", err),
			"code":  "INVALID_AMOUNT",
		})
		return
	}

	// 7. Create PayPal Order via PayPal Orders API v2
	// Stable purchase-scoped idempotency key prevents duplicate orders during network retries
	idempotencyKey := fmt.Sprintf("parishram-purchase-%d-%d", userID, purchase.ID)
	orderReq := paypal.CreateOrderRequest{
		Intent: "CAPTURE",
		PurchaseUnits: []paypal.PurchaseUnitRequest{
			{
				ReferenceID: fmt.Sprintf("%d", purchase.ID),
				Description: purchase.ItemName,
				CustomID:    fmt.Sprintf("%d", purchase.ID),
				Amount: paypal.Amount{
					CurrencyCode: purchase.Currency,
					Value:        decimalAmount,
				},
			},
		},
		ApplicationContext: &paypal.ApplicationContext{
			UserAction: "PAY_NOW",
		},
	}

	order, err := ctrl.paypalClient.CreateOrder(c.Request.Context(), orderReq, idempotencyKey)
	if err != nil {
		if errors.Is(err, paypal.ErrTimeout) {
			c.JSON(http.StatusGatewayTimeout, gin.H{
				"error": "request to PayPal timed out; please retry or verify order state before creating a new payment",
				"code":  "PAYPAL_TIMEOUT",
			})
			return
		}
		if errors.Is(err, paypal.ErrAuthFailed) {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to authenticate with PayPal API",
				"code":  "PAYPAL_AUTH_FAILED",
			})
			return
		}
		var apiErr *paypal.APIError
		if errors.As(err, &apiErr) {
			c.JSON(http.StatusBadGateway, gin.H{
				"error":    "paypal rejected order creation request",
				"code":     "PAYPAL_API_ERROR",
				"name":     apiErr.Name,
				"message":  apiErr.Message,
				"debug_id": apiErr.DebugID,
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{
			"error": fmt.Sprintf("failed to create order with PayPal: %v", err),
			"code":  "PAYPAL_ERROR",
		})
		return
	}

	// 8. Persist Payment Record in 'awaiting_approval' status
	// Creating an order does NOT approve the purchase or capture payment
	payment, err := ctrl.querier.CreatePayment(c.Request.Context(), db.CreatePaymentParams{
		UserID:        userID,
		PurchaseID:    pgtype.Int8{Int64: purchase.ID, Valid: true},
		PaypalOrderID: order.ID,
		Amount:        purchase.Amount,
		Currency:      purchase.Currency,
		Status:        "awaiting_approval",
	})
	if err != nil {
		log.Printf("[CRITICAL] PayPal order %s created successfully but failed to persist payment in database for purchase %d: %v", order.ID, purchase.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to persist payment record",
			"code":  "DATABASE_ERROR",
		})
		return
	}

	var createdAt time.Time
	if payment.CreatedAt.Valid {
		createdAt = payment.CreatedAt.Time
	} else {
		createdAt = time.Now().UTC()
	}

	approvalURL := order.ApprovalURL()

	// 9. Return Response
	c.JSON(http.StatusCreated, CreateOrderResponse{
		PurchaseID:    purchase.ID,
		PaymentID:     payment.ID,
		PaypalOrderID: order.ID,
		Status:        payment.Status,
		Amount:        payment.Amount,
		Currency:      payment.Currency,
		ApprovalURL:   approvalURL,
		CreatedAt:     createdAt,
	})
}
