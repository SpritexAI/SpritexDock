package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/auth"
	"github.com/SpritexAI/SpritexDock/internal/db"
)

func TestLoginAndLogout(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if err := auth.SeedOwner(context.Background(), state, "owner", "correct password"); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	RegisterRoutes(app, state)

	response := request(t, app, http.MethodPost, "/login", `{"username":"owner","password":"wrong password"}`, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid login status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	if body := responseBody(t, response); body != `{"error":"invalid credentials"}` {
		t.Fatalf("invalid login body = %s", body)
	}

	response = request(t, app, http.MethodPost, "/login", `{"username":"owner","password":"correct password"}`, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	cookies := cookieHeader(response)
	if cookies == "" {
		t.Fatal("login did not set a session cookie")
	}

	response = request(t, app, http.MethodGet, "/csrf", nil, cookies)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("csrf status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var csrf struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&csrf); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if csrf.Token == "" {
		t.Fatal("csrf token is empty")
	}
	cookies = mergeCookies(cookies, response)

	logoutRequest := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutRequest.Header.Set("Cookie", cookies)
	logoutRequest.Header.Set("X-Csrf-Token", csrf.Token)
	response, err = app.Test(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}

func TestAuthenticationMiddlewareRejectsUnauthenticatedRequests(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	app := fiber.New()
	sessions := registerAuthRoutes(app, state)
	app.Get("/protected", requireAuth(sessions), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	})

	response := request(t, app, http.MethodGet, "/protected", nil, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("protected status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	_ = response.Body.Close()
}

func TestLoginRateLimit(t *testing.T) {

	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if err := auth.SeedOwner(context.Background(), state, "owner", "correct password"); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	RegisterRoutes(app, state)
	for attempt := 0; attempt < 5; attempt++ {
		response := request(t, app, http.MethodPost, "/login", `{"username":"owner","password":"wrong"}`, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, response.StatusCode, http.StatusUnauthorized)
		}
		_ = response.Body.Close()
	}
	response := request(t, app, http.MethodPost, "/login", `{"username":"owner","password":"wrong"}`, "")
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("sixth login status = %d, want %d", response.StatusCode, http.StatusTooManyRequests)
	}
}

func request(t *testing.T, app *fiber.App, method, path string, body any, cookies string) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body.(string))
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookies != "" {
		request.Header.Set("Cookie", cookies)
	}
	response, err := app.Test(request, -1)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func responseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func cookieHeader(response *http.Response) string {
	var cookies []string
	for _, cookie := range response.Cookies() {
		cookies = append(cookies, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(cookies, "; ")
}

func mergeCookies(existing string, response *http.Response) string {
	values := map[string]string{}
	for _, part := range strings.Split(existing, "; ") {
		key, value, ok := strings.Cut(part, "=")
		if ok {
			values[key] = value
		}
	}
	for _, cookie := range response.Cookies() {
		values[cookie.Name] = cookie.Value
	}
	parts := make([]string, 0, len(values))
	for key, value := range values {
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, "; ")
}
