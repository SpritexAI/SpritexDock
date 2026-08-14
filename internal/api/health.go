package api

import (
	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

// RegisterRoutes attaches the control-plane routes to app.
func RegisterRoutes(app *fiber.App, state *db.DB) {
	app.Get("/health", health)
	registerAuthRoutes(app, state)
}

// health is a liveness check. Dependency readiness checks belong to later slices.
func health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}
