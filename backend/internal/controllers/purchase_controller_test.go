package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "parishram-ai/backend/internal/db/sqlc"
	"parishram-ai/backend/internal/middleware"
)

// mockPurchaseQuerier implements db.Querier for testing purchase evaluation and proposal endpoints.
type mockPurchaseQuerier struct {
	db.Querier
	usersByID       map[int64]db.User
	budgetsByUser   map[int64]db.Budget
	purchasesByID   map[int64]db.Purchase
	purchasesByUser map[int64][]db.Purchase
	expensesByUser  map[int64][]db.Expense
	simulateDBErr   bool
	nextPurchaseID  int64
}

func newMockPurchaseQuerier() *mockPurchaseQuerier {
	return &mockPurchaseQuerier{
		usersByID:       make(map[int64]db.User),
		budgetsByUser:   make(map[int64]db.Budget),
		purchasesByID:   make(map[int64]db.Purchase),
		purchasesByUser: make(map[int64][]db.Purchase),
		expensesByUser:  make(map[int64][]db.Expense),
		nextPurchaseID:  1,
	}
}

func (m *mockPurchaseQuerier) GetUserByID(ctx context.Context, id int64) (db.User, error) {
	if m.simulateDBErr {
		return db.User{}, errors.New("simulated database failure on GetUserByID")
	}
	u, exists := m.usersByID[id]
	if !exists {
		return db.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (m *mockPurchaseQuerier) GetBudgetByUserID(ctx context.Context, userID int64) (db.Budget, error) {
	if m.simulateDBErr {
		return db.Budget{}, errors.New("simulated database failure on GetBudgetByUserID")
	}
	b, exists := m.budgetsByUser[userID]
	if !exists {
		return db.Budget{}, pgx.ErrNoRows
	}
	return b, nil
}

func (m *mockPurchaseQuerier) GetMonthlyExpenseTotalForUser(ctx context.Context, arg db.GetMonthlyExpenseTotalForUserParams) (int64, error) {
	if m.simulateDBErr {
		return 0, errors.New("simulated database failure on GetMonthlyExpenseTotalForUser")
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

func (m *mockPurchaseQuerier) CreatePurchaseProposal(ctx context.Context, arg db.CreatePurchaseProposalParams) (db.Purchase, error) {
	if m.simulateDBErr {
		return db.Purchase{}, errors.New("simulated database failure on CreatePurchaseProposal")
	}
	now := time.Now().UTC()
	ts := pgtype.Timestamptz{Time: now, Valid: true}

	p := db.Purchase{
		ID:           m.nextPurchaseID,
		UserID:       arg.UserID,
		MerchantName: arg.MerchantName,
		ItemName:     arg.ItemName,
		Amount:       arg.Amount,
		Currency:     arg.Currency,
		Status:       arg.Status,
		RiskScore:    arg.RiskScore,
		RiskFactors:  arg.RiskFactors,
		Notes:        arg.Notes,
		CreatedAt:    ts,
		UpdatedAt:    ts,
	}
	m.nextPurchaseID++
	m.purchasesByID[p.ID] = p
	m.purchasesByUser[arg.UserID] = append(m.purchasesByUser[arg.UserID], p)
	return p, nil
}

func (m *mockPurchaseQuerier) GetPurchaseByIDAndUser(ctx context.Context, arg db.GetPurchaseByIDAndUserParams) (db.Purchase, error) {
	if m.simulateDBErr {
		return db.Purchase{}, errors.New("simulated database failure on GetPurchaseByIDAndUser")
	}
	p, exists := m.purchasesByID[arg.ID]
	if !exists || p.UserID != arg.UserID {
		return db.Purchase{}, pgx.ErrNoRows
	}
	return p, nil
}

func (m *mockPurchaseQuerier) ListPurchasesByUserID(ctx context.Context, arg db.ListPurchasesByUserIDParams) ([]db.Purchase, error) {
	if m.simulateDBErr {
		return nil, errors.New("simulated database failure on ListPurchasesByUserID")
	}
	userPurchases := make([]db.Purchase, len(m.purchasesByUser[arg.UserID]))
	copy(userPurchases, m.purchasesByUser[arg.UserID])

	sort.Slice(userPurchases, func(i, j int) bool {
		return userPurchases[i].CreatedAt.Time.After(userPurchases[j].CreatedAt.Time)
	})

	offset := int(arg.Offset)
	limit := int(arg.Limit)
	if offset >= len(userPurchases) {
		return []db.Purchase{}, nil
	}
	end := offset + limit
	if end > len(userPurchases) {
		end = len(userPurchases)
	}
	return userPurchases[offset:end], nil
}

func setupPurchaseTestRouter(querier db.Querier, authenticatedUserID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	purchaseCtrl := NewPurchaseController(querier)

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
		protected.POST("/purchases/evaluate", purchaseCtrl.EvaluatePurchase)
		protected.GET("/purchases", purchaseCtrl.ListPurchases)
		protected.GET("/purchases/:id", purchaseCtrl.GetPurchase)
	}

	return r
}

// 1. Unauthenticated requests are rejected
func TestPurchase_UnauthenticatedRequests_Rejected(t *testing.T) {
	mq := newMockPurchaseQuerier()
	r := setupPurchaseTestRouter(mq, 0) // 0 means unauthenticated

	endpoints := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodPost, "/api/v1/purchases/evaluate", `{"item_name":"Shoes","merchant":"Store","amount":50000,"currency":"INR"}`},
		{http.MethodGet, "/api/v1/purchases", ""},
		{http.MethodGet, "/api/v1/purchases/1", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.url, func(t *testing.T) {
			var req *http.Request
			if ep.body != "" {
				req, _ = http.NewRequest(ep.method, ep.url, bytes.NewBufferString(ep.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req, _ = http.NewRequest(ep.method, ep.url, nil)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 Unauthorized for %s %s, got %d", ep.method, ep.url, w.Code)
			}
		})
	}
}

// 2. Authenticated user can evaluate a purchase and gets structured findings
func TestPurchase_EvaluatePurchase_Success(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(10)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{
		UserID:                userID,
		MonthlyLimit:          500000, // ₹5,000 in paise
		Currency:              "INR",
		AlertThresholdPercent: 80,
	}

	// Existing spending of ₹1,000 (100000 paise) in the current month
	now := time.Now().UTC()
	mq.expensesByUser[userID] = []db.Expense{
		{
			ID:       1,
			UserID:   userID,
			Amount:   100000,
			Currency: "INR",
			SpentAt:  pgtype.Timestamptz{Time: now, Valid: true},
		},
	}

	r := setupPurchaseTestRouter(mq, userID)

	// Proposed purchase of ₹2,000 (200000 paise)
	body := `{
		"item_name": "Wireless headphones",
		"merchant": "Audio Hub",
		"amount": 200000,
		"currency": "INR",
		"description": "Noise-cancelling headphones"
	}`

	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on evaluate, got %d: %s", w.Code, w.Body.String())
	}

	var resp PurchaseEvaluationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.PurchaseID == 0 {
		t.Errorf("expected non-zero purchase ID")
	}
	if resp.Status != "proposed" {
		t.Errorf("expected status 'proposed', got %s", resp.Status)
	}
	if !resp.CanProceed {
		t.Errorf("expected CanProceed=true")
	}
	if resp.MonthlyBudget != 500000 {
		t.Errorf("expected MonthlyBudget=500000, got %d", resp.MonthlyBudget)
	}
	if resp.CurrentMonthlySpending != 100000 {
		t.Errorf("expected CurrentMonthlySpending=100000, got %d", resp.CurrentMonthlySpending)
	}
	if resp.RemainingBefore != 400000 {
		t.Errorf("expected RemainingBefore=400000, got %d", resp.RemainingBefore)
	}
	if resp.RemainingAfter != 200000 {
		t.Errorf("expected RemainingAfter=200000, got %d", resp.RemainingAfter)
	}
	if resp.UtilizationBeforePercent != 20 {
		t.Errorf("expected UtilizationBefore=20%%, got %d%%", resp.UtilizationBeforePercent)
	}
	if resp.UtilizationAfterPercent != 60 {
		t.Errorf("expected UtilizationAfter=60%%, got %d%%", resp.UtilizationAfterPercent)
	}
	if resp.ExceedsBudget {
		t.Errorf("expected ExceedsBudget=false")
	}
	if resp.RiskScore != 0 {
		t.Errorf("expected RiskScore=0, got %d", resp.RiskScore)
	}
	if len(resp.Findings) == 0 {
		t.Errorf("expected findings to contain observations")
	}
}

// 3. Missing budget returns a clear error
func TestPurchase_MissingBudget_Rejected(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(11)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	// User has NO budget record

	r := setupPurchaseTestRouter(mq, userID)

	body := `{"item_name":"Shoes","merchant":"Store","amount":50000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when budget is missing, got %d: %s", w.Code, w.Body.String())
	}

	var errResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["code"] != "BUDGET_NOT_CONFIGURED" {
		t.Errorf("expected code BUDGET_NOT_CONFIGURED, got %v", errResp["code"])
	}
}

// 4. Missing required purchase fields are rejected
func TestPurchase_MissingRequiredFields_Rejected(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(12)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR"}
	r := setupPurchaseTestRouter(mq, userID)

	testCases := []struct {
		name string
		body string
	}{
		{"missing item_name", `{"merchant":"Store","amount":50000,"currency":"INR"}`},
		{"empty item_name", `{"item_name":"  ","merchant":"Store","amount":50000,"currency":"INR"}`},
		{"missing merchant", `{"item_name":"Shoes","amount":50000,"currency":"INR"}`},
		{"empty merchant", `{"item_name":"Shoes","merchant":"","amount":50000,"currency":"INR"}`},
		{"missing amount", `{"item_name":"Shoes","merchant":"Store","currency":"INR"}`},
		{"missing currency", `{"item_name":"Shoes","merchant":"Store","amount":50000}`},
		{"empty json", `{}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for %s, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// 5. Invalid amount is rejected (zero and negative)
func TestPurchase_InvalidAmount_Rejected(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(13)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR"}
	r := setupPurchaseTestRouter(mq, userID)

	for _, amt := range []int64{0, -1, -10000} {
		t.Run(fmt.Sprintf("amt_%d", amt), func(t *testing.T) {
			body := fmt.Sprintf(`{"item_name":"Watch","merchant":"Store","amount":%d,"currency":"INR"}`, amt)
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for amount %d, got %d: %s", amt, w.Code, w.Body.String())
			}
		})
	}
}

// 6. Invalid currency format is rejected
func TestPurchase_InvalidCurrency_Rejected(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(14)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR"}
	r := setupPurchaseTestRouter(mq, userID)

	invalidCurrs := []string{"inr", "IN", "INRR", "123", "   ", "US$"}
	for _, c := range invalidCurrs {
		t.Run(c, func(t *testing.T) {
			body := fmt.Sprintf(`{"item_name":"Watch","merchant":"Store","amount":50000,"currency":%q}`, c)
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for currency %q, got %d", c, w.Code)
			}
		})
	}
}

// 7. Currency mismatch against user profile or budget is rejected
func TestPurchase_CurrencyMismatch_Rejected(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(15)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR"}
	r := setupPurchaseTestRouter(mq, userID)

	// User account is INR, purchase specifies USD
	body := `{"item_name":"SaaS","merchant":"US Cloud","amount":5000,"currency":"USD"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on currency mismatch, got %d: %s", w.Code, w.Body.String())
	}

	var errResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["code"] != "CURRENCY_MISMATCH" {
		t.Errorf("expected code CURRENCY_MISMATCH, got %v", errResp["code"])
	}
}

// 8. Current expenses are loaded from the database, not the request
// 9. Client cannot override budget or spending data
func TestPurchase_FinancialState_LoadedFromDatabaseOnly(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(16)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{
		UserID:       userID,
		MonthlyLimit: 500000, // ₹5,000 real budget
		Currency:     "INR",
	}

	// Real spending in DB: ₹3,000 (300000 paise)
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

	r := setupPurchaseTestRouter(mq, userID)

	// Malicious/tampered request body attempting to supply fake financial state
	body := `{
		"item_name": "Watch",
		"merchant": "Store",
		"amount": 250000,
		"currency": "INR",
		"monthly_budget": 99999999,
		"current_monthly_spending": 0,
		"spending_limit": 99999999,
		"risk_score": 0,
		"can_proceed": true
	}`

	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var resp PurchaseEvaluationResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Verify server ignored all client-supplied budget/spending state and used true DB values:
	if resp.MonthlyBudget != 500000 {
		t.Errorf("security violation: client overrode MonthlyBudget! Got %d", resp.MonthlyBudget)
	}
	if resp.CurrentMonthlySpending != 300000 {
		t.Errorf("security violation: client overrode CurrentMonthlySpending! Got %d", resp.CurrentMonthlySpending)
	}
	// 300,000 + 250,000 = 550,000 > 500,000 budget! Must exceed budget!
	if !resp.ExceedsBudget {
		t.Errorf("expected ExceedsBudget=true based on real DB state, got false")
	}
	if resp.CanProceed {
		t.Errorf("security violation: client forced CanProceed=true despite budget excess!")
	}
}

// 10. Spending limit is loaded from the database
// 12. Spending-limit-exceeded result is returned correctly
func TestPurchase_SpendingLimit_LoadedFromDatabaseAndEnforced(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(17)
	mq.usersByID[userID] = db.User{
		ID:            userID,
		Currency:      "INR",
		SpendingLimit: 100000, // ₹1,000 single-purchase limit in paise
	}
	mq.budgetsByUser[userID] = db.Budget{
		UserID:       userID,
		MonthlyLimit: 1000000, // ₹10,000 large monthly budget
		Currency:     "INR",
	}

	r := setupPurchaseTestRouter(mq, userID)

	// Purchase amount ₹1,500 (150000 paise) exceeds the ₹1,000 limit, though within monthly budget
	body := `{"item_name":"Drone","merchant":"Gadgets","amount":150000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var resp PurchaseEvaluationResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.SpendingLimit == nil || *resp.SpendingLimit != 100000 {
		t.Errorf("expected SpendingLimit 100000 from database, got %v", resp.SpendingLimit)
	}
	if !resp.ExceedsSpendingLimit {
		t.Errorf("expected ExceedsSpendingLimit=true")
	}
	if resp.ExceedsBudget {
		t.Errorf("expected ExceedsBudget=false (within monthly budget)")
	}
	if resp.CanProceed {
		t.Errorf("expected CanProceed=false when spending limit is exceeded")
	}
}

// 11. Budget-exceeded result is returned correctly
func TestPurchase_BudgetExceeded_ResultCorrect(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(18)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{
		UserID:       userID,
		MonthlyLimit: 500000, // ₹5,000
		Currency:     "INR",
	}

	// Prior spending of ₹4,000 (400000 paise)
	now := time.Now().UTC()
	mq.expensesByUser[userID] = []db.Expense{
		{
			ID:       1,
			UserID:   userID,
			Amount:   400000,
			Currency: "INR",
			SpentAt:  pgtype.Timestamptz{Time: now, Valid: true},
		},
	}

	r := setupPurchaseTestRouter(mq, userID)

	// Purchase ₹2,000 (200000 paise) pushes spending to ₹6,000 (> ₹5,000 budget by ₹1,000)
	body := `{"item_name":"Coat","merchant":"Apparel","amount":200000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var resp PurchaseEvaluationResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if !resp.ExceedsBudget {
		t.Errorf("expected ExceedsBudget=true")
	}
	if resp.CanProceed {
		t.Errorf("expected CanProceed=false")
	}
	if resp.RemainingAfter != -100000 {
		t.Errorf("expected RemainingAfter=-100000 (exceeded by ₹1,000), got %d", resp.RemainingAfter)
	}
	if resp.RiskScore < 45 {
		t.Errorf("expected RiskScore >= 45 for budget excess, got %d", resp.RiskScore)
	}
}

// 13. Exact budget boundary behaves correctly (remaining = 0, utilization = 100%, can_proceed = true)
func TestPurchase_ExactBudgetBoundary_BehavesCorrectly(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(19)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{
		UserID:                userID,
		MonthlyLimit:          500000,
		Currency:              "INR",
		AlertThresholdPercent: 80,
	}

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

	r := setupPurchaseTestRouter(mq, userID)

	// Exactly ₹2,000 (200000 paise): 300,000 + 200,000 = 500,000 exactly
	body := `{"item_name":"Headset","merchant":"TechStore","amount":200000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var resp PurchaseEvaluationResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.RemainingAfter != 0 {
		t.Errorf("expected RemainingAfter=0 at exact boundary, got %d", resp.RemainingAfter)
	}
	if resp.UtilizationAfterPercent != 100 {
		t.Errorf("expected UtilizationAfter=100%%, got %d%%", resp.UtilizationAfterPercent)
	}
	if resp.ExceedsBudget {
		t.Errorf("expected ExceedsBudget=false at exact boundary")
	}
	if !resp.CanProceed {
		t.Errorf("expected CanProceed=true when exactly reaching budget limit")
	}
}

// 14. No existing expenses returns zero spending
func TestPurchase_NoExistingExpenses_ReturnsZeroSpending(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(21)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{
		UserID:       userID,
		MonthlyLimit: 500000,
		Currency:     "INR",
	}
	// Zero expenses recorded

	r := setupPurchaseTestRouter(mq, userID)

	body := `{"item_name":"Book","merchant":"Bookstore","amount":50000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var resp PurchaseEvaluationResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.CurrentMonthlySpending != 0 {
		t.Errorf("expected CurrentMonthlySpending=0 when no expenses exist, got %d", resp.CurrentMonthlySpending)
	}
	if resp.RemainingBefore != 500000 {
		t.Errorf("expected RemainingBefore=500000, got %d", resp.RemainingBefore)
	}
}

// 15. Monthly period boundaries are correct for Asia/Kolkata
func TestPurchase_MonthlyPeriodBoundaries_AsiaKolkata(t *testing.T) {
	// Test mid-November 2026: 2026-11-15 10:00:00 UTC = 2026-11-15 15:30:00 IST
	refNov := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	start, end, err := GetCurrentISTMonthlyPeriod(refNov)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1st Nov 2026 00:00:00 IST is 31st Oct 2026 18:30:00 UTC
	expectedStart := time.Date(2026, 10, 31, 18, 30, 0, 0, time.UTC)
	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}

	// 1st Dec 2026 00:00:00 IST is 30th Nov 2026 18:30:00 UTC
	expectedEnd := time.Date(2026, 11, 30, 18, 30, 0, 0, time.UTC)
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}

	// Test boundary transition in December: 2026-12-31 20:00:00 UTC
	// In IST (+5:30), 20:00:00 UTC + 5:30 = 01:30:00 on Jan 1st 2027!
	// So reference time is already in January 2027 IST!
	refNewYearIST := time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC)
	startNY, endNY, err := GetCurrentISTMonthlyPeriod(refNewYearIST)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// January 2027 IST starts at 2026-12-31 18:30:00 UTC
	expectedStartNY := time.Date(2026, 12, 31, 18, 30, 0, 0, time.UTC)
	if !startNY.Equal(expectedStartNY) {
		t.Errorf("expected New Year start %v, got %v", expectedStartNY, startNY)
	}

	// February 2027 IST starts at 2027-01-31 18:30:00 UTC
	expectedEndNY := time.Date(2027, 1, 31, 18, 30, 0, 0, time.UTC)
	if !endNY.Equal(expectedEndNY) {
		t.Errorf("expected New Year end %v, got %v", expectedEndNY, endNY)
	}
}

// 16. Database failures are handled correctly
func TestPurchase_DatabaseErrors_Handled(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(22)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 500000, Currency: "INR"}
	mq.simulateDBErr = true

	r := setupPurchaseTestRouter(mq, userID)

	body := `{"item_name":"Shoes","merchant":"Store","amount":50000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 Internal Server Error on DB failure, got %d: %s", w.Code, w.Body.String())
	}
}

// 17. Purchase evaluation never marks a purchase as paid or approved
func TestPurchase_NeverMarkedApprovedOrPaidOnEvaluation(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(23)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 1000000, Currency: "INR"}

	r := setupPurchaseTestRouter(mq, userID)

	// An easily affordable purchase with zero risk
	body := `{"item_name":"Coffee","merchant":"Cafe","amount":15000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var resp PurchaseEvaluationResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Ensure the returned status is strictly "proposed"
	if resp.Status != "proposed" {
		t.Errorf("critical invariant violated: evaluation returned status %s instead of 'proposed'", resp.Status)
	}

	// Verify database record has status "proposed"
	saved, exists := mq.purchasesByID[resp.PurchaseID]
	if !exists {
		t.Fatalf("persisted purchase not found in database")
	}
	if saved.Status != "proposed" {
		t.Errorf("critical invariant violated: database record marked status %s instead of 'proposed'", saved.Status)
	}
}

// 18. User A cannot retrieve User B's purchase evaluation
func TestPurchase_UserIsolation_CannotRetrieveOtherUserPurchase(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userA = int64(101)
	const userB = int64(102)

	mq.usersByID[userA] = db.User{ID: userA, Currency: "INR"}
	mq.usersByID[userB] = db.User{ID: userB, Currency: "INR"}
	mq.budgetsByUser[userB] = db.Budget{UserID: userB, MonthlyLimit: 500000, Currency: "INR"}

	// User B evaluates and persists a purchase proposal
	rB := setupPurchaseTestRouter(mq, userB)
	bodyB := `{"item_name":"UserB Secret Item","merchant":"VendorB","amount":250000,"currency":"INR"}`
	reqB, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(bodyB))
	reqB.Header.Set("Content-Type", "application/json")
	wB := httptest.NewRecorder()
	rB.ServeHTTP(wB, reqB)

	if wB.Code != http.StatusCreated {
		t.Fatalf("User B evaluate failed: %s", wB.Body.String())
	}
	var respB PurchaseEvaluationResponse
	json.Unmarshal(wB.Body.Bytes(), &respB)
	purchaseID := respB.PurchaseID

	// User A attempts to GET User B's purchase
	rA := setupPurchaseTestRouter(mq, userA)
	reqA, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/purchases/%d", purchaseID), nil)
	wA := httptest.NewRecorder()
	rA.ServeHTTP(wA, reqA)

	if wA.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when User A accesses User B's purchase, got %d", wA.Code)
	}

	// User B can retrieve their own purchase
	reqBGet, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/purchases/%d", purchaseID), nil)
	wBGet := httptest.NewRecorder()
	rB.ServeHTTP(wBGet, reqBGet)

	if wBGet.Code != http.StatusOK {
		t.Fatalf("expected User B to retrieve own purchase with 200 OK, got %d", wBGet.Code)
	}
	var detailsB PurchaseDetailsResponse
	json.Unmarshal(wBGet.Body.Bytes(), &detailsB)
	if detailsB.ID != purchaseID || detailsB.UserID != userB {
		t.Errorf("User B purchase retrieval mismatch: %+v", detailsB)
	}
	if !detailsB.CanProceed {
		t.Errorf("expected CanProceed=true for affordable purchase proposal, got false")
	}
}

// 17. Test that retrieving a purchase that exceeded budget returns CanProceed=false
func TestPurchase_GetPurchase_CanProceed_FalseWhenExceeded(t *testing.T) {
	mq := newMockPurchaseQuerier()
	const userID = int64(99)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, MonthlyLimit: 100000, Currency: "INR", AlertThresholdPercent: 80}

	r := setupPurchaseTestRouter(mq, userID)

	// Evaluate purchase that exceeds budget (200000 > 100000)
	body := `{"item_name":"Expensive Item","merchant":"Luxury Brand","amount":200000,"currency":"INR"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/purchases/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on evaluate, got %d", w.Code)
	}

	var evalResp PurchaseEvaluationResponse
	json.Unmarshal(w.Body.Bytes(), &evalResp)
	if evalResp.CanProceed {
		t.Errorf("expected evalResp.CanProceed=false")
	}

	// Retrieve via GET /api/v1/purchases/:id
	getReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/purchases/%d", evalResp.PurchaseID), nil)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", getW.Code)
	}

	var details PurchaseDetailsResponse
	json.Unmarshal(getW.Body.Bytes(), &details)
	if details.CanProceed {
		t.Errorf("expected details.CanProceed=false for budget-exceeding purchase proposal")
	}
}
