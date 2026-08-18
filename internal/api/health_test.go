package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
)

func TestHealthEndpoint(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	app := fiber.New()
	RegisterRoutes(app, state, &config.Config{PublicIP: "203.0.113.10"}, nil)

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}
