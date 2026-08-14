package api

import (
	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
)

// RegisterRoutes attaches the control-plane routes to app.
func RegisterRoutes(app *fiber.App, state *db.DB, cfg *config.Config) {
	app.Get("/health", health)
	sessions, csrfMiddleware := registerAuthRoutes(app, state)
	registerApplicationRoutes(app, state, cfg, sessions, csrfMiddleware)
}

// health is a liveness check. Dependency readiness checks belong to later slices.
func health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}
