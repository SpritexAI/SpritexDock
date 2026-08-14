package api

import "github.com/gofiber/fiber/v2"

// RegisterRoutes attaches the control-plane routes to app.
func RegisterRoutes(app *fiber.App) {
	app.Get("/health", health)
}

// health is a liveness check. Dependency readiness checks belong to later slices.
func health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}
