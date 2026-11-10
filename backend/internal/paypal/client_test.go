package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFormatMinorToDecimal(t *testing.T) {
	testCases := []struct {
		amount   int64
		currency string
		expected string
		wantErr  bool
	}{
		// Standard 2-decimal currencies
		{200000, "INR", "2000.00", false},
		{5000, "USD", "50.00", false},
		{50, "USD", "0.50", false},
		{5, "USD", "0.05", false},
		{1, "EUR", "0.01", false},
		{999999999, "GBP", "9999999.99", false},

		// 0-decimal currencies
		{1500, "JPY", "1500", false},
		{25000, "KRW", "25000", false},
		{500000, "VND", "500000", false},

		// 3-decimal currencies
		{1500, "BHD", "1.500", false},
		{2750, "KWD", "2.750", false},
		{5, "OMR", "0.005", false},

		// Invalid values
		{0, "INR", "", true},
		{-100, "USD", "", true},
		{100, "US", "", true},
		{100, "USDD", "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.currency, func(t *testing.T) {
			res, err := FormatMinorToDecimal(tc.amount, tc.currency)
			if (err != nil) != tc.wantErr {
				t.Fatalf("FormatMinorToDecimal(%d, %s) error = %v, wantErr %v", tc.amount, tc.currency, err, tc.wantErr)
			}
			if !tc.wantErr && res != tc.expected {
				t.Errorf("FormatMinorToDecimal(%d, %s) = %s, expected %s", tc.amount, tc.currency, res, tc.expected)
			}
		})
	}
}

func TestPayPalClient_GetAccessToken_SuccessAndCaching(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			callCount++
			username, password, ok := r.BasicAuth()
			if !ok || username != "test_client_id" || password != "test_client_secret" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"scope": "https://uri.paypal.com/services/invoicing",
				"access_token": "TEST_ACCESS_TOKEN_12345",
				"token_type": "Bearer",
				"app_id": "APP-80W284485P519543T",
				"expires_in": 3600,
				"nonce": "nonce-123"
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithHTTP(server.URL, "test_client_id", "test_client_secret", server.Client())
	ctx := context.Background()

	// First fetch: should make an HTTP request
	token1, err := client.GetAccessToken(ctx)
	if err != nil {
		t.Fatalf("expected token1 to succeed, got: %v", err)
	}
	if token1 != "TEST_ACCESS_TOKEN_12345" {
		t.Errorf("expected token 'TEST_ACCESS_TOKEN_12345', got '%s'", token1)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}

	// Second fetch: should use in-memory cache without calling HTTP server
	token2, err := client.GetAccessToken(ctx)
	if err != nil {
		t.Fatalf("expected token2 to succeed, got: %v", err)
	}
	if token2 != "TEST_ACCESS_TOKEN_12345" {
		t.Errorf("expected token 'TEST_ACCESS_TOKEN_12345', got '%s'", token2)
	}
	if callCount != 1 {
		t.Errorf("expected cached token to not invoke server, but got %d calls", callCount)
	}
}

func TestPayPalClient_GetAccessToken_AuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_client","error_description":"Client Authentication failed"}`))
	}))
	defer server.Close()

	client := NewClientWithHTTP(server.URL, "bad_id", "bad_secret", server.Client())
	_, err := client.GetAccessToken(context.Background())
	if err == nil {
		t.Fatalf("expected auth error, got nil")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed, got: %v", err)
	}
}

func TestPayPalClient_CreateOrder_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"VALID_TOKEN","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path == "/v2/checkout/orders" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer VALID_TOKEN" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			reqID := r.Header.Get("PayPal-Request-Id")
			if reqID != "REQ-IDEMP-42" {
				t.Errorf("expected PayPal-Request-Id header 'REQ-IDEMP-42', got '%s'", reqID)
			}

			var req CreateOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{
				"id": "PAYPAL-ORDER-9999",
				"status": "CREATED",
				"intent": "CAPTURE",
				"links": [
					{
						"href": "https://api-m.sandbox.paypal.com/v2/checkout/orders/PAYPAL-ORDER-9999",
						"rel": "self",
						"method": "GET"
					},
					{
						"href": "https://www.sandbox.paypal.com/checkoutnow?token=PAYPAL-ORDER-9999",
						"rel": "approve",
						"method": "GET"
					}
				]
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithHTTP(server.URL, "client", "secret", server.Client())
	orderReq := CreateOrderRequest{
		Intent: "CAPTURE",
		PurchaseUnits: []PurchaseUnitRequest{
			{
				ReferenceID: "42",
				Description: "Headphones",
				CustomID:    "42",
				Amount: Amount{
					CurrencyCode: "INR",
					Value:        "2000.00",
				},
			},
		},
	}

	order, err := client.CreateOrder(context.Background(), orderReq, "REQ-IDEMP-42")
	if err != nil {
		t.Fatalf("expected CreateOrder to succeed, got: %v", err)
	}

	if order.ID != "PAYPAL-ORDER-9999" {
		t.Errorf("expected order ID 'PAYPAL-ORDER-9999', got '%s'", order.ID)
	}
	if order.Status != "CREATED" {
		t.Errorf("expected status 'CREATED', got '%s'", order.Status)
	}
	approvalURL := order.ApprovalURL()
	expectedURL := "https://www.sandbox.paypal.com/checkoutnow?token=PAYPAL-ORDER-9999"
	if approvalURL != expectedURL {
		t.Errorf("expected approval url '%s', got '%s'", expectedURL, approvalURL)
	}
}

func TestPayPalClient_CreateOrder_APIFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Write([]byte(`{"access_token":"VALID_TOKEN","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path == "/v2/checkout/orders" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{
				"name": "UNPROCESSABLE_ENTITY",
				"message": "The requested action could not be performed.",
				"debug_id": "debug-xyz-123"
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithHTTP(server.URL, "client", "secret", server.Client())
	_, err := client.CreateOrder(context.Background(), CreateOrderRequest{Intent: "CAPTURE"}, "idemp-key")
	if err == nil {
		t.Fatalf("expected API error, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected status 422, got %d", apiErr.StatusCode)
	}
	if apiErr.DebugID != "debug-xyz-123" {
		t.Errorf("expected debug_id 'debug-xyz-123', got '%s'", apiErr.DebugID)
	}
}

func TestPayPalClient_CreateOrder_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Write([]byte(`{"access_token":"VALID_TOKEN","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path == "/v2/checkout/orders" {
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			return
		}
	}))
	defer server.Close()

	client := NewClientWithHTTP(server.URL, "client", "secret", server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.CreateOrder(ctx, CreateOrderRequest{Intent: "CAPTURE"}, "idemp-key")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("expected ErrTimeout, got: %v", err)
	}
}
