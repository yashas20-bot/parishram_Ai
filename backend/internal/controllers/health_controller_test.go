package controllers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"parishram-ai/backend/internal/controllers"
)

func TestHealthCheck_NoPool(t *testing.T) {
	gin.SetMode(gin.TestMode)

	controller := controllers.NewHealthController(nil, nil)

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request, _ = http.NewRequest(http.MethodGet, "/health", nil)

	controller.HealthCheck(ctx)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got '%v'", response["status"])
	}

	if response["database"] != "disconnected" {
		t.Errorf("expected database 'disconnected', got '%v'", response["database"])
	}
}
