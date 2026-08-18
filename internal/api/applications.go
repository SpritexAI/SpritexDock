package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
)

const applicationOperationTimeout = 5 * time.Second

func registerApplicationRoutes(app *fiber.App, state *db.DB, cfg *config.Config, sessions *session.Store, csrfMiddleware fiber.Handler) {
	publicIP := ""
	if cfg != nil {
		publicIP = cfg.PublicIP
	}
	routes := app.Group("/applications", requireAuth(sessions))
	routes.Get("", listApplicationsHandler(state))
	routes.Get("/:id", getApplicationHandler(state))
	routes.Post("", csrfMiddleware, createApplicationHandler(state, publicIP))
	routes.Put("/:id", csrfMiddleware, updateApplicationHandler(state, publicIP))
	routes.Delete("/:id", csrfMiddleware, deleteApplicationHandler(state))
}

func listApplicationsHandler(state *db.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), applicationOperationTimeout)
		defer cancel()
		applications, err := application.List(ctx, state)
		if err != nil {
			return applicationInternalError(c)
		}
		if applications == nil {
			applications = []*application.Application{}
		}
		for _, app := range applications {
			urls, err := app.GetAccessibleURLs(ctx, state)
			if err == nil {
				app.AccessibleURLs = urls
			}
		}
		return c.JSON(applications)
	}
}

func getApplicationHandler(state *db.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), applicationOperationTimeout)
		defer cancel()
		item, err := application.Get(ctx, state, c.Params("id"))
		if errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		}
		if err != nil {
			return applicationInternalError(c)
		}
		urls, err := item.GetAccessibleURLs(ctx, state)
		if err == nil {
			item.AccessibleURLs = urls
		}
		return c.JSON(item)
	}
}

func createApplicationHandler(state *db.DB, publicIP string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var input application.Input
		if err := c.BodyParser(&input); err != nil {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), applicationOperationTimeout)
		defer cancel()
		item, err := application.Create(ctx, state, input, publicIP)
		if errors.Is(err, application.ErrInvalidInput) {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid application"})
		}
		if errors.Is(err, application.ErrDuplicateSlug) {
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "application slug already exists"})
		}
		if err != nil {
			return applicationInternalError(c)
		}
		urls, err := item.GetAccessibleURLs(ctx, state)
		if err == nil {
			item.AccessibleURLs = urls
		}
		return c.Status(http.StatusCreated).JSON(item)
	}
}

func updateApplicationHandler(state *db.DB, publicIP string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var input application.Input
		if err := c.BodyParser(&input); err != nil {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), applicationOperationTimeout)
		defer cancel()
		item, err := application.Update(ctx, state, c.Params("id"), input, publicIP)
		if errors.Is(err, application.ErrInvalidInput) {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid application"})
		}
		if errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		}
		if errors.Is(err, application.ErrDuplicateSlug) {
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "application slug already exists"})
		}
		if err != nil {
			return applicationInternalError(c)
		}
		urls, err := item.GetAccessibleURLs(ctx, state)
		if err == nil {
			item.AccessibleURLs = urls
		}
		return c.JSON(item)
	}
}

func deleteApplicationHandler(state *db.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), applicationOperationTimeout)
		defer cancel()
		if err := application.Delete(ctx, state, c.Params("id")); errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		} else if err != nil {
			return applicationInternalError(c)
		}
		return c.SendStatus(http.StatusNoContent)
	}
}

func applicationInternalError(c *fiber.Ctx) error {
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "application operation unavailable"})
}
