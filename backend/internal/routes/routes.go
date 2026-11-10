package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"parishram-ai/backend/internal/controllers"
)

// SetupRouter initializes Gin engine with middlewares and registered routes.
// The public health endpoints remain accessible without credentials,
// while all v1 business API routes require a valid Supabase JWT via authMiddleware.
func SetupRouter(
	healthController *controllers.HealthController,
	budgetController *controllers.BudgetController,
	expenseController *controllers.ExpenseController,
	purchaseController *controllers.PurchaseController,
	paymentController *controllers.PaymentController,
	allowedOrigins string,
	authMiddleware gin.HandlerFunc,
) *gin.Engine {
	r := gin.New()

	// Standard middlewares
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Basic CORS middleware
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", allowedOrigins)
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	})

	// Public health routes for load balancers and deployment monitoring
	r.GET("/health", healthController.HealthCheck)

	// API v1 grouping
	v1 := r.Group("/api/v1")
	{
		// Public health endpoint within v1
		v1.GET("/health", healthController.HealthCheck)

		// Protected v1 business routes requiring valid Supabase JWT
		protected := v1.Group("")
		if authMiddleware != nil {
			protected.Use(authMiddleware)
		}
		{
			if budgetController != nil {
				protected.GET("/budgets/me", budgetController.GetBudget)
				protected.PUT("/budgets/me", budgetController.UpdateBudget)
			}
			if expenseController != nil {
				protected.POST("/expenses", expenseController.CreateExpense)
				protected.GET("/expenses", expenseController.ListExpenses)
				protected.GET("/expenses/:id", expenseController.GetExpense)
				protected.DELETE("/expenses/:id", expenseController.DeleteExpense)
			}
			if purchaseController != nil {
				protected.POST("/purchases/evaluate", purchaseController.EvaluatePurchase)
				protected.GET("/purchases", purchaseController.ListPurchases)
				protected.GET("/purchases/:id", purchaseController.GetPurchase)
			}
			if paymentController != nil {
				protected.POST("/payments/create-order", paymentController.CreateOrder)
			}
		}
	}

	return r
}
