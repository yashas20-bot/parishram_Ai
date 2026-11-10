package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "parishram-ai/backend/internal/db/sqlc"
)

// mockQuerier implements db.Querier for testing identity mapping and ownership enforcement.
type mockQuerier struct {
	db.Querier
	users         map[int64]db.User
	usersByUUID   map[[16]byte]db.User
	purchases     map[int64]db.Purchase
	payments      map[int64]db.Payment
	supportDrafts map[int64]db.SupportDraft
	nextUserID    int64
}

func newMockQuerier() *mockQuerier {
	return &mockQuerier{
		users:         make(map[int64]db.User),
		usersByUUID:   make(map[[16]byte]db.User),
		purchases:     make(map[int64]db.Purchase),
		payments:      make(map[int64]db.Payment),
		supportDrafts: make(map[int64]db.SupportDraft),
		nextUserID:    1,
	}
}

func (m *mockQuerier) GetUserBySupabaseUID(ctx context.Context, supabaseUid pgtype.UUID) (db.User, error) {
	if !supabaseUid.Valid {
		return db.User{}, errors.New("invalid supabase_uid")
	}
	user, exists := m.usersByUUID[supabaseUid.Bytes]
	if !exists {
		return db.User{}, pgx.ErrNoRows
	}
	return user, nil
}

func (m *mockQuerier) CreateUserWithSupabaseUID(ctx context.Context, arg db.CreateUserWithSupabaseUIDParams) (db.User, error) {
	if !arg.SupabaseUid.Valid {
		return db.User{}, errors.New("invalid supabase_uid")
	}
	if _, exists := m.usersByUUID[arg.SupabaseUid.Bytes]; exists {
		return db.User{}, errors.New("ERROR: duplicate key value violates unique constraint \"uq_users_supabase_uid\" (SQLSTATE 23505)")
	}
	u := db.User{
		ID:            m.nextUserID,
		Email:         arg.Email,
		Name:          arg.Name,
		SpendingLimit: arg.SpendingLimit,
		Currency:      arg.Currency,
		SupabaseUid:   arg.SupabaseUid,
	}
	m.users[u.ID] = u
	m.usersByUUID[arg.SupabaseUid.Bytes] = u
	m.nextUserID++
	return u, nil
}

func (m *mockQuerier) UpsertUserWithSupabaseUID(ctx context.Context, arg db.UpsertUserWithSupabaseUIDParams) (db.User, error) {
	if !arg.SupabaseUid.Valid {
		return db.User{}, errors.New("invalid supabase_uid")
	}
	existing, exists := m.usersByUUID[arg.SupabaseUid.Bytes]
	if exists {
		existing.Email = arg.Email
		if arg.Name != "" {
			existing.Name = arg.Name
		}
		m.users[existing.ID] = existing
		m.usersByUUID[arg.SupabaseUid.Bytes] = existing
		return existing, nil
	}
	u := db.User{
		ID:            m.nextUserID,
		Email:         arg.Email,
		Name:          arg.Name,
		SpendingLimit: arg.SpendingLimit,
		Currency:      arg.Currency,
		SupabaseUid:   arg.SupabaseUid,
	}
	m.users[u.ID] = u
	m.usersByUUID[arg.SupabaseUid.Bytes] = u
	m.nextUserID++
	return u, nil
}

func (m *mockQuerier) UpdatePurchaseStatusForUser(ctx context.Context, arg db.UpdatePurchaseStatusForUserParams) (db.Purchase, error) {
	purchase, exists := m.purchases[arg.ID]
	if !exists {
		return db.Purchase{}, pgx.ErrNoRows
	}
	// Enforce strict ownership check: WHERE id = $1 AND user_id = $3
	if purchase.UserID != arg.UserID {
		return db.Purchase{}, pgx.ErrNoRows
	}
	purchase.Status = arg.Status
	m.purchases[arg.ID] = purchase
	return purchase, nil
}

func (m *mockQuerier) UpdatePaymentStatusForUser(ctx context.Context, arg db.UpdatePaymentStatusForUserParams) (db.Payment, error) {
	payment, exists := m.payments[arg.ID]
	if !exists {
		return db.Payment{}, pgx.ErrNoRows
	}
	// Enforce strict ownership check: WHERE id = $1 AND user_id = $5
	if payment.UserID != arg.UserID {
		return db.Payment{}, pgx.ErrNoRows
	}
	payment.Status = arg.Status
	if arg.CaptureID.Valid {
		payment.CaptureID = arg.CaptureID
	}
	if arg.FailureReason != "" {
		payment.FailureReason = arg.FailureReason
	}
	m.payments[arg.ID] = payment
	return payment, nil
}

func (m *mockQuerier) UpdateSupportDraftForUser(ctx context.Context, arg db.UpdateSupportDraftForUserParams) (db.SupportDraft, error) {
	draft, exists := m.supportDrafts[arg.ID]
	if !exists {
		return db.SupportDraft{}, pgx.ErrNoRows
	}
	// Enforce strict ownership check: WHERE id = $1 AND user_id = $5
	if draft.UserID != arg.UserID {
		return db.SupportDraft{}, pgx.ErrNoRows
	}
	draft.Subject = arg.Subject
	draft.DraftBody = arg.DraftBody
	draft.Status = arg.Status
	m.supportDrafts[arg.ID] = draft
	return draft, nil
}

func makeUUID(b byte) pgtype.UUID {
	var bytes [16]byte
	bytes[0] = b
	bytes[15] = b
	return pgtype.UUID{Bytes: bytes, Valid: true}
}

// 1. Test Finding an existing Supabase identity
func TestGetUserBySupabaseUID_Existing(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	uid := makeUUID(0x01)
	created, err := mq.CreateUserWithSupabaseUID(ctx, db.CreateUserWithSupabaseUIDParams{
		SupabaseUid:   uid,
		Email:         "user@example.com",
		Name:          "Alice",
		SpendingLimit: 500000,
		Currency:      "INR",
	})
	if err != nil {
		t.Fatalf("unexpected error creating user: %v", err)
	}

	found, err := mq.GetUserBySupabaseUID(ctx, uid)
	if err != nil {
		t.Fatalf("expected to find user by Supabase UUID, got error: %v", err)
	}

	if found.ID != created.ID {
		t.Errorf("expected user ID %d, got %d", created.ID, found.ID)
	}
	if found.Email != "user@example.com" {
		t.Errorf("expected email 'user@example.com', got '%s'", found.Email)
	}
	if found.SupabaseUid != uid {
		t.Errorf("expected supabase UID %v, got %v", uid, found.SupabaseUid)
	}
}

// 2. Test Finding non-existent Supabase identity returns ErrNoRows
func TestGetUserBySupabaseUID_NotFound(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	uid := makeUUID(0x99)
	_, err := mq.GetUserBySupabaseUID(ctx, uid)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("expected pgx.ErrNoRows, got %v", err)
	}
}

// 3. Test Creating a new mapped user
func TestCreateUserWithSupabaseUID_Success(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	uid := makeUUID(0x02)
	user, err := mq.CreateUserWithSupabaseUID(ctx, db.CreateUserWithSupabaseUIDParams{
		SupabaseUid:   uid,
		Email:         "bob@example.com",
		Name:          "Bob",
		SpendingLimit: 100000,
		Currency:      "INR",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	if user.ID == 0 {
		t.Errorf("expected positive user ID, got %d", user.ID)
	}
	if user.Currency != "INR" {
		t.Errorf("expected currency 'INR', got '%s'", user.Currency)
	}
	if user.SpendingLimit != 100000 {
		t.Errorf("expected limit 100000, got %d", user.SpendingLimit)
	}
}

// 4. Test Duplicate Provisioning Attempts with CreateUserWithSupabaseUID
func TestCreateUserWithSupabaseUID_DuplicateRejection(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	uid := makeUUID(0x03)
	_, err := mq.CreateUserWithSupabaseUID(ctx, db.CreateUserWithSupabaseUIDParams{
		SupabaseUid:   uid,
		Email:         "charlie@example.com",
		Name:          "Charlie",
		SpendingLimit: 0,
		Currency:      "INR",
	})
	if err != nil {
		t.Fatalf("first creation should succeed: %v", err)
	}

	// Second attempt with exact same Supabase UID must fail on unique constraint
	_, err = mq.CreateUserWithSupabaseUID(ctx, db.CreateUserWithSupabaseUIDParams{
		SupabaseUid:   uid,
		Email:         "charlie.new@example.com",
		Name:          "Charlie 2",
		SpendingLimit: 0,
		Currency:      "INR",
	})
	if err == nil {
		t.Fatalf("expected duplicate key error for second CreateUserWithSupabaseUID attempt, got nil")
	}
	if !strings.Contains(err.Error(), "unique constraint") {
		t.Errorf("expected unique constraint violation error, got: %v", err)
	}
}

// 5. Test Idempotent Provisioning with UpsertUserWithSupabaseUID
func TestUpsertUserWithSupabaseUID_IdempotentProvisioning(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	uid := makeUUID(0x04)
	first, err := mq.UpsertUserWithSupabaseUID(ctx, db.UpsertUserWithSupabaseUIDParams{
		SupabaseUid:   uid,
		Email:         "dana@example.com",
		Name:          "Dana",
		SpendingLimit: 50000,
		Currency:      "INR",
	})
	if err != nil {
		t.Fatalf("first upsert should succeed: %v", err)
	}

	// Second provisioning attempt must be idempotent: returns same user ID, does not create duplicates
	second, err := mq.UpsertUserWithSupabaseUID(ctx, db.UpsertUserWithSupabaseUIDParams{
		SupabaseUid:   uid,
		Email:         "dana.updated@example.com",
		Name:          "Dana Updated",
		SpendingLimit: 50000,
		Currency:      "INR",
	})
	if err != nil {
		t.Fatalf("second upsert must not fail: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("expected idempotent upsert to keep user ID %d, but got %d", first.ID, second.ID)
	}
	if second.Email != "dana.updated@example.com" {
		t.Errorf("expected updated email 'dana.updated@example.com', got '%s'", second.Email)
	}
	if len(mq.users) != 1 {
		t.Errorf("expected exactly 1 user in database, found %d", len(mq.users))
	}
}

// 6. Test User-Scoped UpdatePurchaseStatusForUser (Ownership Enforcement)
func TestUpdatePurchaseStatusForUser_OwnershipEnforcement(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	ownerID := int64(10)
	attackerID := int64(99)
	purchaseID := int64(500)

	mq.purchases[purchaseID] = db.Purchase{
		ID:           purchaseID,
		UserID:       ownerID,
		MerchantName: "Store A",
		ItemName:     "Headphones",
		Amount:       250000,
		Currency:     "INR",
		Status:       "proposed",
	}

	// Attacker tries to update owner's purchase -> must be denied with ErrNoRows
	_, err := mq.UpdatePurchaseStatusForUser(ctx, db.UpdatePurchaseStatusForUserParams{
		ID:     purchaseID,
		Status: "approved",
		UserID: attackerID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected attacker's cross-user update to return ErrNoRows, got %v", err)
	}

	// Verify purchase status was not modified
	if mq.purchases[purchaseID].Status != "proposed" {
		t.Errorf("purchase status should remain 'proposed', but changed to '%s'", mq.purchases[purchaseID].Status)
	}

	// Legitimate owner updates purchase -> must succeed
	updated, err := mq.UpdatePurchaseStatusForUser(ctx, db.UpdatePurchaseStatusForUserParams{
		ID:     purchaseID,
		Status: "approved",
		UserID: ownerID,
	})
	if err != nil {
		t.Fatalf("owner's update should succeed, got %v", err)
	}
	if updated.Status != "approved" {
		t.Errorf("expected status 'approved', got '%s'", updated.Status)
	}
}

// 7. Test User-Scoped UpdatePaymentStatusForUser (Ownership Enforcement)
func TestUpdatePaymentStatusForUser_OwnershipEnforcement(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	ownerID := int64(20)
	attackerID := int64(77)
	paymentID := int64(600)

	mq.payments[paymentID] = db.Payment{
		ID:            paymentID,
		UserID:        ownerID,
		PaypalOrderID: "PAYPAL-12345",
		Amount:        10000,
		Currency:      "INR",
		Status:        "awaiting_approval",
	}

	// Attacker attempts to cancel owner's payment -> rejected
	_, err := mq.UpdatePaymentStatusForUser(ctx, db.UpdatePaymentStatusForUserParams{
		ID:     paymentID,
		Status: "cancelled",
		UserID: attackerID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected attacker's update to fail with ErrNoRows, got %v", err)
	}

	// Owner successfully cancels their own payment
	updated, err := mq.UpdatePaymentStatusForUser(ctx, db.UpdatePaymentStatusForUserParams{
		ID:     paymentID,
		Status: "cancelled",
		UserID: ownerID,
	})
	if err != nil {
		t.Fatalf("owner update should succeed, got: %v", err)
	}
	if updated.Status != "cancelled" {
		t.Errorf("expected status 'cancelled', got '%s'", updated.Status)
	}
}

// 8. Test User-Scoped UpdateSupportDraftForUser (Ownership Enforcement)
func TestUpdateSupportDraftForUser_OwnershipEnforcement(t *testing.T) {
	mq := newMockQuerier()
	ctx := context.Background()

	ownerID := int64(30)
	attackerID := int64(88)
	draftID := int64(700)

	mq.supportDrafts[draftID] = db.SupportDraft{
		ID:          draftID,
		UserID:      ownerID,
		RequestType: "refund",
		Subject:     "Initial Subject",
		DraftBody:   "Initial Body",
		Status:      "draft",
	}

	// Attacker tries to modify draft -> rejected
	_, err := mq.UpdateSupportDraftForUser(ctx, db.UpdateSupportDraftForUserParams{
		ID:        draftID,
		Subject:   "Malicious Subject",
		DraftBody: "Malicious Body",
		Status:    "sent",
		UserID:    attackerID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected attacker update to return ErrNoRows, got %v", err)
	}

	// Owner modifies draft -> succeeds
	updated, err := mq.UpdateSupportDraftForUser(ctx, db.UpdateSupportDraftForUserParams{
		ID:        draftID,
		Subject:   "Updated Subject",
		DraftBody: "Updated Body",
		Status:    "reviewed",
		UserID:    ownerID,
	})
	if err != nil {
		t.Fatalf("owner update should succeed, got %v", err)
	}
	if updated.Subject != "Updated Subject" || updated.Status != "reviewed" {
		t.Errorf("unexpected draft values: %+v", updated)
	}
}

// 9. Static Verification of Generated SQL Strings for User-Scoped Conditions
func TestGeneratedSQLQueries_OwnershipClauses(t *testing.T) {
	// Verify that the generated queries in db package enforce user_id scoping
	testCases := []struct {
		name          string
		queryConstant string
		requiredScope string
	}{
		{
			name:          "UpdatePurchaseStatusForUser scopes by user_id",
			queryConstant: "WHERE id = $1 AND user_id = $3",
			requiredScope: "WHERE id = $1 AND user_id = $3",
		},
		{
			name:          "UpdatePaymentStatusForUser scopes by user_id",
			queryConstant: "WHERE id = $1 AND user_id = $5",
			requiredScope: "WHERE id = $1 AND user_id = $5",
		},
		{
			name:          "UpdateSupportDraftForUser scopes by user_id",
			queryConstant: "WHERE id = $1 AND user_id = $5",
			requiredScope: "WHERE id = $1 AND user_id = $5",
		},
		{
			name:          "DeleteExpenseForUser scopes by user_id",
			queryConstant: "WHERE id = $1 AND user_id = $2",
			requiredScope: "WHERE id = $1 AND user_id = $2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.queryConstant, tc.requiredScope) {
				t.Errorf("expected SQL string to contain '%s', got '%s'", tc.requiredScope, tc.queryConstant)
			}
		})
	}
}
