package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/domain"
	"github.com/SpritexAI/SpritexDock/internal/proxy"
)

const domainOperationTimeout = 5 * time.Second

type domainCreateRequest struct {
	Hostname string `json:"hostname"`
}

type domainCreateResponse struct {
	Domain      *domain.Domain   `json:"domain"`
	RequiredDNS domain.DNSRecord `json:"required_dns"`
}

// registerDomainRoutes attaches the custom domain CRUD and verification routes Below
// /applications/:id/domains.
func registerDomainRoutes(app *fiber.App, state *db.DB, cfg *config.Config, router proxy.Router, sessions *session.Store, csrfMiddleware fiber.Handler) {
	routes := app.Group("/applications/:id", requireAuth(sessions))
	routes.Post("/domains", csrfMiddleware, addDomainHandler(state, cfg))
	routes.Get("/domains", listDomainsHandler(state))
	routes.Delete("/domains/:domain", csrfMiddleware, deleteDomainHandler(state, router))
	routes.Post("/domains/:domain/verify", csrfMiddleware, verifyDomainHandler(state, cfg, router))
}

func addDomainHandler(state *db.DB, cfg *config.Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req domainCreateRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), domainOperationTimeout)
		defer cancel()

		appID := c.Params("id")
		if _, err := application.Get(ctx, state, appID); errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		} else if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "database error"})
		}

		item, err := domain.Add(ctx, state, appID, req.Hostname)
		if errors.Is(err, domain.ErrInvalidHostname) {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if errors.Is(err, domain.ErrDuplicateDomain) {
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "domain already exists"})
		}
		if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to add domain"})
		}

		var publicIP string
		if cfg != nil {
			publicIP = cfg.PublicIP
		}
		dns := domain.RequiredDNSRecord(item.Hostname, publicIP)

		return c.Status(http.StatusCreated).JSON(domainCreateResponse{
			Domain:      item,
			RequiredDNS: dns,
		})
	}
}

func listDomainsHandler(state *db.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), domainOperationTimeout)
		defer cancel()

		appID := c.Params("id")
		if _, err := application.Get(ctx, state, appID); errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		} else if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "database error"})
		}

		items, err := domain.List(ctx, state, appID)
		if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to list domains"})
		}
		if items == nil {
			items = []*domain.Domain{}
		}
		return c.JSON(items)
	}
}

func deleteDomainHandler(state *db.DB, router proxy.Router) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), domainOperationTimeout)
		defer cancel()

		appID := c.Params("id")
		if _, err := application.Get(ctx, state, appID); errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		} else if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "database error"})
		}

		hostname := c.Params("domain")
		err := domain.Delete(ctx, state, appID, hostname)
		if errors.Is(err, domain.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "domain not found"})
		}
		if errors.Is(err, domain.ErrInvalidHostname) {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to delete domain"})
		}

		// Cleanup routing for the removed domain. Removing an unknown hostname
		// is a no-op so this stays safe and idempotent.
		if router != nil {
			if parseErr := router.RemoveRoute(ctx, hostname); parseErr != nil {
				slog.WarnContext(ctx, "failed to delete proxy route for domain", "application", appID, "domain", hostname, "error", parseErr)
			}
		}

		return c.SendStatus(http.StatusNoContent)
	}
}

func verifyDomainHandler(state *db.DB, cfg *config.Config, router proxy.Router) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), domainOperationTimeout)
		defer cancel()

		appID := c.Params("id")
		if _, err := application.Get(ctx, state, appID); errors.Is(err, application.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "application not found"})
		} else if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "database error"})
		}

		var publicIP string
		if cfg != nil {
			publicIP = cfg.PublicIP
		}
		service := &domain.Service{
			State:    state,
			PublicIP: publicIP,
			Router:   router,
		}

		hostname := c.Params("domain")
		verified, err := service.VerifyDomain(ctx, appID, hostname)
		if errors.Is(err, domain.ErrNotFound) {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "domain not found"})
		}
		if errors.Is(err, domain.ErrInvalidHostname) {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}

		responseStatus := http.StatusOK
		if err != nil {
			// If verification failed but the record updated, return StatusConflict (409)
			// with the verification error reason to signal validation failure.
			responseStatus = http.StatusConflict
		}
		return c.Status(responseStatus).JSON(verified)
	}
}
