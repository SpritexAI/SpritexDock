package api

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/csrf"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/SpritexAI/SpritexDock/internal/auth"
	"github.com/SpritexAI/SpritexDock/internal/db"
)

const (
	authenticatedUserKey = "spritexdock.user_id"
	csrfTokenKey         = "spritexdock.csrf_token"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// registerAuthRoutes adds the authentication routes and returns the session store.
func registerAuthRoutes(app *fiber.App, state *db.DB) *session.Store {
	sessions := session.New(session.Config{
		KeyLookup:      "cookie:spritexdock_session",
		Expiration:     24 * time.Hour,
		CookieSecure:   true,
		CookieHTTPOnly: true,
		CookieSameSite: "Lax",
	})

	csrfMiddleware := csrf.New(csrf.Config{
		KeyLookup:      "header:X-Csrf-Token",
		CookieName:     "spritexdock_csrf",
		CookieSecure:   true,
		CookieHTTPOnly: false,
		CookieSameSite: "Lax",
		ContextKey:     csrfTokenKey,
		ErrorHandler:   csrfError,
		Session:        sessions,
		SessionKey:     "spritexdock.csrf",
		Expiration:     time.Hour,
	})

	loginLimiter := limiter.New(limiter.Config{
		Max:        5,
		Expiration: time.Minute,
	})

	app.Get("/csrf", csrfMiddleware, csrfToken)
	app.Post("/login", loginLimiter, loginHandler(state, sessions))
	app.Post("/logout", csrfMiddleware, requireAuth(sessions), logoutHandler(sessions))
	return sessions
}

func loginHandler(state *db.DB, sessions *session.Store) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var request loginRequest
		if err := c.BodyParser(&request); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
		defer cancel()
		userID, err := auth.Authenticate(ctx, state, request.Username, request.Password)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid credentials"})
		}

		sess, err := sessions.Get(c)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "authentication unavailable"})
		}
		if err := sess.Regenerate(); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "authentication unavailable"})
		}
		sess.Set(authenticatedUserKey, userID)
		if err := sess.Save(); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "authentication unavailable"})
		}
		return c.JSON(fiber.Map{"status": "authenticated"})
	}
}

func logoutHandler(sessions *session.Store) fiber.Handler {
	return func(c *fiber.Ctx) error {
		sess, err := sessions.Get(c)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "authentication unavailable"})
		}
		if err := sess.Destroy(); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "authentication unavailable"})
		}
		return c.JSON(fiber.Map{"status": "logged_out"})
	}
}

func requireAuth(sessions *session.Store) fiber.Handler {
	return func(c *fiber.Ctx) error {
		sess, err := sessions.Get(c)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		userID, ok := sess.Get(authenticatedUserKey).(string)
		if !ok || userID == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		c.Locals(authenticatedUserKey, userID)
		return c.Next()
	}
}

func csrfToken(c *fiber.Ctx) error {
	token, ok := c.Locals(csrfTokenKey).(string)
	if !ok || token == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "csrf unavailable"})
	}
	return c.JSON(fiber.Map{"token": token})
}

func csrfError(c *fiber.Ctx, _ error) error {
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "csrf validation failed"})
}
