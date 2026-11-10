package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	Port               string
	DatabaseURL        string
	Environment        string
	CORSAllowedOrigins string
	SupabaseURL        string
	SupabaseJWTSecret  string
	SupabaseJWKSURL    string
	SupabaseAuthMode   string // "jwks", "hmac", or "" (auto-detect if unambiguous)
	PayPalClientID     string
	PayPalClientSecret string
	PayPalBaseURL      string
	PayPalMode         string // "sandbox"
	PayPalWebhookID    string
}

// LoadConfig reads configuration from .env and the environment with robust defaults.
func LoadConfig() *Config {
	loadDotEnv(".env")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/parishram_ai?sslmode=disable"
	}

	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "development"
	}

	corsOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if corsOrigins == "" {
		corsOrigins = "*"
	}

	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseJWTSecret := os.Getenv("SUPABASE_JWT_SECRET")
	supabaseJWKSURL := os.Getenv("SUPABASE_JWKS_URL")
	supabaseAuthMode := os.Getenv("SUPABASE_AUTH_MODE")
	if supabaseJWKSURL == "" && supabaseURL != "" {
		supabaseJWKSURL = strings.TrimRight(supabaseURL, "/") + "/auth/v1/.well-known/jwks.json"
	}

	paypalClientID := os.Getenv("PAYPAL_CLIENT_ID")
	paypalClientSecret := os.Getenv("PAYPAL_CLIENT_SECRET")
	paypalBaseURL := os.Getenv("PAYPAL_BASE_URL")
	paypalMode := os.Getenv("PAYPAL_MODE")
	if paypalMode == "" {
		paypalMode = "sandbox"
	}
	if paypalBaseURL == "" {
		if strings.ToLower(paypalMode) == "sandbox" {
			paypalBaseURL = "https://api-m.sandbox.paypal.com"
		} else {
			paypalBaseURL = "https://api-m.paypal.com"
		}
	}
	paypalWebhookID := os.Getenv("PAYPAL_WEBHOOK_ID")

	return &Config{
		Port:               port,
		DatabaseURL:        dbURL,
		Environment:        env,
		CORSAllowedOrigins: corsOrigins,
		SupabaseURL:        supabaseURL,
		SupabaseJWTSecret:  supabaseJWTSecret,
		SupabaseJWKSURL:    supabaseJWKSURL,
		SupabaseAuthMode:   supabaseAuthMode,
		PayPalClientID:     paypalClientID,
		PayPalClientSecret: paypalClientSecret,
		PayPalBaseURL:      paypalBaseURL,
		PayPalMode:         paypalMode,
		PayPalWebhookID:    paypalWebhookID,
	}
}

// ValidatePayPal validates that PayPal sandbox credentials and configuration are properly set.
func (c *Config) ValidatePayPal() error {
	if strings.ToLower(c.PayPalMode) != "sandbox" {
		return fmt.Errorf("only sandbox mode is supported in this phase, got: %s", c.PayPalMode)
	}
	if c.PayPalClientID == "" {
		return errors.New("PAYPAL_CLIENT_ID is required")
	}
	if c.PayPalClientSecret == "" {
		return errors.New("PAYPAL_CLIENT_SECRET is required")
	}
	if !strings.HasPrefix(c.PayPalBaseURL, "https://") {
		return errors.New("PAYPAL_BASE_URL must use HTTPS protocol")
	}
	return nil
}

// loadDotEnv parses a simple KEY=VALUE file and sets environment variables if not already set.
func loadDotEnv(filepath string) {
	file, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
	}
}
