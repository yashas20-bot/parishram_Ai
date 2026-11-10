package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"parishram-ai/backend/internal/config"
	"parishram-ai/backend/internal/controllers"
	db "parishram-ai/backend/internal/db/sqlc"
	"parishram-ai/backend/internal/middleware"
	"parishram-ai/backend/internal/paypal"
	"parishram-ai/backend/internal/routes"
)

func main() {
	cfg := config.LoadConfig()

	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize PostgreSQL connection pool
	var pool *pgxpool.Pool
	var querier db.Querier

	dbPool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Printf("[WARN] Failed to configure database connection pool: %v", err)
	} else {
		// Ping database to verify initial connectivity
		pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
		if pingErr := dbPool.Ping(pingCtx); pingErr != nil {
			log.Printf("[WARN] Database pool created but ping failed: %v", pingErr)
		} else {
			log.Printf("[INFO] Successfully connected to PostgreSQL")
		}
		pingCancel()

		pool = dbPool
		querier = db.New(dbPool)
		defer dbPool.Close()
	}

	// Initialize controllers with SQLC Querier and dbPool
	healthController := controllers.NewHealthController(querier, pool)
	budgetController := controllers.NewBudgetController(querier)
	expenseController := controllers.NewExpenseController(querier)
	purchaseController := controllers.NewPurchaseController(querier)

	var paymentController *controllers.PaymentController
	if querier != nil {
		if err := cfg.ValidatePayPal(); err != nil {
			log.Printf("[WARN] PayPal configuration incomplete or invalid: %v. PayPal payment routes disabled.", err)
		} else {
			paypalClient := paypal.NewClient(cfg.PayPalBaseURL, cfg.PayPalClientID, cfg.PayPalClientSecret, 15*time.Second)
			paymentController = controllers.NewPaymentController(querier, paypalClient)
			log.Printf("[INFO] PayPal Sandbox client initialized (mode: %s, base_url: %s)", cfg.PayPalMode, cfg.PayPalBaseURL)
		}
	}

	// Initialize authentication validator and middleware
	var authMiddleware gin.HandlerFunc
	if querier != nil {
		if validator, err := middleware.NewTokenValidatorFromConfig(cfg); err != nil {
			log.Printf("[WARN] Supabase JWT validator not configured: %v", err)
		} else {
			authMiddleware = middleware.RequireAuth(validator, querier)
			log.Printf("[INFO] Supabase JWT authentication middleware enabled (algorithms: %v)", validator.SupportedAlgorithms())
		}
	}

	// Setup routes
	router := routes.SetupRouter(healthController, budgetController, expenseController, purchaseController, paymentController, cfg.CORSAllowedOrigins, authMiddleware)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	// Start server in background goroutine
	go func() {
		log.Printf("[INFO] Parishram AI Backend starting on port %s (env: %s)", cfg.Port, cfg.Environment)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Listen error: %s", err)
		}
	}()

	// Graceful shutdown handling
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[INFO] Shutting down Parishram AI server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("[FATAL] Server forced to shutdown: %v", err)
	}

	log.Println("[INFO] Server exited gracefully")
}
