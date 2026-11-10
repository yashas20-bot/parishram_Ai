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

// mockExpenseQuerier implements db.Querier for testing expense controllers and monthly aggregation.
type mockExpenseQuerier struct {
	db.Querier
	expensesByID   map[int64]db.Expense
	expensesByUser map[int64][]db.Expense
	usersByID      map[int64]db.User
	budgetsByUser  map[int64]db.Budget
	auditLogs      []db.AuditLog
	simulateDBErr  bool
	nextExpenseID  int64
}

func newMockExpenseQuerier() *mockExpenseQuerier {
	return &mockExpenseQuerier{
		expensesByID:   make(map[int64]db.Expense),
		expensesByUser: make(map[int64][]db.Expense),
		usersByID:      make(map[int64]db.User),
		budgetsByUser:  make(map[int64]db.Budget),
		auditLogs:      make([]db.AuditLog, 0),
		nextExpenseID:  1,
	}
}

func (m *mockExpenseQuerier) CreateExpense(ctx context.Context, arg db.CreateExpenseParams) (db.Expense, error) {
	if m.simulateDBErr {
		return db.Expense{}, errors.New("simulated database failure on CreateExpense")
	}
	now := time.Now().UTC()
	createdTS := pgtype.Timestamptz{Time: now, Valid: true}

	e := db.Expense{
		ID:          m.nextExpenseID,
		UserID:      arg.UserID,
		Category:    arg.Category,
		Amount:      arg.Amount,
		Currency:    arg.Currency,
		Description: arg.Description,
		SpentAt:     arg.SpentAt,
		CreatedAt:   createdTS,
	}
	m.nextExpenseID++
	m.expensesByID[e.ID] = e
	m.expensesByUser[arg.UserID] = append(m.expensesByUser[arg.UserID], e)
	return e, nil
}

func (m *mockExpenseQuerier) GetExpenseByIDAndUser(ctx context.Context, arg db.GetExpenseByIDAndUserParams) (db.Expense, error) {
	if m.simulateDBErr {
		return db.Expense{}, errors.New("simulated database failure on GetExpenseByIDAndUser")
	}
	e, exists := m.expensesByID[arg.ID]
	if !exists || e.UserID != arg.UserID {
		return db.Expense{}, pgx.ErrNoRows
	}
	return e, nil
}

func (m *mockExpenseQuerier) ListExpensesByUserID(ctx context.Context, arg db.ListExpensesByUserIDParams) ([]db.Expense, error) {
	if m.simulateDBErr {
		return nil, errors.New("simulated database failure on ListExpensesByUserID")
	}
	userExpenses := make([]db.Expense, len(m.expensesByUser[arg.UserID]))
	copy(userExpenses, m.expensesByUser[arg.UserID])

	// Sort newest first by spent_at DESC
	sort.Slice(userExpenses, func(i, j int) bool {
		return userExpenses[i].SpentAt.Time.After(userExpenses[j].SpentAt.Time)
	})

	offset := int(arg.Offset)
	limit := int(arg.Limit)

	if offset >= len(userExpenses) {
		return []db.Expense{}, nil
	}
	end := offset + limit
	if end > len(userExpenses) {
		end = len(userExpenses)
	}
	return userExpenses[offset:end], nil
}

func (m *mockExpenseQuerier) DeleteExpenseForUser(ctx context.Context, arg db.DeleteExpenseForUserParams) error {
	if m.simulateDBErr {
		return errors.New("simulated database failure on DeleteExpenseForUser")
	}
	e, exists := m.expensesByID[arg.ID]
	if !exists || e.UserID != arg.UserID {
		return nil // SQL DELETE returns 0 rows, no error
	}
	delete(m.expensesByID, arg.ID)

	// Remove from user slice
	filtered := make([]db.Expense, 0)
	for _, item := range m.expensesByUser[arg.UserID] {
		if item.ID != arg.ID {
			filtered = append(filtered, item)
		}
	}
	m.expensesByUser[arg.UserID] = filtered
	return nil
}

func (m *mockExpenseQuerier) CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error) {
	log := db.AuditLog{
		ID:         int64(len(m.auditLogs) + 1),
		UserID:     arg.UserID,
		Action:     arg.Action,
		EntityType: arg.EntityType,
		EntityID:   arg.EntityID,
		Details:    arg.Details,
		IpAddress:  arg.IpAddress,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
	m.auditLogs = append(m.auditLogs, log)
	return log, nil
}

func (m *mockExpenseQuerier) GetUserByID(ctx context.Context, id int64) (db.User, error) {
	if m.simulateDBErr {
		return db.User{}, errors.New("simulated database failure on GetUserByID")
	}
	u, exists := m.usersByID[id]
	if !exists {
		return db.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (m *mockExpenseQuerier) GetBudgetByUserID(ctx context.Context, userID int64) (db.Budget, error) {
	if m.simulateDBErr {
		return db.Budget{}, errors.New("simulated database failure on GetBudgetByUserID")
	}
	b, exists := m.budgetsByUser[userID]
	if !exists {
		return db.Budget{}, pgx.ErrNoRows
	}
	return b, nil
}

func (m *mockExpenseQuerier) GetMonthlyExpenseTotalForUser(ctx context.Context, arg db.GetMonthlyExpenseTotalForUserParams) (int64, error) {
	if m.simulateDBErr {
		return 0, errors.New("simulated database failure on GetMonthlyExpenseTotalForUser")
	}
	start := arg.SpentAt.Time
	end := arg.SpentAt_2.Time

	var total int64 = 0
	for _, e := range m.expensesByUser[arg.UserID] {
		if e.Currency != arg.Currency {
			continue // Never mix currencies
		}
		t := e.SpentAt.Time
		// Start-inclusive, end-exclusive interval [start, end)
		if (t.Equal(start) || t.After(start)) && t.Before(end) {
			total += e.Amount
		}
	}
	return total, nil
}

func (m *mockExpenseQuerier) GetMonthlyExpensesByCategory(ctx context.Context, arg db.GetMonthlyExpensesByCategoryParams) ([]db.GetMonthlyExpensesByCategoryRow, error) {
	if m.simulateDBErr {
		return nil, errors.New("simulated database failure on GetMonthlyExpensesByCategory")
	}
	start := arg.SpentAt.Time
	end := arg.SpentAt_2.Time

	catTotals := make(map[string]int64)
	catCounts := make(map[string]int64)

	for _, e := range m.expensesByUser[arg.UserID] {
		if e.Currency != arg.Currency {
			continue
		}
		t := e.SpentAt.Time
		if (t.Equal(start) || t.After(start)) && t.Before(end) {
			catTotals[e.Category] += e.Amount
			catCounts[e.Category]++
		}
	}

	rows := make([]db.GetMonthlyExpensesByCategoryRow, 0, len(catTotals))
	for cat, total := range catTotals {
		rows = append(rows, db.GetMonthlyExpensesByCategoryRow{
			Category:    cat,
			TotalAmount: total,
			Count:       catCounts[cat],
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].TotalAmount > rows[j].TotalAmount
	})
	return rows, nil
}

func setupExpenseTestRouter(querier db.Querier, authenticatedUserID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	expenseCtrl := NewExpenseController(querier)

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
		protected.POST("/expenses", expenseCtrl.CreateExpense)
		protected.GET("/expenses", expenseCtrl.ListExpenses)
		protected.GET("/expenses/:id", expenseCtrl.GetExpense)
		protected.DELETE("/expenses/:id", expenseCtrl.DeleteExpense)
	}

	return r
}

// 1. Unauthenticated requests are rejected
func TestExpense_UnauthenticatedRequests_Rejected(t *testing.T) {
	mq := newMockExpenseQuerier()
	r := setupExpenseTestRouter(mq, 0) // 0 means unauthenticated

	endpoints := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodPost, "/api/v1/expenses", `{"category":"Food","amount":50000,"currency":"INR"}`},
		{http.MethodGet, "/api/v1/expenses", ""},
		{http.MethodGet, "/api/v1/expenses/1", ""},
		{http.MethodDelete, "/api/v1/expenses/1", ""},
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

// 2. Authenticated user can create an expense
func TestExpense_CreateExpense_Success(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(10)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}

	r := setupExpenseTestRouter(mq, userID)

	body := `{
		"category": "Groceries",
		"amount": 250000,
		"currency": "INR",
		"description": "Weekly essentials at supermarket",
		"spent_at": "2026-11-05T14:30:00Z"
	}`

	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var resp ExpenseResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.UserID != userID {
		t.Errorf("expected user_id %d, got %d", userID, resp.UserID)
	}
	if resp.Category != "Groceries" {
		t.Errorf("expected category Groceries, got %s", resp.Category)
	}
	if resp.Amount != 250000 {
		t.Errorf("expected amount 250000, got %d", resp.Amount)
	}
	if resp.Currency != "INR" {
		t.Errorf("expected currency INR, got %s", resp.Currency)
	}
	if resp.Description != "Weekly essentials at supermarket" {
		t.Errorf("expected description Weekly essentials at supermarket, got %s", resp.Description)
	}
	expectedTime, _ := time.Parse(time.RFC3339, "2026-11-05T14:30:00Z")
	if !resp.SpentAt.Equal(expectedTime) {
		t.Errorf("expected spent_at %v, got %v", expectedTime, resp.SpentAt)
	}
}

// 3. Authenticated user can list their expenses
func TestExpense_ListExpenses_Success(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(20)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}

	// Seed 3 expenses for user 20 with different timestamps
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)

	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "Dining",
		Amount:   50000,
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: t1, Valid: true},
	})
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "Transport",
		Amount:   12000,
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: t2, Valid: true},
	})
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "Books",
		Amount:   35000,
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: t3, Valid: true},
	})

	r := setupExpenseTestRouter(mq, userID)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses?limit=10&offset=0", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp ListExpensesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Count != 3 {
		t.Errorf("expected count 3, got %d", resp.Count)
	}
	// Verify newest first ordering: Books (t3) -> Transport (t2) -> Dining (t1)
	if resp.Expenses[0].Category != "Books" {
		t.Errorf("expected first expense to be Books (newest), got %s", resp.Expenses[0].Category)
	}
	if resp.Expenses[1].Category != "Transport" {
		t.Errorf("expected second expense to be Transport, got %s", resp.Expenses[1].Category)
	}
	if resp.Expenses[2].Category != "Dining" {
		t.Errorf("expected third expense to be Dining, got %s", resp.Expenses[2].Category)
	}
}

// 4. Authenticated user can retrieve their own expense
func TestExpense_GetExpense_Success(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(30)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}

	created, _ := mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:      userID,
		Category:    "Utilities",
		Amount:      180000,
		Currency:    "INR",
		Description: "Electricity bill",
		SpentAt:     pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})

	r := setupExpenseTestRouter(mq, userID)

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/expenses/%d", created.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp ExpenseResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ID != created.ID || resp.Amount != 180000 || resp.Category != "Utilities" {
		t.Errorf("expense data mismatch: %+v", resp)
	}
}

// 5. User A cannot retrieve User B's expense
func TestExpense_UserIsolation_CannotRetrieveOtherUserExpense(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userA = int64(101)
	const userB = int64(102)

	mq.usersByID[userA] = db.User{ID: userA, Currency: "INR"}
	mq.usersByID[userB] = db.User{ID: userB, Currency: "INR"}

	// User B creates an expense
	expenseB, _ := mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:      userB,
		Category:    "Secret",
		Amount:      999999,
		Currency:    "INR",
		Description: "User B private expense",
		SpentAt:     pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})

	// User A tries to GET User B's expense ID
	rA := setupExpenseTestRouter(mq, userA)
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/expenses/%d", expenseB.ID), nil)
	w := httptest.NewRecorder()
	rA.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when User A accesses User B's expense, got %d", w.Code)
	}
}

// 6. User A cannot delete User B's expense
func TestExpense_UserIsolation_CannotDeleteOtherUserExpense(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userA = int64(201)
	const userB = int64(202)

	mq.usersByID[userA] = db.User{ID: userA, Currency: "INR"}
	mq.usersByID[userB] = db.User{ID: userB, Currency: "INR"}

	expenseB, _ := mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:      userB,
		Category:    "Protected",
		Amount:      500000,
		Currency:    "INR",
		Description: "Important expense",
		SpentAt:     pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})

	// User A attempts to DELETE User B's expense
	rA := setupExpenseTestRouter(mq, userA)
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/expenses/%d", expenseB.ID), nil)
	w := httptest.NewRecorder()
	rA.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when User A attempts deletion of User B expense, got %d", w.Code)
	}

	// Verify User B's expense remains intact and undeleted
	if _, exists := mq.expensesByID[expenseB.ID]; !exists {
		t.Errorf("User B's expense was improperly deleted!")
	}
}

// 7. Missing required fields are rejected
func TestExpense_MissingRequiredFields_Rejected(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(1)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupExpenseTestRouter(mq, userID)

	testCases := []struct {
		name string
		body string
	}{
		{"missing category", `{"amount": 50000, "currency": "INR"}`},
		{"empty category", `{"category": "   ", "amount": 50000, "currency": "INR"}`},
		{"missing amount", `{"category": "Food", "currency": "INR"}`},
		{"missing currency", `{"category": "Food", "amount": 50000}`},
		{"empty json", `{}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for %s, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// 8. Zero and negative amounts are rejected
func TestExpense_InvalidAmounts_Rejected(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(2)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupExpenseTestRouter(mq, userID)

	amounts := []int64{0, -1, -50000}
	for _, amt := range amounts {
		t.Run(fmt.Sprintf("amount_%d", amt), func(t *testing.T) {
			body := fmt.Sprintf(`{"category": "Food", "amount": %d, "currency": "INR"}`, amt)
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for amount %d, got %d: %s", amt, w.Code, w.Body.String())
			}
		})
	}
}

// 9. Invalid currencies are rejected
func TestExpense_InvalidCurrencies_Rejected(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(3)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupExpenseTestRouter(mq, userID)

	invalidCodes := []string{"inr", "IN", "INRR", "123", "   ", "US$", ""}
	for _, code := range invalidCodes {
		t.Run(fmt.Sprintf("curr_%s", code), func(t *testing.T) {
			body := fmt.Sprintf(`{"category": "Food", "amount": 50000, "currency": %q}`, code)
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for currency %q, got %d: %s", code, w.Code, w.Body.String())
			}
		})
	}
}

// 10. Currency mismatch against user profile or budget is rejected
func TestExpense_CurrencyMismatch_Handled(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(4)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.budgetsByUser[userID] = db.Budget{UserID: userID, Currency: "INR"}
	r := setupExpenseTestRouter(mq, userID)

	// Attempt to create expense in USD when account/budget is in INR
	body := `{"category": "Tech", "amount": 5000, "currency": "USD"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for currency mismatch, got %d: %s", w.Code, w.Body.String())
	}

	var errResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["code"] != "CURRENCY_MISMATCH" {
		t.Errorf("expected code CURRENCY_MISMATCH, got %v", errResp["code"])
	}
}

// 11. Invalid dates are rejected
func TestExpense_InvalidDates_Rejected(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(5)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupExpenseTestRouter(mq, userID)

	invalidDates := []string{"not-a-date", "2026/11/10", "10-11-2026", "2026-13-45T99:99:99Z"}
	for _, dt := range invalidDates {
		t.Run(dt, func(t *testing.T) {
			body := fmt.Sprintf(`{"category": "Food", "amount": 50000, "currency": "INR", "spent_at": %q}`, dt)
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for date %q, got %d: %s", dt, w.Code, w.Body.String())
			}
		})
	}
}

// 12. Pagination validation works
func TestExpense_PaginationValidation(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(6)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	r := setupExpenseTestRouter(mq, userID)

	invalidParams := []string{
		"limit=0",
		"limit=101",
		"limit=-5",
		"limit=abc",
		"offset=-1",
		"offset=xyz",
	}

	for _, p := range invalidParams {
		t.Run(p, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses?"+p, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for %s, got %d", p, w.Code)
			}
		})
	}
}

// 13. Database errors are handled correctly
func TestExpense_DatabaseErrors_Handled(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(7)
	mq.usersByID[userID] = db.User{ID: userID, Currency: "INR"}
	mq.simulateDBErr = true
	r := setupExpenseTestRouter(mq, userID)

	// POST failure
	{
		body := `{"category": "Food", "amount": 50000, "currency": "INR"}`
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500 for POST on DB failure, got %d", w.Code)
		}
	}

	// GET list failure
	{
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500 for GET list on DB failure, got %d", w.Code)
		}
	}

	// GET single failure
	{
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses/1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500 for GET single on DB failure, got %d", w.Code)
		}
	}

	// DELETE failure
	{
		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/expenses/1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500 for DELETE on DB failure, got %d", w.Code)
		}
	}
}

// 14. Monthly aggregation uses the correct start-inclusive, end-exclusive period boundaries
func TestExpense_MonthlyAggregation_PeriodBoundaries(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userID = int64(80)

	novStart := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	novEnd := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	// Expense 1: Exactly at novStart -> MUST BE INCLUDED (start-inclusive)
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "Rent",
		Amount:   1000000, // ₹10,000
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: novStart, Valid: true},
	})

	// Expense 2: Mid-November -> MUST BE INCLUDED
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "Food",
		Amount:   250000, // ₹2,500
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), Valid: true},
	})

	// Expense 3: Exactly 1 millisecond before novEnd (2026-11-30 23:59:59.999) -> MUST BE INCLUDED
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "Shopping",
		Amount:   150000, // ₹1,500
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: novEnd.Add(-time.Millisecond), Valid: true},
	})

	// Expense 4: Exactly at novEnd (2026-12-01 00:00:00) -> MUST BE EXCLUDED (end-exclusive)
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "DecemberBill",
		Amount:   800000,
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: novEnd, Valid: true},
	})

	// Expense 5: Before November (2026-10-31 23:59:59) -> MUST BE EXCLUDED
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userID,
		Category: "OctoberBill",
		Amount:   700000,
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: novStart.Add(-time.Second), Valid: true},
	})

	// Execute aggregation query
	total, err := mq.GetMonthlyExpenseTotalForUser(context.Background(), db.GetMonthlyExpenseTotalForUserParams{
		UserID:    userID,
		Currency:  "INR",
		SpentAt:   pgtype.Timestamptz{Time: novStart, Valid: true},
		SpentAt_2: pgtype.Timestamptz{Time: novEnd, Valid: true},
	})
	if err != nil {
		t.Fatalf("unexpected error on aggregation: %v", err)
	}

	// Expected: 1,000,000 + 250,000 + 150,000 = 1,400,000 paise (₹14,000)
	const expectedTotal = int64(1400000)
	if total != expectedTotal {
		t.Errorf("expected November total %d, got %d", expectedTotal, total)
	}
}

// 15. Monthly aggregation filters by currency, isolates users, and returns zero when empty
func TestExpense_MonthlyAggregation_FiltersAndIsolation(t *testing.T) {
	mq := newMockExpenseQuerier()
	const userA = int64(91)
	const userB = int64(92)

	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	midNov := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)

	// 1. Zero expenses case: User with no records returns 0
	totalEmpty, err := mq.GetMonthlyExpenseTotalForUser(context.Background(), db.GetMonthlyExpenseTotalForUserParams{
		UserID:    userA,
		Currency:  "INR",
		SpentAt:   pgtype.Timestamptz{Time: start, Valid: true},
		SpentAt_2: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalEmpty != 0 {
		t.Errorf("expected 0 for user with no expenses, got %d", totalEmpty)
	}

	// 2. User A has an INR expense and a USD expense
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userA,
		Category: "Travel",
		Amount:   400000,
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: midNov, Valid: true},
	})
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userA,
		Category: "Software",
		Amount:   5000, // $50 in cents
		Currency: "USD",
		SpentAt:  pgtype.Timestamptz{Time: midNov, Valid: true},
	})

	// User B has an INR expense in the same period
	mq.CreateExpense(context.Background(), db.CreateExpenseParams{
		UserID:   userB,
		Category: "UserB_Item",
		Amount:   900000,
		Currency: "INR",
		SpentAt:  pgtype.Timestamptz{Time: midNov, Valid: true},
	})

	// Query User A in INR: MUST ONLY return User A's INR expense (400,000), ignoring USD and User B
	totalA_INR, err := mq.GetMonthlyExpenseTotalForUser(context.Background(), db.GetMonthlyExpenseTotalForUserParams{
		UserID:    userA,
		Currency:  "INR",
		SpentAt:   pgtype.Timestamptz{Time: start, Valid: true},
		SpentAt_2: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalA_INR != 400000 {
		t.Errorf("expected User A INR total 400000, got %d (currency mixing or user leak detected!)", totalA_INR)
	}

	// Query User A in USD: MUST ONLY return 5,000
	totalA_USD, err := mq.GetMonthlyExpenseTotalForUser(context.Background(), db.GetMonthlyExpenseTotalForUserParams{
		UserID:    userA,
		Currency:  "USD",
		SpentAt:   pgtype.Timestamptz{Time: start, Valid: true},
		SpentAt_2: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalA_USD != 5000 {
		t.Errorf("expected User A USD total 5000, got %d", totalA_USD)
	}

	// Query category breakdown for User A in INR
	catRows, err := mq.GetMonthlyExpensesByCategory(context.Background(), db.GetMonthlyExpensesByCategoryParams{
		UserID:    userA,
		Currency:  "INR",
		SpentAt:   pgtype.Timestamptz{Time: start, Valid: true},
		SpentAt_2: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(catRows) != 1 || catRows[0].Category != "Travel" || catRows[0].TotalAmount != 400000 {
		t.Errorf("category aggregation failed: %+v", catRows)
	}
}
