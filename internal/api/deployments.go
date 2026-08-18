package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/worker"
)

const deploymentOperationTimeout = 5 * time.Second

type deployRequest struct {
	RevisionCommitSHA string `json:"revision_commit_sha"`
	TriggerType       string `json:"trigger_type"`
}

// registerDeploymentRoutes attaches the deployment trigger, history, and
// status routes below an authenticated /applications/:id group.
func registerDeploymentRoutes(app *fiber.App, state *db.DB, deployWorker *worker.Worker, sessions *session.Store, csrfMiddleware fiber.Handler) {
	routes := app.Group("/applications/:id", requireAuth(sessions))
	routes.Post("/deployments", csrfMiddleware, createDeploymentHandler(state, deployWorker))
	routes.Get("/deployments", listDeploymentsHandler(state))
	routes.Get("/status", applicationStatusHandler(state, deployWorker))
}

func createDeploymentHandler(state *db.DB, deployWorker *worker.Worker) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var request deployRequest
		if err := c.BodyParser(&request); err != nil {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
		}
		if request.TriggerType == "" {
			request.TriggerType = "manual"
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), deploymentOperationTimeout)
		defer cancel()

		app, err := application.Get(ctx, state, c.Params("id"))
		if errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		}
		if err != nil {
			return applicationInternalError(c)
		}
		if deployWorker == nil {
			return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"error": "deployment worker unavailable"})
		}

		item, err := deployWorker.Enqueue(ctx, app, request.RevisionCommitSHA, request.TriggerType)
		if errors.Is(err, deployment.ErrDeployQueued) {
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "a deployment is already in progress for this application"})
		}
		if err != nil {
			return applicationInternalError(c)
		}
		return c.Status(http.StatusAccepted).JSON(item)
	}
}

func listDeploymentsHandler(state *db.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), deploymentOperationTimeout)
		defer cancel()
		if _, err := application.Get(ctx, state, c.Params("id")); err != nil {
			if errors.Is(err, application.ErrNotFound) {
				return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
			}
			return applicationInternalError(c)
		}
		items, err := deployment.ListByApplication(ctx, state, c.Params("id"))
		if err != nil {
			return applicationInternalError(c)
		}
		if items == nil {
			items = []*deployment.Deployment{}
		}
		return c.JSON(items)
	}
}

func applicationStatusHandler(state *db.DB, deployWorker *worker.Worker) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), deploymentOperationTimeout)
		defer cancel()
		if _, err := application.Get(ctx, state, c.Params("id")); err != nil {
			if errors.Is(err, application.ErrNotFound) {
				return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
			}
			return applicationInternalError(c)
		}
		if deployWorker == nil {
			return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"error": "deployment worker unavailable"})
		}
		report, err := deployWorker.Status(ctx, c.Params("id"))
		if err != nil {
			return applicationInternalError(c)
		}
		return c.JSON(report)
	}
}
