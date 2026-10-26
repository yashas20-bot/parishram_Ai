package config

import (
	"bufio"
	"os"
	"strings"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	Port               string
	DatabaseURL        string
	Environment        string
	CORSAllowedOrigins string
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

	return &Config{
		Port:               port,
		DatabaseURL:        dbURL,
		Environment:        env,
		CORSAllowedOrigins: corsOrigins,
	}
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
