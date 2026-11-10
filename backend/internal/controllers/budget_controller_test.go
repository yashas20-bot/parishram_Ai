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
)

// mockBudgetQuerier implements db.Querier for testing budget controllers.
type mockBudgetQuerier struct {
	db.Querier
	budgetsByUser map[int64]db.Budget
	usersByID     map[int64]db.User
	simulateDBErr bool
	nextBudgetID  int64
}

func newMockBudgetQuerier() *mockBudgetQuerier {
	return &mockBudgetQuerier{
		budgetsByUser: make(map[int64]db.Budget),
		usersByID:     make(map[int64]db.User),
		nextBudgetID:  1,
	}
}

func (m *mockBudgetQuerier) GetBudgetByUserID(ctx context.Context, userID int64) (db.Budget, error) {
	if m.simulateDBErr {
		return db.Budget{}, errors.New("simulated database failure on GetBudgetByUserID")
	}
	b, exists := m.budgetsByUser[userID]
	if !exists {
		return db.Budget{}, pgx.ErrNoRows
	}
	return b, nil
}

func (m *mockBudgetQuerier) CreateOrUpdateBudget(ctx context.Context, arg db.CreateOrUpdateBudgetParams) (db.Budget, error) {
	if m.simulateDBErr {
		return db.Budget{}, errors.New("simulated database failure on CreateOrUpdateBudget")
	}
	now := time.Now().UTC()
	ts := pgtype.Timestamptz{Time: now, Valid: true}

	existing, exists := m.budgetsByUser[arg.UserID]
	if exists {
		existing.MonthlyLimit = arg.MonthlyLimit
		existing.AlertThresholdPercent = arg.AlertThresholdPercent
		existing.Currency = arg.Currency
		existing.UpdatedAt = ts
		m.budgetsByUser[arg.UserID] = existing
		return existing, nil
	}

	b := db.Budget{
		ID:                    m.nextBudgetID,
		UserID:                arg.UserID,
		MonthlyLimit:          arg.MonthlyLimit,
		AlertThresholdPercent: arg.AlertThresholdPercent,
		Currency:              arg.Currency,
		CreatedAt:             ts,
		UpdatedAt:             ts,
	}
	m.nextBudgetID++
	m.budgetsByUser[arg.UserID] = b
	return b, nil
}

func (m *mockBudgetQuerier) GetUserByID(ctx context.Context, id int64) (db.User, error) {
	if m.simulateDBErr {
		return db.User{}, errors.New("simulated database failure on GetUserByID")
	}
	u, exists := m.usersByID[id]
	if !exists {
		return db.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func setupBudgetTestRouter(querier db.Querier, authenticatedUserID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	budgetCtrl := NewBudgetController(querier)

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
		protected.GET("/budgets/me", budgetCtrl.GetBudget)
		protected.PUT("/budgets/me", budgetCtrl.UpdateBudget)
	}

	return r
}

// 1. Unauthenticated requests are rejected (both GET and PUT)
func TestBudget_UnauthenticatedRequests_Rejected(t *testing.T) {
	mq := newMockBudgetQuerier()
	r := setupBudgetTestRouter(mq, 0) // 0 means unauthenticated

	// Test GET /api/v1/budgets/me
	{
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/budgets/me", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected GET without auth to return 401, got %d", w.Code)
		}
	}

	// Test PUT /api/v1/budgets/me
	{
		body := `{"monthly_limit": 500000, "currency": "INR", "alert_threshold_percent": 80}`
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected PUT without auth to return 401, got %d", w.Code)
		}
	}
}

// 2. Authenticated user can create a budget
func TestBudget_CreateBudget_Success(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(42)
	mq.usersByID[userID] = db.User{
		ID:       userID,
		Email:    "user42@example.com",
		Name:     "Test User 42",
		Currency: "INR",
	}

	r := setupBudgetTestRouter(mq, userID)

	body := `{"monthly_limit": 500000, "currency": "INR", "alert_threshold_percent": 80}`
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on create budget, got %d: %s", w.Code, w.Body.String())
	}

	var resp BudgetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.UserID != userID {
		t.Errorf("expected user_id %d, got %d", userID, resp.UserID)
	}
	if resp.MonthlyLimit != 500000 {
		t.Errorf("expected monthly_limit 500000, got %d", resp.MonthlyLimit)
	}
	if resp.Currency != "INR" {
		t.Errorf("expected currency INR, got %s", resp.Currency)
	}
	if resp.AlertThresholdPercent != 80 {
		t.Errorf("expected alert_threshold_percent 80, got %d", resp.AlertThresholdPercent)
	}
	if resp.ID == 0 {
		t.Errorf("expected non-zero budget ID")
	}
}

// 3. Authenticated user can retrieve their budget
func TestBudget_GetBudget_Success(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(10)
	now := time.Now().UTC()
	mq.budgetsByUser[userID] = db.Budget{
		ID:                    1,
		UserID:                userID,
		MonthlyLimit:          750000, // ₹7,500 in paise
		AlertThresholdPercent: 85,
		Currency:              "INR",
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	}

	r := setupBudgetTestRouter(mq, userID)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/budgets/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp BudgetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.UserID != userID {
		t.Errorf("expected user_id %d, got %d", userID, resp.UserID)
	}
	if resp.MonthlyLimit != 750000 {
		t.Errorf("expected monthly_limit 750000, got %d", resp.MonthlyLimit)
	}
	if resp.Currency != "INR" {
		t.Errorf("expected currency INR, got %s", resp.Currency)
	}
	if resp.AlertThresholdPercent != 85 {
		t.Errorf("expected alert_threshold_percent 85, got %d", resp.AlertThresholdPercent)
	}
}

// 4. Authenticated user can update their budget
func TestBudget_UpdateBudget_Success(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(15)
	now := time.Now().UTC()
	mq.budgetsByUser[userID] = db.Budget{
		ID:                    1,
		UserID:                userID,
		MonthlyLimit:          500000,
		AlertThresholdPercent: 80,
		Currency:              "INR",
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	}

	r := setupBudgetTestRouter(mq, userID)

	updateBody := `{"monthly_limit": 900000, "currency": "INR", "alert_threshold_percent": 90}`
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(updateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on update, got %d: %s", w.Code, w.Body.String())
	}

	var resp BudgetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.MonthlyLimit != 900000 {
		t.Errorf("expected monthly_limit 900000, got %d", resp.MonthlyLimit)
	}
	if resp.AlertThresholdPercent != 90 {
		t.Errorf("expected alert_threshold_percent 90, got %d", resp.AlertThresholdPercent)
	}
	if resp.Currency != "INR" {
		t.Errorf("expected currency INR, got %s", resp.Currency)
	}

	// Verify persistence in querier
	saved := mq.budgetsByUser[userID]
	if saved.MonthlyLimit != 900000 || saved.AlertThresholdPercent != 90 {
		t.Errorf("persisted budget mismatch: %+v", saved)
	}
}

// 5. User A cannot access or modify User B's budget
func TestBudget_UserIsolation_CannotAccessOtherUserBudget(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userA = int64(101)
	const userB = int64(102)

	now := time.Now().UTC()
	mq.budgetsByUser[userA] = db.Budget{
		ID:                    1,
		UserID:                userA,
		MonthlyLimit:          500000,
		AlertThresholdPercent: 80,
		Currency:              "INR",
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	}

	mq.usersByID[userB] = db.User{
		ID:       userB,
		Email:    "userB@example.com",
		Currency: "INR",
	}

	// User B tries to read their own budget (which doesn't exist yet)
	rB := setupBudgetTestRouter(mq, userB)
	reqGet, _ := http.NewRequest(http.MethodGet, "/api/v1/budgets/me", nil)
	wGet := httptest.NewRecorder()
	rB.ServeHTTP(wGet, reqGet)

	if wGet.Code != http.StatusNotFound {
		t.Fatalf("expected User B to get 404 when having no budget, got %d", wGet.Code)
	}

	// Verify User A's budget was untouched and cannot be read by User B
	rA := setupBudgetTestRouter(mq, userA)
	reqGetA, _ := http.NewRequest(http.MethodGet, "/api/v1/budgets/me", nil)
	wGetA := httptest.NewRecorder()
	rA.ServeHTTP(wGetA, reqGetA)

	if wGetA.Code != http.StatusOK {
		t.Fatalf("expected User A to get 200 OK, got %d", wGetA.Code)
	}
	var respA BudgetResponse
	json.Unmarshal(wGetA.Body.Bytes(), &respA)
	if respA.UserID != userA || respA.MonthlyLimit != 500000 {
		t.Errorf("User A's budget corrupted: %+v", respA)
	}

	// User B creates their own budget
	bodyB := `{"monthly_limit": 250000, "currency": "INR", "alert_threshold_percent": 70}`
	reqPutB, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(bodyB))
	reqPutB.Header.Set("Content-Type", "application/json")
	wPutB := httptest.NewRecorder()
	rB.ServeHTTP(wPutB, reqPutB)

	if wPutB.Code != http.StatusOK {
		t.Fatalf("expected User B create to succeed, got %d: %s", wPutB.Code, wPutB.Body.String())
	}

	// Ensure User A's budget is still strictly isolated and unchanged
	savedA := mq.budgetsByUser[userA]
	if savedA.MonthlyLimit != 500000 || savedA.AlertThresholdPercent != 80 {
		t.Errorf("User A's budget was modified by User B action: %+v", savedA)
	}
}

// 6. Missing required fields are rejected
func TestBudget_MissingRequiredFields_Rejected(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(1)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupBudgetTestRouter(mq, userID)

	testCases := []struct {
		name string
		body string
	}{
		{"missing monthly_limit", `{"currency": "INR", "alert_threshold_percent": 80}`},
		{"missing currency", `{"monthly_limit": 500000, "alert_threshold_percent": 80}`},
		{"empty json object", `{}`},
		{"malformed json", `{"monthly_limit":`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for %s, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// 7. Invalid currency is rejected
func TestBudget_InvalidCurrency_Rejected(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(2)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupBudgetTestRouter(mq, userID)

	invalidFormatCases := []struct {
		name     string
		currency string
	}{
		{"lowercase", "inr"},
		{"too short", "IN"},
		{"too long", "INRR"},
		{"numeric", "123"},
		{"symbols", "IN$"},
		{"spaces", "   "},
		{"empty string", ""},
	}

	for _, tc := range invalidFormatCases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"monthly_limit": 500000, "currency": %q}`, tc.currency)
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for currency %q, got %d: %s", tc.currency, w.Code, w.Body.String())
			}
		})
	}

	// Inconsistent currency test 1: New budget currency doesn't match user's account currency
	t.Run("inconsistent with user account currency", func(t *testing.T) {
		body := `{"monthly_limit": 500000, "currency": "USD"}` // User is INR
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for currency mismatch with user, got %d: %s", w.Code, w.Body.String())
		}
	})

	// Inconsistent currency test 2: Existing budget in INR, client attempts update to USD
	t.Run("inconsistent currency update on existing budget", func(t *testing.T) {
		const existingUser = int64(3)
		mq.usersByID[existingUser] = db.User{ID: existingUser, Currency: "INR"}
		mq.budgetsByUser[existingUser] = db.Budget{
			ID:           5,
			UserID:       existingUser,
			MonthlyLimit: 500000,
			Currency:     "INR",
		}
		rExisting := setupBudgetTestRouter(mq, existingUser)

		body := `{"monthly_limit": 600000, "currency": "USD"}`
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		rExisting.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for changing existing budget currency, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// 8. Invalid monetary amounts are rejected
func TestBudget_InvalidMonetaryAmounts_Rejected(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(4)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupBudgetTestRouter(mq, userID)

	testCases := []struct {
		name   string
		amount int64
	}{
		{"zero amount", 0},
		{"negative amount", -100},
		{"large negative amount", -500000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"monthly_limit": %d, "currency": "INR", "alert_threshold_percent": 80}`, tc.amount)
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for amount %d, got %d: %s", tc.amount, w.Code, w.Body.String())
			}
		})
	}
}

// 9. Invalid alert thresholds are rejected
func TestBudget_InvalidAlertThresholds_Rejected(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(5)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupBudgetTestRouter(mq, userID)

	testCases := []struct {
		name      string
		threshold int
	}{
		{"zero threshold", 0},
		{"negative threshold", -5},
		{"threshold above 100", 101},
		{"threshold 200", 200},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"monthly_limit": 500000, "currency": "INR", "alert_threshold_percent": %d}`, tc.threshold)
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for threshold %d, got %d: %s", tc.threshold, w.Code, w.Body.String())
			}
		})
	}
}

// 10. Database errors are handled correctly
func TestBudget_DatabaseErrors_Handled(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(6)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.simulateDBErr = true

	r := setupBudgetTestRouter(mq, userID)

	// Test GET with DB error
	{
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/budgets/me", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500 Internal Server Error on GET DB failure, got %d: %s", w.Code, w.Body.String())
		}
	}

	// Test PUT with DB error
	{
		body := `{"monthly_limit": 500000, "currency": "INR", "alert_threshold_percent": 80}`
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500 Internal Server Error on PUT DB failure, got %d: %s", w.Code, w.Body.String())
		}
	}
}

// 11. Existing budget values are not accidentally overwritten when fields are omitted
func TestBudget_ExistingBudgetValues_NotAccidentallyOverwritten(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(7)
	now := time.Now().UTC()

	// Initial budget setup with custom alert_threshold_percent = 65% (non-default)
	mq.budgetsByUser[userID] = db.Budget{
		ID:                    1,
		UserID:                userID,
		MonthlyLimit:          500000,
		AlertThresholdPercent: 65,
		Currency:              "INR",
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	}

	r := setupBudgetTestRouter(mq, userID)

	// Client sends PUT to update monthly_limit only, omitting alert_threshold_percent
	updateBody := `{"monthly_limit": 700000, "currency": "INR"}`
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/budgets/me", bytes.NewBufferString(updateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on update, got %d: %s", w.Code, w.Body.String())
	}

	var resp BudgetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.MonthlyLimit != 700000 {
		t.Errorf("expected monthly_limit 700000, got %d", resp.MonthlyLimit)
	}
	// Verify that the custom 65% threshold was preserved, NOT overwritten by the 80% default!
	if resp.AlertThresholdPercent != 65 {
		t.Errorf("expected alert_threshold_percent to be preserved as 65, got %d", resp.AlertThresholdPercent)
	}

	saved := mq.budgetsByUser[userID]
	if saved.AlertThresholdPercent != 65 {
		t.Errorf("persisted threshold was overwritten: %+v", saved)
	}
}

// 12. GetBudget returns 404 when budget has not been set yet
func TestBudget_GetBudget_NotFound(t *testing.T) {
	mq := newMockBudgetQuerier()
	const userID = int64(8)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}

	r := setupBudgetTestRouter(mq, userID)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/budgets/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for unset budget, got %d: %s", w.Code, w.Body.String())
	}

	var errResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["code"] != "BUDGET_NOT_FOUND" {
		t.Errorf("expected error code BUDGET_NOT_FOUND, got %v", errResp["code"])
	}
}
