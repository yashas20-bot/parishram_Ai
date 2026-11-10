package middleware

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"parishram-ai/backend/internal/config"
	db "parishram-ai/backend/internal/db/sqlc"
)

// Context keys for authenticated identity values in Gin context.
const (
	ContextKeyUserID      = "userID"
	ContextKeySupabaseUID = "supabaseUID"
	ContextKeyUserEmail   = "userEmail"
	ContextKeyUser        = "currentUser"
)

// Cache and refresh constants.
const (
	defaultKeyCacheTTL        = 1 * time.Hour
	defaultMinRefreshInterval = 10 * time.Second
)

// Common authentication errors.
var (
	ErrUnauthenticated   = errors.New("unauthenticated request context")
	ErrInvalidUserID     = errors.New("invalid user ID type in context")
	ErrInvalidAlgorithm  = errors.New("disallowed signing algorithm")
	ErrInvalidToken      = errors.New("invalid or malformed token")
	ErrMissingAuthHeader = errors.New("missing or malformed authorization header")
	ErrKeyNotFound       = errors.New("signing key not found in key set")
	ErrUnverifiedEmail   = errors.New("email is not verified")
	ErrAccountCollision  = errors.New("account email collision with unlinked identity")
)

// SupabaseClaims represents the standard and custom claims contained in a Supabase Auth JWT.
type SupabaseClaims struct {
	jwt.RegisteredClaims
	Email         string                 `json:"email"`
	EmailVerified *bool                  `json:"email_verified"`
	Role          string                 `json:"role"`
	UserMetadata  map[string]interface{} `json:"user_metadata"`
	AppMetadata   map[string]interface{} `json:"app_metadata"`
}

// IsEmailVerified checks whether the authoritative top-level email_verified claim is explicitly true.
// User-controlled metadata (user_metadata or app_metadata) is never trusted for email verification.
func (c *SupabaseClaims) IsEmailVerified() bool {
	if c.EmailVerified == nil {
		return false
	}
	return *c.EmailVerified
}

// TokenValidator defines the contract for cryptographically verifying a JWT access token.
type TokenValidator interface {
	ValidateToken(tokenString string) (*SupabaseClaims, error)
	SupportedAlgorithms() []string
	ExpectedIssuer() string
}

// HMACTokenValidator verifies symmetric HS256 tokens using a server-side secret.
type HMACTokenValidator struct {
	secret []byte
	issuer string
}

// NewHMACTokenValidator creates a validator for legacy/configured HS256 secrets.
func NewHMACTokenValidator(secret []byte, issuer string) (*HMACTokenValidator, error) {
	if len(secret) == 0 {
		return nil, errors.New("HMAC secret cannot be empty")
	}
	return &HMACTokenValidator{
		secret: secret,
		issuer: issuer,
	}, nil
}

// SupportedAlgorithms returns the strict allowlist for HMAC tokens.
func (v *HMACTokenValidator) SupportedAlgorithms() []string {
	return []string{jwt.SigningMethodHS256.Alg()}
}

// ExpectedIssuer returns the configured expected token issuer.
func (v *HMACTokenValidator) ExpectedIssuer() string {
	return v.issuer
}

// ValidateToken validates an HS256 token against the configured secret and expected claims.
func (v *HMACTokenValidator) ValidateToken(tokenString string) (*SupabaseClaims, error) {
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(v.SupportedAlgorithms()),
		jwt.WithExpirationRequired(),
		jwt.WithAudience("authenticated"),
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}

	parser := jwt.NewParser(opts...)
	claims := &SupabaseClaims{}

	token, err := parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: expected HMAC, got %v", ErrInvalidAlgorithm, t.Header["alg"])
		}
		return v.secret, nil
	})

	if err != nil || !token.Valid {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	return claims, nil
}

// JWK represents a single JSON Web Key from a JWKS document.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// JWKS represents a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKSTokenValidator verifies asymmetric RS256/ES256 tokens using a remote or static JWKS key set.
type JWKSTokenValidator struct {
	jwksURL            string
	issuer             string
	httpClient         *http.Client
	isProduction       bool
	keyCacheTTL        time.Duration
	minRefreshInterval time.Duration
	mu                 sync.RWMutex
	refreshMu          sync.Mutex
	keys               map[string]crypto.PublicKey
	lastFetch          time.Time
}

// NewJWKSTokenValidator creates a validator for asymmetric keys fetched from a JWKS endpoint.
func NewJWKSTokenValidator(jwksURL string, issuer string, httpClient *http.Client, isProduction bool) (*JWKSTokenValidator, error) {
	if jwksURL == "" {
		return nil, errors.New("JWKS URL cannot be empty")
	}

	parsedURL, err := url.Parse(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("invalid JWKS URL: %w", err)
	}

	if isProduction && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("JWKS URL must use HTTPS in production, got '%s'", parsedURL.Scheme)
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}

	return &JWKSTokenValidator{
		jwksURL:            jwksURL,
		issuer:             issuer,
		httpClient:         httpClient,
		isProduction:       isProduction,
		keyCacheTTL:        defaultKeyCacheTTL,
		minRefreshInterval: defaultMinRefreshInterval,
		keys:               make(map[string]crypto.PublicKey),
	}, nil
}

// NewStaticJWKSTokenValidator creates a JWKS validator pre-seeded with in-memory public keys (for testing).
func NewStaticJWKSTokenValidator(keys map[string]crypto.PublicKey, issuer string) *JWKSTokenValidator {
	return &JWKSTokenValidator{
		issuer:             issuer,
		keyCacheTTL:        defaultKeyCacheTTL,
		minRefreshInterval: defaultMinRefreshInterval,
		keys:               keys,
		lastFetch:          time.Now(),
	}
}

// SetCacheTTL allows tests to configure key cache expiration.
func (v *JWKSTokenValidator) SetCacheTTL(ttl time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keyCacheTTL = ttl
}

// SetLastFetch allows tests to simulate stale/expired key cache.
func (v *JWKSTokenValidator) SetLastFetch(t time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lastFetch = t
}

// SupportedAlgorithms returns the strict allowlist for asymmetric Supabase tokens.
func (v *JWKSTokenValidator) SupportedAlgorithms() []string {
	return []string{jwt.SigningMethodRS256.Alg(), jwt.SigningMethodES256.Alg()}
}

// ExpectedIssuer returns the configured expected token issuer.
func (v *JWKSTokenValidator) ExpectedIssuer() string {
	return v.issuer
}

// FetchKeys retrieves and parses public keys from the JWKS endpoint.
func (v *JWKSTokenValidator) FetchKeys(ctx context.Context) error {
	if v.jwksURL == "" {
		return errors.New("no JWKS URL configured")
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create JWKS request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS endpoint returned non-200 status: %d", resp.StatusCode)
	}

	limitedReader := io.LimitReader(resp.Body, 1024*1024)

	var jwks JWKS
	if err := json.NewDecoder(limitedReader).Decode(&jwks); err != nil {
		return fmt.Errorf("failed to decode JWKS JSON: %w", err)
	}

	parsedKeys := make(map[string]crypto.PublicKey)
	for _, jwk := range jwks.Keys {
		pubKey, err := parseJWK(jwk)
		if err != nil {
			continue
		}
		if jwk.Kid != "" {
			parsedKeys[jwk.Kid] = pubKey
		}
	}

	if len(parsedKeys) == 0 {
		return errors.New("no valid verification keys found in JWKS")
	}

	v.mu.Lock()
	v.keys = parsedKeys
	v.lastFetch = time.Now()
	v.mu.Unlock()

	return nil
}

func parseJWK(jwk JWK) (crypto.PublicKey, error) {
	switch jwk.Kty {
	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil {
			return nil, fmt.Errorf("invalid RSA modulus (n): %w", err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil {
			return nil, fmt.Errorf("invalid RSA exponent (e): %w", err)
		}
		var eInt int
		for _, b := range eBytes {
			eInt = (eInt << 8) | int(b)
		}
		return &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: eInt,
		}, nil

	case "EC":
		xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil {
			return nil, fmt.Errorf("invalid EC X coordinate: %w", err)
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
		if err != nil {
			return nil, fmt.Errorf("invalid EC Y coordinate: %w", err)
		}
		if jwk.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported EC curve: %s (expected P-256)", jwk.Crv)
		}
		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported key type: %s", jwk.Kty)
	}
}

// ValidateToken validates an RS256/ES256 token against the JWKS key set and expected claims.
func (v *JWKSTokenValidator) ValidateToken(tokenString string) (*SupabaseClaims, error) {
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(v.SupportedAlgorithms()),
		jwt.WithExpirationRequired(),
		jwt.WithAudience("authenticated"),
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}

	parser := jwt.NewParser(opts...)
	claims := &SupabaseClaims{}

	token, err := parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("%w: missing 'kid' header in token", ErrInvalidToken)
		}

		// Fast read under read-lock
		v.mu.RLock()
		key, exists := v.keys[kid]
		cacheExpired := v.lastFetch.IsZero() || time.Since(v.lastFetch) > v.keyCacheTTL
		v.mu.RUnlock()

		// If key is present and cache is still fresh, return key immediately
		if exists && !cacheExpired {
			return key, nil
		}

		// If static validator without jwksURL, rely on in-memory keys
		if v.jwksURL == "" {
			if !exists {
				return nil, fmt.Errorf("%w: unknown kid '%s'", ErrKeyNotFound, kid)
			}
			return key, nil
		}

		// Stampede-protected key refresh
		v.refreshMu.Lock()
		defer v.refreshMu.Unlock()

		// Double-check under read-lock after acquiring mutex
		v.mu.RLock()
		key, exists = v.keys[kid]
		cacheExpired = v.lastFetch.IsZero() || time.Since(v.lastFetch) > v.keyCacheTTL
		canFetch := time.Since(v.lastFetch) > v.minRefreshInterval
		v.mu.RUnlock()

		if exists && !cacheExpired {
			return key, nil
		}

		if canFetch {
			if fetchErr := v.FetchKeys(context.Background()); fetchErr != nil {
				// Fail-closed on network errors: do not allow expired or unknown keys
				return nil, fmt.Errorf("%w: JWKS fetch failed: %v", ErrKeyNotFound, fetchErr)
			}
			v.mu.RLock()
			key, exists = v.keys[kid]
			v.mu.RUnlock()
		}

		if !exists {
			return nil, fmt.Errorf("%w: unknown kid '%s'", ErrKeyNotFound, kid)
		}

		return key, nil
	})

	if err != nil || !token.Valid {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	return claims, nil
}

// resolveExpectedIssuer establishes the trusted expected Supabase issuer URL.
// The trusted project root must be configured via SUPABASE_URL to prevent arbitrary JWKS URLs
// from silently establishing a trusted issuer.
func resolveExpectedIssuer(cfg *config.Config) (string, error) {
	if cfg.SupabaseURL != "" {
		parsed, err := url.Parse(cfg.SupabaseURL)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("invalid SUPABASE_URL: %w", err)
		}
		if cfg.Environment == "production" && parsed.Scheme != "https" {
			return "", fmt.Errorf("SUPABASE_URL must use HTTPS in production, got '%s'", parsed.Scheme)
		}
		return strings.TrimRight(cfg.SupabaseURL, "/") + "/auth/v1", nil
	}

	if cfg.Environment == "production" {
		return "", errors.New("SUPABASE_URL is mandatory in production environment to establish the trusted issuer")
	}

	return "", nil
}

// NewTokenValidatorFromConfig constructs an unambiguous TokenValidator based on runtime configuration.
func NewTokenValidatorFromConfig(cfg *config.Config) (TokenValidator, error) {
	isProduction := cfg.Environment == "production"

	// 1. Establish trusted expected issuer from project root
	expectedIssuer, err := resolveExpectedIssuer(cfg)
	if err != nil {
		return nil, err
	}

	if isProduction && expectedIssuer == "" {
		return nil, errors.New("expected JWT issuer is mandatory in production environment (set SUPABASE_URL)")
	}

	// 2. Validate consistency of SUPABASE_JWKS_URL against SUPABASE_URL if both are provided
	if cfg.SupabaseJWKSURL != "" && cfg.SupabaseURL != "" {
		expectedPrefix := strings.TrimRight(cfg.SupabaseURL, "/") + "/auth/v1"
		if !strings.HasPrefix(cfg.SupabaseJWKSURL, expectedPrefix) {
			return nil, fmt.Errorf("inconsistent configuration: SUPABASE_JWKS_URL '%s' does not match project SUPABASE_URL '%s'", cfg.SupabaseJWKSURL, cfg.SupabaseURL)
		}
	}

	hasJWKS := cfg.SupabaseJWKSURL != "" || cfg.SupabaseURL != ""
	hasHMAC := cfg.SupabaseJWTSecret != ""
	mode := strings.ToLower(strings.TrimSpace(cfg.SupabaseAuthMode))

	// Reject ambiguous dual-configuration when no explicit mode is defined
	if hasJWKS && hasHMAC && mode == "" {
		return nil, errors.New("ambiguous authentication configuration: both JWKS endpoint and HMAC secret are configured; please explicitly specify SUPABASE_AUTH_MODE='jwks' or 'hmac'")
	}

	switch mode {
	case "jwks":
		jwksURL := cfg.SupabaseJWKSURL
		if jwksURL == "" && cfg.SupabaseURL != "" {
			jwksURL = strings.TrimRight(cfg.SupabaseURL, "/") + "/auth/v1/.well-known/jwks.json"
		}
		if jwksURL == "" {
			return nil, errors.New("SUPABASE_AUTH_MODE is 'jwks' but no SUPABASE_JWKS_URL or SUPABASE_URL is configured")
		}
		validator, err := NewJWKSTokenValidator(jwksURL, expectedIssuer, nil, isProduction)
		if err != nil {
			return nil, err
		}
		_ = validator.FetchKeys(context.Background())
		return validator, nil

	case "hmac":
		if cfg.SupabaseJWTSecret == "" {
			return nil, errors.New("SUPABASE_AUTH_MODE is 'hmac' but SUPABASE_JWT_SECRET is empty")
		}
		return NewHMACTokenValidator([]byte(cfg.SupabaseJWTSecret), expectedIssuer)

	case "":
		if hasJWKS {
			jwksURL := cfg.SupabaseJWKSURL
			if jwksURL == "" && cfg.SupabaseURL != "" {
				jwksURL = strings.TrimRight(cfg.SupabaseURL, "/") + "/auth/v1/.well-known/jwks.json"
			}
			validator, err := NewJWKSTokenValidator(jwksURL, expectedIssuer, nil, isProduction)
			if err != nil {
				return nil, err
			}
			_ = validator.FetchKeys(context.Background())
			return validator, nil
		}
		if hasHMAC {
			return NewHMACTokenValidator([]byte(cfg.SupabaseJWTSecret), expectedIssuer)
		}
		return nil, errors.New("neither SUPABASE_JWKS_URL nor SUPABASE_JWT_SECRET is configured")

	default:
		return nil, fmt.Errorf("invalid SUPABASE_AUTH_MODE '%s'; must be 'jwks' or 'hmac'", cfg.SupabaseAuthMode)
	}
}

// RequireAuth returns a Gin middleware that authenticates requests using a TokenValidator and safely resolves identity.
func RequireAuth(validator TokenValidator, querier db.Querier) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing authorization header",
			})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "malformed authorization header: expected 'Bearer <token>'",
			})
			return
		}

		tokenString := strings.TrimSpace(parts[1])

		// 1. Cryptographically verify token signature, expiration, audience, and issuer
		claims, err := validator.ValidateToken(tokenString)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid or expired token",
			})
			return
		}

		// 2. Validate subject claim is a valid RFC 4122 UUID
		var supabaseUUID pgtype.UUID
		if err := supabaseUUID.Scan(claims.Subject); err != nil || !supabaseUUID.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid subject identity in token",
			})
			return
		}

		// 3. Resolve internal user by Supabase UUID
		user, err := querier.GetUserBySupabaseUID(c.Request.Context(), supabaseUUID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 4. Safe JIT Provisioning Policy:
				// Must require a verified email claim to prevent identity spoofing
				if claims.Email == "" {
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
						"error": "token missing email claim required for user provisioning",
					})
					return
				}

				// Only the top-level email_verified claim is authoritative.
				// User-controlled metadata (user_metadata, app_metadata) is never trusted.
				if !claims.IsEmailVerified() {
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
						"error": "unverified email cannot be used for user provisioning",
					})
					return
				}

				// Account Takeover Protection:
				// Check whether an account already exists with this email address
				existingUser, emailErr := querier.GetUserByEmail(c.Request.Context(), claims.Email)
				if emailErr == nil {
					// An existing account with this email was found.
					// Never silently link or take over an existing account unless its Supabase UID already matches.
					if existingUser.SupabaseUid.Valid && existingUser.SupabaseUid == supabaseUUID {
						user = existingUser
					} else {
						c.AbortWithStatusJSON(http.StatusConflict, gin.H{
							"error": "account with this email is not linked to this identity",
						})
						return
					}
				} else if errors.Is(emailErr, pgx.ErrNoRows) {
					// No account with this email exists; safe to provision new mapped user
					name := extractNameFromClaims(claims)

					provisioned, provisionErr := querier.UpsertUserWithSupabaseUID(c.Request.Context(), db.UpsertUserWithSupabaseUIDParams{
						SupabaseUid:   supabaseUUID,
						Email:         claims.Email,
						Name:          name,
						SpendingLimit: 0,
						Currency:      "INR",
					})
					if provisionErr != nil {
						c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
							"error": "failed to provision user record",
						})
						return
					}
					user = provisioned
				} else {
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
						"error": "internal database error during identity resolution",
					})
					return
				}
			} else {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": "internal database error",
				})
				return
			}
		}

		// 5. Inject authenticated identity into Gin context
		c.Set(ContextKeyUserID, user.ID)
		c.Set(ContextKeySupabaseUID, claims.Subject)
		c.Set(ContextKeyUserEmail, user.Email)
		c.Set(ContextKeyUser, user)

		c.Next()
	}
}

// extractNameFromClaims determines a friendly display name from Supabase user metadata.
func extractNameFromClaims(claims *SupabaseClaims) string {
	if claims.UserMetadata != nil {
		if name, ok := claims.UserMetadata["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
		if fullName, ok := claims.UserMetadata["full_name"].(string); ok && strings.TrimSpace(fullName) != "" {
			return strings.TrimSpace(fullName)
		}
	}
	if atIdx := strings.Index(claims.Email, "@"); atIdx > 0 {
		return claims.Email[:atIdx]
	}
	return "User"
}

// GetUserID retrieves the authenticated internal int64 user ID from Gin context.
func GetUserID(c *gin.Context) (int64, error) {
	val, exists := c.Get(ContextKeyUserID)
	if !exists {
		return 0, ErrUnauthenticated
	}
	id, ok := val.(int64)
	if !ok {
		return 0, ErrInvalidUserID
	}
	return id, nil
}

// GetSupabaseUID retrieves the authenticated Supabase UUID string from Gin context.
func GetSupabaseUID(c *gin.Context) (string, error) {
	val, exists := c.Get(ContextKeySupabaseUID)
	if !exists {
		return "", ErrUnauthenticated
	}
	uid, ok := val.(string)
	if !ok {
		return "", ErrUnauthenticated
	}
	return uid, nil
}

// GetUserEmail retrieves the authenticated user's email from Gin context.
func GetUserEmail(c *gin.Context) (string, error) {
	val, exists := c.Get(ContextKeyUserEmail)
	if !exists {
		return "", ErrUnauthenticated
	}
	email, ok := val.(string)
	if !ok {
		return "", ErrUnauthenticated
	}
	return email, nil
}

// GetUser retrieves the full db.User record from Gin context.
func GetUser(c *gin.Context) (db.User, error) {
	val, exists := c.Get(ContextKeyUser)
	if !exists {
		return db.User{}, ErrUnauthenticated
	}
	user, ok := val.(db.User)
	if !ok {
		return db.User{}, ErrUnauthenticated
	}
	return user, nil
}
