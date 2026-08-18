package api

import (
	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/worker"
)

// RegisterRoutes attaches the control-plane routes to app. A nil deployWorker
// disables the deployment trigger and status routes (used in tests).
func RegisterRoutes(app *fiber.App, state *db.DB, cfg *config.Config, deployWorker *worker.Worker) {
	app.Get("/health", health)
	sessions, csrfMiddleware := registerAuthRoutes(app, state)
	registerApplicationRoutes(app, state, cfg, sessions, csrfMiddleware)
	if deployWorker != nil {
		registerDeploymentRoutes(app, state, deployWorker, sessions, csrfMiddleware)
		registerDomainRoutes(app, state, cfg, deployWorker.Router, sessions, csrfMiddleware)
	} else {
		registerDomainRoutes(app, state, cfg, nil, sessions, csrfMiddleware)
	}
}

// health is a liveness check. Dependency readiness checks belong to later slices.
func health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}
