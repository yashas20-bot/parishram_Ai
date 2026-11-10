package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client defines the interface for interacting with PayPal REST APIs.
type Client interface {
	GetAccessToken(ctx context.Context) (string, error)
	CreateOrder(ctx context.Context, req CreateOrderRequest, idempotencyKey string) (*Order, error)
}

type clientImpl struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client

	tokenMu        sync.RWMutex
	cachedToken    string
	tokenExpiresAt time.Time
}

// NewClient initializes a PayPal client with the specified base URL, credentials, and timeout.
func NewClient(baseURL, clientID, clientSecret string, timeout time.Duration) Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	httpClient := &http.Client{
		Timeout: timeout,
	}
	return NewClientWithHTTP(baseURL, clientID, clientSecret, httpClient)
}

// NewClientWithHTTP initializes a PayPal client with an explicit http.Client (e.g. for mock testing).
func NewClientWithHTTP(baseURL, clientID, clientSecret string, httpClient *http.Client) Client {
	return &clientImpl{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		httpClient:   httpClient,
	}
}

type oauthTokenResponse struct {
	Scope       string `json:"scope"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	AppID       string `json:"app_id"`
	ExpiresIn   int64  `json:"expires_in"`
	Nonce       string `json:"nonce"`
}

// GetAccessToken retrieves an unexpired OAuth 2.0 access token using client credentials.
// It caches valid tokens in memory until near expiration (with a 60-second safety window).
func (c *clientImpl) GetAccessToken(ctx context.Context) (string, error) {
	if c.clientID == "" || c.clientSecret == "" {
		return "", fmt.Errorf("%w: client_id and client_secret are required", ErrInvalidConfig)
	}

	// 1. Check read cache
	c.tokenMu.RLock()
	if c.cachedToken != "" && time.Now().Add(60*time.Second).Before(c.tokenExpiresAt) {
		token := c.cachedToken
		c.tokenMu.RUnlock()
		return token, nil
	}
	c.tokenMu.RUnlock()

	// 2. Acquire write lock and refresh token
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// Double-check after acquiring write lock
	if c.cachedToken != "" && time.Now().Add(60*time.Second).Before(c.tokenExpiresAt) {
		return c.cachedToken, nil
	}

	endpoint := c.baseURL + "/v1/oauth2/token"
	formData := url.Values{}
	formData.Set("grant_type", "client_credentials")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create oauth request: %w", err)
	}

	httpReq.SetBasicAuth(c.clientID, c.clientSecret)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Accept-Language", "en_US")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: oauth request timed out", ErrTimeout)
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return "", fmt.Errorf("%w: oauth request network timeout", ErrTimeout)
		}
		return "", fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read oauth response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(bodyBytes, &apiErr)
		return "", fmt.Errorf("%w: status %d (%s: %s)", ErrAuthFailed, resp.StatusCode, apiErr.Error, apiErr.ErrorDescription)
	}

	var tokenResp oauthTokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse oauth response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("%w: received empty access token", ErrAuthFailed)
	}

	expiresInSec := tokenResp.ExpiresIn
	if expiresInSec <= 0 {
		expiresInSec = 3600 // Fallback to 1 hour
	}

	c.cachedToken = tokenResp.AccessToken
	c.tokenExpiresAt = time.Now().Add(time.Duration(expiresInSec) * time.Second)

	return c.cachedToken, nil
}

// CreateOrder calls POST /v2/checkout/orders to create a payment order on PayPal.
func (c *clientImpl) CreateOrder(ctx context.Context, req CreateOrderRequest, idempotencyKey string) (*Order, error) {
	token, err := c.GetAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode order request: %w", err)
	}

	endpoint := c.baseURL + "/v2/checkout/orders"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create order request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	if strings.TrimSpace(idempotencyKey) != "" {
		httpReq.Header.Set("PayPal-Request-Id", strings.TrimSpace(idempotencyKey))
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: create order request timed out", ErrTimeout)
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, fmt.Errorf("%w: create order request network timeout", ErrTimeout)
		}
		return nil, fmt.Errorf("%w: %v", ErrAPIFailure, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read order response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var paypalErr struct {
			Name    string `json:"name"`
			Message string `json:"message"`
			DebugID string `json:"debug_id"`
		}
		_ = json.Unmarshal(bodyBytes, &paypalErr)
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Name:       paypalErr.Name,
			Message:    paypalErr.Message,
			DebugID:    paypalErr.DebugID,
		}
	}

	var order Order
	if err := json.Unmarshal(bodyBytes, &order); err != nil {
		return nil, fmt.Errorf("failed to decode order response: %w", err)
	}

	return &order, nil
}
