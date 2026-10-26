package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"parishram-ai/backend/internal/controllers"
)

// SetupRouter initializes Gin engine with middlewares and registered routes.
func SetupRouter(healthController *controllers.HealthController, allowedOrigins string) *gin.Engine {
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

	// Health routes
	r.GET("/health", healthController.HealthCheck)

	// API v1 grouping
	v1 := r.Group("/api/v1")
	{
		v1.GET("/health", healthController.HealthCheck)
		// Future feature endpoints will be registered here (purchases, payments, budgets, recovery, drafts)
	}

	return r
}
