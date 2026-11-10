package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "parishram-ai/backend/internal/db/sqlc"
	"parishram-ai/backend/internal/middleware"
	"parishram-ai/backend/internal/paypal"
)

// mockPaymentQuerier implements the database queries required by PaymentController.
type mockPaymentQuerier struct {
	db.Querier
	usersByID          map[int64]db.User
	budgetsByUser      map[int64]db.Budget
	purchasesByID      map[int64]db.Purchase
	expensesByUser     map[int64][]db.Expense
	paymentsByPurchase map[int64][]db.Payment
	simulateDBErr      bool
	failCreatePayment  bool
	nextPaymentID      int64
}

func newMockPaymentQuerier() *mockPaymentQuerier {
	return &mockPaymentQuerier{
		usersByID:          make(map[int64]db.User),
		budgetsByUser:      make(map[int64]db.Budget),
		purchasesByID:      make(map[int64]db.Purchase),
		expensesByUser:     make(map[int64][]db.Expense),
		paymentsByPurchase: make(map[int64][]db.Payment),
		nextPaymentID:      1,
	}
}

func (m *mockPaymentQuerier) GetPurchaseByIDAndUser(ctx context.Context, arg db.GetPurchaseByIDAndUserParams) (db.Purchase, error) {
	if m.simulateDBErr {
		return db.Purchase{}, errors.New("simulated db error on GetPurchaseByIDAndUser")
	}
	p, exists := m.purchasesByID[arg.ID]
	if !exists || p.UserID != arg.UserID {
		return db.Purchase{}, pgx.ErrNoRows
	}
	return p, nil
}

func (m *mockPaymentQuerier) GetPaymentByPurchaseIDAndUser(ctx context.Context, arg db.GetPaymentByPurchaseIDAndUserParams) (db.Payment, error) {
	if m.simulateDBErr {
		return db.Payment{}, errors.New("simulated db error on GetPaymentByPurchaseIDAndUser")
	}
	if !arg.PurchaseID.Valid {
		return db.Payment{}, pgx.ErrNoRows
	}
	list := m.paymentsByPurchase[arg.PurchaseID.Int64]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].UserID == arg.UserID {
			return list[i], nil
		}
	}
	return db.Payment{}, pgx.ErrNoRows
}

func (m *mockPaymentQuerier) GetUserByID(ctx context.Context, id int64) (db.User, error) {
	if m.simulateDBErr {
		return db.User{}, errors.New("simulated db error on GetUserByID")
	}
	u, exists := m.usersByID[id]
	if !exists {
		return db.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (m *mockPaymentQuerier) GetBudgetByUserID(ctx context.Context, userID int64) (db.Budget, error) {
	if m.simulateDBErr {
		return db.Budget{}, errors.New("simulated db error on GetBudgetByUserID")
	}
	b, exists := m.budgetsByUser[userID]
	if !exists {
		return db.Budget{}, pgx.ErrNoRows
	}
	return b, nil
}

func (m *mockPaymentQuerier) GetMonthlyExpenseTotalForUser(ctx context.Context, arg db.GetMonthlyExpenseTotalForUserParams) (int64, error) {
	if m.simulateDBErr {
		return 0, errors.New("simulated db error on GetMonthlyExpenseTotalForUser")
	}
	start := arg.SpentAt.Time
	end := arg.SpentAt_2.Time

	var total int64 = 0
	for _, e := range m.expensesByUser[arg.UserID] {
		if e.Currency != arg.Currency {
			continue
		}
		t := e.SpentAt.Time
		if (t.Equal(start) || t.After(start)) && t.Before(end) {
			total += e.Amount
		}
	}
	return total, nil
}

func (m *mockPaymentQuerier) CreatePayment(ctx context.Context, arg db.CreatePaymentParams) (db.Payment, error) {
	if m.simulateDBErr || m.failCreatePayment {
		return db.Payment{}, errors.New("simulated db error on CreatePayment")
	}
	p := db.Payment{
		ID:            m.nextPaymentID,
		UserID:        arg.UserID,
		PurchaseID:    arg.PurchaseID,
		PaypalOrderID: arg.PaypalOrderID,
		Amount:        arg.Amount,
		Currency:      arg.Currency,
		Status:        arg.Status,
		CreatedAt:     pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		UpdatedAt:     pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
	m.nextPaymentID++
	if arg.PurchaseID.Valid {
		m.paymentsByPurchase[arg.PurchaseID.Int64] = append(m.paymentsByPurchase[arg.PurchaseID.Int64], p)
	}
	return p, nil
}

// mockPayPalClient implements paypal.Client for testing without external network calls.
type mockPayPalClient struct {
	createOrderFunc func(ctx context.Context, req paypal.CreateOrderRequest, idempotencyKey string) (*paypal.Order, error)
	recordedOrders  []paypal.CreateOrderRequest
	recordedKeys    []string
}

func (m *mockPayPalClient) GetAccessToken(ctx context.Context) (string, error) {
	return "MOCK_ACCESS_TOKEN", nil
}

func (m *mockPayPalClient) CreateOrder(ctx context.Context, req paypal.CreateOrderRequest, idempotencyKey string) (*paypal.Order, error) {
	m.recordedOrders = append(m.recordedOrders, req)
	m.recordedKeys = append(m.recordedKeys, idempotencyKey)
	if m.createOrderFunc != nil {
		return m.createOrderFunc(ctx, req, idempotencyKey)
	}
	return &paypal.Order{
		ID:     "MOCK-PAYPAL-ORDER-12345",
		Status: "CREATED",
		Intent: "CAPTURE",
		Links: []paypal.LinkDescription{
			{
				Href:   "https://www.sandbox.paypal.com/checkoutnow?token=MOCK-PAYPAL-ORDER-12345",
				Rel:    "approve",
				Method: "GET",
			},
		},
	}, nil
}

func setupPaymentTestRouter(querier db.Querier, paypalClient paypal.Client, authenticatedUserID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	paymentCtrl := NewPaymentController(querier, paypalClient)

	r := gin.New()
	v1 := r.Group("/api/v1")
	protected := v1.Group("")
	protected.Use(func(c *gin.Context) {
		if authenticatedUserID > 0 {
			c.Set(middleware.ContextKeyUserID, authenticatedUserID)
			c.Next()
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "code": "UNAUTHORIZED"})
		}
	})
	{
		protected.POST("/payments/create-order", paymentCtrl.CreateOrder)
	}

	return r
}

// 1. Unauthenticated requests are rejected with 401
func TestPayment_CreateOrder_Unauthenticated(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	r := setupPaymentTestRouter(mq, mp, 0) // unauthenticated

	body := `{"purchase_id": 42}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}
}

// 2. Invalid purchase ID rejected with 400
func TestPayment_CreateOrder_InvalidPurchaseID(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	r := setupPaymentTestRouter(mq, mp, 10)

	invalidBodies := []string{
		`{"purchase_id": 0}`,
		`{"purchase_id": -1}`,
		`{"purchase_id": -500}`,
		`{"purchase_id": "invalid"}`,
		`{}`,
		`invalid json`,
	}

	for _, b := range invalidBodies {
		t.Run(b, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(b))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request for %s, got %d", b, w.Code)
			}
		})
	}
}

// 3. Purchase not found returns 404
func TestPayment_CreateOrder_PurchaseNotFound(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	r := setupPaymentTestRouter(mq, mp, 10)

	body := `{"purchase_id": 999}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found, got %d: %s", w.Code, w.Body.String())
	}
}

// 4. User ownership enforced: User A cannot create payment for User B's purchase (returns 404)
func TestPayment_CreateOrder_UserIsolation_OtherUserPurchase(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}

	const userA = int64(10)
	const userB = int64(20)
	const purchaseID = int64(42)

	// Purchase belongs strictly to User B
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userB,
		ItemName:     "Laptop",
		MerchantName: "Store",
		Amount:       5000000,
		Currency:     "INR",
		Status:       "proposed",
	}

	// User A attempts to create payment order for User B's purchase
	rA := setupPaymentTestRouter(mq, mp, userA)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rA.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when User A tries to pay User B's purchase, got %d: %s", w.Code, w.Body.String())
	}
}

// 5. Purchase not in 'proposed' status rejected with 400
func TestPayment_CreateOrder_PurchaseNotProposed(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Watch",
		MerchantName: "Store",
		Amount:       100000,
		Currency:     "INR",
		Status:       "rejected", // Not 'proposed'
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for non-proposed purchase, got %d: %s", w.Code, w.Body.String())
	}
}

// 6. Purchase already paid returns 409 Conflict
func TestPayment_CreateOrder_PurchaseAlreadyPaid(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Watch",
		MerchantName: "Store",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	// Existing payment completed
	mq.paymentsByPurchase[purchaseID] = []db.Payment{
		{
			ID:            1,
			UserID:        userID,
			PurchaseID:    pgtype.Int8{Int64: purchaseID, Valid: true},
			PaypalOrderID: "EXISTING-ORDER-COMPLETED",
			Amount:        100000,
			Currency:      "INR",
			Status:        "completed",
		},
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for already-paid purchase, got %d: %s", w.Code, w.Body.String())
	}
}

// 7. Conflicting active payment returns 409 Conflict
func TestPayment_CreateOrder_ConflictingActivePayment(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Watch",
		MerchantName: "Store",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	// Active order awaiting approval
	mq.paymentsByPurchase[purchaseID] = []db.Payment{
		{
			ID:            1,
			UserID:        userID,
			PurchaseID:    pgtype.Int8{Int64: purchaseID, Valid: true},
			PaypalOrderID: "EXISTING-ORDER-AWAITING",
			Amount:        100000,
			Currency:      "INR",
			Status:        "awaiting_approval",
		},
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for existing active payment order, got %d: %s", w.Code, w.Body.String())
	}
}

// 8. Currency mismatch between purchase and user account returns 400
func TestPayment_CreateOrder_CurrencyMismatch_User(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Cloud Subscription",
		MerchantName: "US Provider",
		Amount:       5000,
		Currency:     "USD", // User is INR, purchase is USD
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on currency mismatch, got %d: %s", w.Code, w.Body.String())
	}
}

// 9. Budget not configured returns 400
func TestPayment_CreateOrder_BudgetNotConfigured(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	// User has NO budget
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Shoes",
		MerchantName: "Store",
		Amount:       50000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when budget is not configured, got %d: %s", w.Code, w.Body.String())
	}
}

// 10. Fresh financial revalidation: budget exceeded after proposal creation rejected with 422
func TestPayment_CreateOrder_FreshRevalidation_BudgetExceeded(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	// Monthly budget is ₹5,000 (500000 paise)
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	// Purchase is ₹3,000 (300000 paise)
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Headphones",
		MerchantName: "Audio Store",
		Amount:       300000,
		Currency:     "INR",
		Status:       "proposed",
	}

	// Between evaluation and order creation, new expenses occurred: ₹3,000 spent!
	// Total spending becomes 300000 + 300000 = 600000 > 500000 budget!
	now := time.Now().UTC()
	mq.expensesByUser[userID] = []db.Expense{
		{
			ID:       1,
			UserID:   userID,
			Amount:   300000,
			Currency: "INR",
			SpentAt:  pgtype.Timestamptz{Time: now, Valid: true},
		},
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 Unprocessable Entity when budget is exceeded upon revalidation, got %d: %s", w.Code, w.Body.String())
	}
	// Verify that PayPal was NEVER called
	if len(mp.recordedOrders) != 0 {
		t.Errorf("expected zero PayPal calls when limits are exceeded, got %d", len(mp.recordedOrders))
	}
}

// 11. Fresh financial revalidation: spending limit exceeded rejected with 422
func TestPayment_CreateOrder_FreshRevalidation_SpendingLimitExceeded(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	// User has a spending limit of ₹2,000 (200000 paise)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR", SpendingLimit: 200000}
	// Budget is large: ₹10,000
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 1000000, Currency: "INR", AlertThresholdPercent: 80}
	// Purchase is ₹2,500 (250000 paise) which exceeds the ₹2,000 spending limit
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Luxury Watch",
		MerchantName: "Jewelry",
		Amount:       250000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 Unprocessable Entity when spending limit is exceeded, got %d: %s", w.Code, w.Body.String())
	}
	if len(mp.recordedOrders) != 0 {
		t.Errorf("expected zero PayPal calls, got %d", len(mp.recordedOrders))
	}
}

// 12. Successful PayPal order creation returns 201 Created with approval_url
func TestPayment_CreateOrder_Success(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{
		createOrderFunc: func(ctx context.Context, req paypal.CreateOrderRequest, idempotencyKey string) (*paypal.Order, error) {
			return &paypal.Order{
				ID:     "PAYPAL-ORDER-777",
				Status: "CREATED",
				Intent: "CAPTURE",
				Links: []paypal.LinkDescription{
					{
						Href:   "https://www.sandbox.paypal.com/checkoutnow?token=PAYPAL-ORDER-777",
						Rel:    "approve",
						Method: "GET",
					},
				},
			}, nil
		},
	}

	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR", SpendingLimit: 1000000}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Wireless headphones",
		MerchantName: "Audio Hub",
		Amount:       200000, // ₹2,000
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on order creation, got %d: %s", w.Code, w.Body.String())
	}

	var resp CreateOrderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.PurchaseID != purchaseID {
		t.Errorf("expected PurchaseID=%d, got %d", purchaseID, resp.PurchaseID)
	}
	if resp.PaypalOrderID != "PAYPAL-ORDER-777" {
		t.Errorf("expected PaypalOrderID='PAYPAL-ORDER-777', got '%s'", resp.PaypalOrderID)
	}
	if resp.Status != "awaiting_approval" {
		t.Errorf("expected Status='awaiting_approval', got '%s'", resp.Status)
	}
	if resp.Amount != 200000 {
		t.Errorf("expected Amount=200000, got %d", resp.Amount)
	}
	if resp.Currency != "INR" {
		t.Errorf("expected Currency='INR', got '%s'", resp.Currency)
	}
	expectedApproval := "https://www.sandbox.paypal.com/checkoutnow?token=PAYPAL-ORDER-777"
	if resp.ApprovalURL != expectedApproval {
		t.Errorf("expected ApprovalURL='%s', got '%s'", expectedApproval, resp.ApprovalURL)
	}

	// Verify PayPal unit was called with pure integer formatting ("2000.00")
	if len(mp.recordedOrders) != 1 {
		t.Fatalf("expected 1 PayPal call, got %d", len(mp.recordedOrders))
	}
	unit := mp.recordedOrders[0].PurchaseUnits[0]
	if unit.Amount.Value != "2000.00" {
		t.Errorf("expected PayPal amount value '2000.00', got '%s'", unit.Amount.Value)
	}
}

// 13. Idempotency key passed to PayPal
func TestPayment_CreateOrder_IdempotencyKeyPassed(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Item",
		MerchantName: "Merchant",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", w.Code)
	}

	if len(mp.recordedKeys) != 1 {
		t.Fatalf("expected 1 recorded key, got %d", len(mp.recordedKeys))
	}
	expectedKey := fmt.Sprintf("parishram-purchase-%d-%d", userID, purchaseID)
	if mp.recordedKeys[0] != expectedKey {
		t.Errorf("expected idempotency key '%s', got '%s'", expectedKey, mp.recordedKeys[0])
	}
}

// 14. PayPal authentication failure returns 502 Bad Gateway
func TestPayment_CreateOrder_PayPalAuthFailure(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{
		createOrderFunc: func(ctx context.Context, req paypal.CreateOrderRequest, idempotencyKey string) (*paypal.Order, error) {
			return nil, paypal.ErrAuthFailed
		},
	}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Item",
		MerchantName: "Merchant",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway on PayPal auth failure, got %d: %s", w.Code, w.Body.String())
	}
}

// 15. PayPal API failure returns 502 Bad Gateway
func TestPayment_CreateOrder_PayPalAPIFailure(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{
		createOrderFunc: func(ctx context.Context, req paypal.CreateOrderRequest, idempotencyKey string) (*paypal.Order, error) {
			return nil, &paypal.APIError{
				StatusCode: http.StatusUnprocessableEntity,
				Name:       "UNPROCESSABLE_ENTITY",
				Message:    "PayPal rejected the order parameters",
				DebugID:    "debug-paypal-123",
			}
		},
	}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Item",
		MerchantName: "Merchant",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway on PayPal API error, got %d: %s", w.Code, w.Body.String())
	}
}

// 16. PayPal timeout returns 504 Gateway Timeout
func TestPayment_CreateOrder_PayPalTimeout(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{
		createOrderFunc: func(ctx context.Context, req paypal.CreateOrderRequest, idempotencyKey string) (*paypal.Order, error) {
			return nil, paypal.ErrTimeout
		},
	}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Item",
		MerchantName: "Merchant",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 Gateway Timeout on PayPal timeout, got %d: %s", w.Code, w.Body.String())
	}
}

// 17. Database persistence failure after PayPal order returns 500
func TestPayment_CreateOrder_DatabasePersistenceFailure(t *testing.T) {
	mq := newMockPaymentQuerier()
	mq.failCreatePayment = true // Simulate DB insert failure after PayPal created order
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Item",
		MerchantName: "Merchant",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error when DB write fails, got %d: %s", w.Code, w.Body.String())
	}
}

// 18. Confirmation that order creation NEVER marks payment completed or purchase approved
func TestPayment_CreateOrder_NeverMarksPaymentCompleted(t *testing.T) {
	mq := newMockPaymentQuerier()
	mp := &mockPayPalClient{}
	const userID = int64(10)
	const purchaseID = int64(42)

	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR", AlertThresholdPercent: 80}
	mq.purchasesByID[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       userID,
		ItemName:     "Headphones",
		MerchantName: "Audio Store",
		Amount:       100000,
		Currency:     "INR",
		Status:       "proposed",
	}

	r := setupPaymentTestRouter(mq, mp, userID)
	body := fmt.Sprintf(`{"purchase_id": %d}`, purchaseID)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/create-order", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", w.Code)
	}

	// Verify purchase status remains strictly 'proposed'
	p := mq.purchasesByID[purchaseID]
	if p.Status != "proposed" {
		t.Errorf("purchase status must remain 'proposed', got '%s'", p.Status)
	}

	// Verify created payment is strictly 'awaiting_approval', never 'completed' or 'approved'
	payments := mq.paymentsByPurchase[purchaseID]
	if len(payments) != 1 {
		t.Fatalf("expected 1 payment record, got %d", len(payments))
	}
	if payments[0].Status != "awaiting_approval" {
		t.Errorf("payment status must be 'awaiting_approval', got '%s'", payments[0].Status)
	}
}
