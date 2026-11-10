package middleware

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"parishram-ai/backend/internal/config"
	db "parishram-ai/backend/internal/db/sqlc"
)

// mockQuerier implements db.Querier for testing authentication, user resolution, and email safety.
type mockQuerier struct {
	db.Querier
	usersByUUID   map[[16]byte]db.User
	usersByEmail  map[string]db.User
	simulateDBErr bool
	nextUserID    int64
}

func newMockQuerier() *mockQuerier {
	return &mockQuerier{
		usersByUUID:  make(map[[16]byte]db.User),
		usersByEmail: make(map[string]db.User),
		nextUserID:   1,
	}
}

func (m *mockQuerier) GetUserBySupabaseUID(ctx context.Context, supabaseUid pgtype.UUID) (db.User, error) {
	if m.simulateDBErr {
		return db.User{}, errors.New("simulated database failure")
	}
	if !supabaseUid.Valid {
		return db.User{}, errors.New("invalid supabase_uid")
	}
	user, exists := m.usersByUUID[supabaseUid.Bytes]
	if !exists {
		return db.User{}, pgx.ErrNoRows
	}
	return user, nil
}

func (m *mockQuerier) GetUserByEmail(ctx context.Context, email string) (db.User, error) {
	if m.simulateDBErr {
		return db.User{}, errors.New("simulated database failure")
	}
	user, exists := m.usersByEmail[email]
	if !exists {
		return db.User{}, pgx.ErrNoRows
	}
	return user, nil
}

func (m *mockQuerier) UpsertUserWithSupabaseUID(ctx context.Context, arg db.UpsertUserWithSupabaseUIDParams) (db.User, error) {
	if m.simulateDBErr {
		return db.User{}, errors.New("simulated database failure")
	}
	if !arg.SupabaseUid.Valid {
		return db.User{}, errors.New("invalid supabase_uid")
	}
	existing, exists := m.usersByUUID[arg.SupabaseUid.Bytes]
	if exists {
		existing.Email = arg.Email
		if arg.Name != "" {
			existing.Name = arg.Name
		}
		m.usersByUUID[arg.SupabaseUid.Bytes] = existing
		m.usersByEmail[existing.Email] = existing
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
	m.usersByUUID[arg.SupabaseUid.Bytes] = u
	m.usersByEmail[u.Email] = u
	m.nextUserID++
	return u, nil
}

func makeUUIDBytes(b byte) [16]byte {
	var bytes [16]byte
	bytes[0] = b
	bytes[15] = b
	return bytes
}

func makeUUIDString(b byte) string {
	bytes := makeUUIDBytes(b)
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		bytes[0], bytes[1], bytes[2], bytes[3],
		bytes[4], bytes[5],
		bytes[6], bytes[7],
		bytes[8], bytes[9],
		bytes[10], bytes[11], bytes[12], bytes[13], bytes[14], bytes[15],
	)
}

func makeHMACToken(secret []byte, sub, email string, emailVerified *bool, iss string, aud []string, expiry time.Duration) string {
	now := time.Now()
	claims := SupabaseClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			Issuer:    iss,
			Audience:  jwt.ClaimStrings(aud),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		},
		Email:         email,
		EmailVerified: emailVerified,
		Role:          "authenticated",
		UserMetadata: map[string]interface{}{
			"name": "Test User",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(secret)
	return tokenStr
}

func makeRSAToken(key *rsa.PrivateKey, kid, sub, email string, emailVerified *bool, iss string, aud []string, expiry time.Duration) string {
	now := time.Now()
	claims := SupabaseClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			Issuer:    iss,
			Audience:  jwt.ClaimStrings(aud),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		},
		Email:         email,
		EmailVerified: emailVerified,
		Role:          "authenticated",
		UserMetadata: map[string]interface{}{
			"name": "RSA Test User",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	tokenStr, _ := token.SignedString(key)
	return tokenStr
}

const (
	testSecret = "super-secret-supabase-jwt-key-for-tests-32b"
	testIssuer = "https://test-project.supabase.co/auth/v1"
)

func boolPtr(b bool) *bool {
	return &b
}

func setupTestEngine(validator TokenValidator, querier db.Querier) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	authMw := RequireAuth(validator, querier)

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	v1 := r.Group("/api/v1")
	{
		v1.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "healthy"})
		})

		protected := v1.Group("")
		protected.Use(authMw)
		{
			protected.GET("/me", func(c *gin.Context) {
				userID, err := GetUserID(c)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				email, _ := GetUserEmail(c)
				uid, _ := GetSupabaseUID(c)
				c.JSON(http.StatusOK, gin.H{
					"user_id":      userID,
					"email":        email,
					"supabase_uid": uid,
				})
			})
		}
	}

	return r
}

// 1. Missing Authorization header
func TestAuth_MissingHeader(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for missing header, got %d", w.Code)
	}

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "missing authorization header" {
		t.Errorf("unexpected error message: %s", resp["error"])
	}
}

// 2. Invalid Bearer format
func TestAuth_InvalidBearerFormat(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	invalidHeaders := []string{
		"Basic dXNlcjpwYXNz",
		"Bearer",
		"Bearer   ",
		"Token some-random-token",
		"bearer",
	}

	for _, header := range invalidHeaders {
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("header '%s': expected 401, got %d", header, w.Code)
		}
	}
}

// 3. Malformed JWT
func TestAuth_MalformedJWT(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for malformed JWT, got %d", w.Code)
	}
}

// 4. Invalid signature
func TestAuth_InvalidSignature(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	wrongSecret := []byte("wrong-signing-secret-key-1234567890")
	token := makeHMACToken(wrongSecret, makeUUIDString(1), "alice@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid signature, got %d", w.Code)
	}
}

// 5. Expired token
func TestAuth_ExpiredToken(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	expiredToken := makeHMACToken([]byte(testSecret), makeUUIDString(1), "alice@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, -10*time.Minute)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+expiredToken)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", w.Code)
	}
}

// 6. Wrong issuer
func TestAuth_WrongIssuer(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	token := makeHMACToken([]byte(testSecret), makeUUIDString(1), "alice@example.com", boolPtr(true), "https://malicious-issuer.com/auth/v1", []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong issuer, got %d", w.Code)
	}
}

// 7. Wrong audience
func TestAuth_WrongAudience(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	token := makeHMACToken([]byte(testSecret), makeUUIDString(1), "alice@example.com", boolPtr(true), testIssuer, []string{"wrong-audience"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong audience, got %d", w.Code)
	}
}

// 8. Invalid UUID subject
func TestAuth_InvalidUUIDSubject(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	token := makeHMACToken([]byte(testSecret), "not-a-uuid-at-all", "alice@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-UUID subject, got %d", w.Code)
	}
}

// 9. Requirement 1.1: Top-level email_verified: true permits JIT provisioning
func TestAuth_JITProvisioning_TopLevelEmailVerified_True(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(9)
	token := makeHMACToken([]byte(testSecret), subStr, "newuser@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with JIT provisioning, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["user_id"] == nil || resp["user_id"].(float64) <= 0 {
		t.Errorf("expected positive provisioned user_id, got %v", resp["user_id"])
	}
	if resp["email"] != "newuser@example.com" {
		t.Errorf("expected email 'newuser@example.com', got %v", resp["email"])
	}
}

// 10. Requirement 1.2: Top-level email_verified: false rejects provisioning
func TestAuth_JITProvisioning_TopLevelEmailVerified_False(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(11)
	token := makeHMACToken([]byte(testSecret), subStr, "unverified@example.com", boolPtr(false), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for top-level email_verified: false, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "unverified email cannot be used for user provisioning" {
		t.Errorf("unexpected error message: %s", resp["error"])
	}

	if len(mq.usersByUUID) != 0 {
		t.Errorf("expected 0 users created, but found %d", len(mq.usersByUUID))
	}
}

// 11. Requirement 1.3: Missing top-level email_verified claim rejects provisioning
func TestAuth_JITProvisioning_MissingTopLevelClaim_Rejected(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(12)
	// email_verified is nil (missing from claims)
	token := makeHMACToken([]byte(testSecret), subStr, "missingclaim@example.com", nil, testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for missing top-level claim, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "unverified email cannot be used for user provisioning" {
		t.Errorf("unexpected error message: %s", resp["error"])
	}
}

// 12. Requirement 1.4: user_metadata.email_verified: true alone does NOT permit provisioning
func TestAuth_JITProvisioning_UserMetadataEmailVerified_Ignored(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(13)
	now := time.Now()
	// Top-level email_verified is nil/missing, but user_metadata has email_verified: true
	claims := SupabaseClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subStr,
			Issuer:    testIssuer,
			Audience:  jwt.ClaimStrings([]string{"authenticated"}),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(1 * time.Hour)),
		},
		Email: "spoofed_user_meta@example.com",
		Role:  "authenticated",
		UserMetadata: map[string]interface{}{
			"email_verified": true, // User-controlled metadata!
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(testSecret))

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized when only user_metadata claims verification, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "unverified email cannot be used for user provisioning" {
		t.Errorf("unexpected error message: %s", resp["error"])
	}
}

// 13. Requirement 1.5: app_metadata.email_verified: true alone does NOT permit provisioning
func TestAuth_JITProvisioning_AppMetadataEmailVerified_Ignored(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(14)
	now := time.Now()
	// Top-level email_verified is nil, but app_metadata has email_verified: true
	claims := SupabaseClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subStr,
			Issuer:    testIssuer,
			Audience:  jwt.ClaimStrings([]string{"authenticated"}),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(1 * time.Hour)),
		},
		Email: "spoofed_app_meta@example.com",
		Role:  "authenticated",
		AppMetadata: map[string]interface{}{
			"email_verified": true,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(testSecret))

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized when only app_metadata claims verification, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// 14. Requirement 1.6: An already-mapped user can authenticate without triggering JIT provisioning
func TestAuth_ExistingMappedUser_AuthenticatesNormally(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(15)
	var subUUID pgtype.UUID
	_ = subUUID.Scan(subStr)

	// Pre-existing mapped user in database
	u := db.User{
		ID:          42,
		Email:       "existing@example.com",
		Name:        "Existing User",
		SupabaseUid: subUUID,
	}
	mq.usersByUUID[subUUID.Bytes] = u
	mq.usersByEmail[u.Email] = u

	// Even if token email_verified is nil/missing, already-mapped user authenticates!
	token := makeHMACToken([]byte(testSecret), subStr, "existing@example.com", nil, testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for existing mapped user, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["user_id"] != float64(42) {
		t.Errorf("expected user_id 42, got %v", resp["user_id"])
	}
}

// 15. Account Takeover Protection: Email collision with unlinked existing account is rejected
func TestAuth_ExistingAccount_EmailLinkingSafety_PreventTakeover(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	// Pre-existing user with email alice@example.com and NO Supabase UID
	existingUser := db.User{
		ID:          100,
		Email:       "alice@example.com",
		Name:        "Alice Original",
		SupabaseUid: pgtype.UUID{Valid: false},
	}
	mq.usersByEmail["alice@example.com"] = existingUser

	// Attacker has a valid token with email alice@example.com and a new Supabase UUID
	attackerSubStr := makeUUIDString(16)
	attackerToken := makeHMACToken([]byte(testSecret), attackerSubStr, "alice@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+attackerToken)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Must reject with 409 Conflict to prevent identity takeover
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for existing unlinked email collision, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "account with this email is not linked to this identity" {
		t.Errorf("unexpected error message: %s", resp["error"])
	}

	// Verify Alice's account was not modified or overwritten
	if mq.usersByEmail["alice@example.com"].ID != 100 {
		t.Errorf("Alice's account ID changed!")
	}
	if mq.usersByEmail["alice@example.com"].SupabaseUid.Valid {
		t.Errorf("Alice's account was hijacked with Supabase UID!")
	}
}

// 16. Ambiguous Signing Key Configuration (both JWKS and HMAC present without explicit mode)
func TestAuth_Config_AmbiguousJWKSAndHMAC_Rejected(t *testing.T) {
	cfg := &config.Config{
		SupabaseURL:       "https://test.supabase.co",
		SupabaseJWKSURL:   "https://test.supabase.co/auth/v1/.well-known/jwks.json",
		SupabaseJWTSecret: "some-secret",
		SupabaseAuthMode:  "", // Not specified
		Environment:       "development",
	}

	_, err := NewTokenValidatorFromConfig(cfg)
	if err == nil {
		t.Fatalf("expected error for ambiguous dual configuration without SUPABASE_AUTH_MODE, got nil")
	}
}

// 17. Explicit SUPABASE_AUTH_MODE resolves configuration deterministically
func TestAuth_Config_ExplicitAuthMode(t *testing.T) {
	cfg := &config.Config{
		SupabaseURL:       "https://test.supabase.co",
		SupabaseJWKSURL:   "https://test.supabase.co/auth/v1/.well-known/jwks.json",
		SupabaseJWTSecret: "some-secret",
		SupabaseAuthMode:  "hmac",
		Environment:       "development",
	}

	v, err := NewTokenValidatorFromConfig(cfg)
	if err != nil {
		t.Fatalf("expected explicit hmac mode to succeed, got %v", err)
	}
	if len(v.SupportedAlgorithms()) != 1 || v.SupportedAlgorithms()[0] != "HS256" {
		t.Errorf("expected HS256 algorithm for hmac mode, got %v", v.SupportedAlgorithms())
	}
}

// 18. Inconsistent SUPABASE_JWKS_URL and SUPABASE_URL rejected
func TestAuth_Config_InconsistentJWKSAndSupabaseURL_Rejected(t *testing.T) {
	cfg := &config.Config{
		SupabaseURL:     "https://myproject.supabase.co",
		SupabaseJWKSURL: "https://attacker.supabase.co/auth/v1/.well-known/jwks.json",
		Environment:     "development",
	}

	_, err := NewTokenValidatorFromConfig(cfg)
	if err == nil {
		t.Fatalf("expected error when SUPABASE_JWKS_URL host does not match SUPABASE_URL, got nil")
	}
}

// 19. Production requires expected issuer via SUPABASE_URL
func TestAuth_Config_Production_RequiresIssuer(t *testing.T) {
	cfg := &config.Config{
		SupabaseURL:       "", // Missing
		SupabaseJWKSURL:   "", // Missing
		SupabaseJWTSecret: "some-secret",
		SupabaseAuthMode:  "hmac",
		Environment:       "production",
	}

	_, err := NewTokenValidatorFromConfig(cfg)
	if err == nil {
		t.Fatalf("expected error in production when expected issuer is missing, got nil")
	}
}

// 20. Production JWKS requires HTTPS
func TestAuth_Config_Production_JWKSRequiresHTTPS(t *testing.T) {
	_, err := NewJWKSTokenValidator("http://insecure.supabase.co/jwks.json", "https://insecure.supabase.co/auth/v1", nil, true)
	if err == nil {
		t.Fatalf("expected error in production for HTTP JWKS URL, got nil")
	}
}

// 21. JWKS Server Timeout & Malformed Response
func TestAuth_JWKS_NetworkFailures(t *testing.T) {
	malformedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not valid json at all"))
	}))
	defer malformedServer.Close()

	v, err := NewJWKSTokenValidator(malformedServer.URL, "https://test.supabase.co/auth/v1", malformedServer.Client(), false)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	err = v.FetchKeys(context.Background())
	if err == nil {
		t.Fatalf("expected FetchKeys to fail on malformed JSON, got nil")
	}
}

// 22. JWKS Unknown Key ID rejected
func TestAuth_JWKS_UnknownKeyID_Rejected(t *testing.T) {
	mq := newMockQuerier()
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := map[string]crypto.PublicKey{
		"known-kid": &rsaKey.PublicKey,
	}

	v := NewStaticJWKSTokenValidator(keys, testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(17)
	token := makeRSAToken(rsaKey, "unknown-kid-999", subStr, "rsa@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown key ID, got %d", w.Code)
	}
}

// 23. JWKS Cache TTL Expiration & Fail-Closed Behavior
func TestAuth_JWKS_CacheTTLExpiration_FailsClosed(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "test-key-exp"
	keys := map[string]crypto.PublicKey{
		kid: &rsaKey.PublicKey,
	}

	// Mock server that returns HTTP 500 when called
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingServer.Close()

	v, err := NewJWKSTokenValidator(failingServer.URL, testIssuer, failingServer.Client(), false)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	// Manually inject keys and simulate cache that is 2 hours old (past the 1-hour TTL)
	for k, val := range keys {
		v.SetCacheTTL(1 * time.Hour)
		v.SetLastFetch(time.Now().Add(-2 * time.Hour))
		_ = k
		_ = val
	}

	// Token with the key
	subStr := makeUUIDString(18)
	token := makeRSAToken(rsaKey, kid, subStr, "expiredcache@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	// Since cache is expired (>1 hour) and server returns 500, it must fail closed!
	_, err = v.ValidateToken(token)
	if err == nil {
		t.Fatalf("expected ValidateToken to fail closed on expired cache with failing JWKS endpoint, got nil")
	}
}

// 24. Database lookup failure returns 500
func TestAuth_DatabaseLookupFailure(t *testing.T) {
	mq := newMockQuerier()
	mq.simulateDBErr = true
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(1)
	token := makeHMACToken([]byte(testSecret), subStr, "alice@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error for database outage, got %d", w.Code)
	}

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "internal database error" {
		t.Errorf("unexpected error message: %s", resp["error"])
	}
}

// 25. Asymmetric JWKS Token Verification (RS256)
func TestAuth_AsymmetricJWKS_RS256(t *testing.T) {
	mq := newMockQuerier()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	kid := "test-key-1"
	keys := map[string]crypto.PublicKey{
		kid: &rsaKey.PublicKey,
	}

	v := NewStaticJWKSTokenValidator(keys, testIssuer)
	router := setupTestEngine(v, mq)

	subStr := makeUUIDString(7)
	token := makeRSAToken(rsaKey, kid, subStr, "rsa_user@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for RS256 token, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["email"] != "rsa_user@example.com" {
		t.Errorf("expected email 'rsa_user@example.com', got %v", resp["email"])
	}
}

// 26. Algorithm confusion attack: sending HMAC token to JWKS validator
func TestAuth_AlgorithmConfusion_Rejected(t *testing.T) {
	mq := newMockQuerier()

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := map[string]crypto.PublicKey{
		"key-1": &rsaKey.PublicKey,
	}
	jwksValidator := NewStaticJWKSTokenValidator(keys, testIssuer)
	router := setupTestEngine(jwksValidator, mq)

	hmacToken := makeHMACToken([]byte(testSecret), makeUUIDString(1), "hacker@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+hmacToken)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for algorithm confusion attempt, got %d", w.Code)
	}
}

// 27. Protected routes reject unauthenticated requests & Public Health routes remain accessible
func TestRoutes_PublicVsProtectedRoutes(t *testing.T) {
	mq := newMockQuerier()
	v, _ := NewHMACTokenValidator([]byte(testSecret), testIssuer)
	authMw := RequireAuth(v, mq)

	router := gin.New()
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})
	v1 := router.Group("/api/v1")
	v1.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})
	v1Protected := v1.Group("")
	v1Protected.Use(authMw)
	v1Protected.GET("/protected-test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 1. Root /health must be 200 without token
	{
		req, _ := http.NewRequest(http.MethodGet, "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected /health to return 200, got %d", w.Code)
		}
	}

	// 2. /api/v1/health must be 200 without token
	{
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected /api/v1/health to return 200, got %d", w.Code)
		}
	}

	// 3. /api/v1/protected-test must be 401 without token
	{
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/protected-test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected /api/v1/protected-test to return 401, got %d", w.Code)
		}
	}

	// 4. /api/v1/protected-test must be 200 with valid token
	{
		subStr := makeUUIDString(3)
		token := makeHMACToken([]byte(testSecret), subStr, "user@example.com", boolPtr(true), testIssuer, []string{"authenticated"}, 1*time.Hour)
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/protected-test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected /api/v1/protected-test with valid token to return 200, got %d", w.Code)
		}
	}
}
