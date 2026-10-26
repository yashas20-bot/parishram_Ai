package controllers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	db "parishram-ai/backend/internal/db/sqlc"
)

// HealthController handles application and database health checks.
type HealthController struct {
	querier db.Querier
	dbPool  *pgxpool.Pool
}

// NewHealthController initializes a new HealthController.
func NewHealthController(querier db.Querier, dbPool *pgxpool.Pool) *HealthController {
	return &HealthController{
		querier: querier,
		dbPool:  dbPool,
	}
}

// HealthCheck responds with current application status and database connectivity.
func (c *HealthController) HealthCheck(ctx *gin.Context) {
	dbStatus := "disconnected"

	if c.dbPool != nil {
		pingCtx, cancel := context.WithTimeout(ctx.Request.Context(), 2*time.Second)
		defer cancel()

		if err := c.dbPool.Ping(pingCtx); err == nil {
			dbStatus = "connected"
		} else {
			dbStatus = "unreachable"
		}
	}

	statusCode := http.StatusOK
	if dbStatus == "unreachable" {
		statusCode = http.StatusServiceUnavailable
	}

	ctx.JSON(statusCode, gin.H{
		"status":    "healthy",
		"service":   "parishram-ai-backend",
		"database":  dbStatus,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
