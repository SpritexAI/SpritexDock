package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/auth"
	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
)

func TestApplicationRoutes(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if err := auth.SeedOwner(context.Background(), state, "owner", "password"); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	RegisterRoutes(app, state, &config.Config{PublicIP: "203.0.113.10"}, nil)

	response := request(t, app, http.MethodGet, "/applications", nil, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	_ = response.Body.Close()

	cookies := loginForApplications(t, app)
	response = request(t, app, http.MethodPost, "/applications", `{"name":"My App","slug":"my-app","repository_url":"https://github.com/example/app.git","exposed_port":8080}`, cookies)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	_ = response.Body.Close()

	csrfToken, cookies := csrfForApplications(t, app, cookies)
	createRequest := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader(`{"name":"My App","slug":"my-app","repository_url":"https://github.com/example/app.git","exposed_port":8080}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Cookie", cookies)
	createRequest.Header.Set("X-Csrf-Token", csrfToken)
	response, err = app.Test(createRequest, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	var created application.Application
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if created.Branch != "main" || created.GeneratedHostname != "my-app.203-0-113-10.sslip.io" {
		t.Fatalf("created = %+v", created)
	}

	response = request(t, app, http.MethodGet, "/applications/"+created.ID, nil, cookies)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	_ = response.Body.Close()

	response = request(t, app, http.MethodGet, "/applications", nil, cookies)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	_ = response.Body.Close()

	updateRequest := httptest.NewRequest(http.MethodPut, "/applications/"+created.ID, strings.NewReader(`{"name":"Updated","slug":"updated","repository_url":"https://github.com/example/app.git","branch":"release","exposed_port":9090}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.Header.Set("Cookie", cookies)
	updateRequest.Header.Set("X-Csrf-Token", csrfToken)
	response, err = app.Test(updateRequest, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	_ = response.Body.Close()

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/applications/"+created.ID, nil)
	deleteRequest.Header.Set("Cookie", cookies)
	deleteRequest.Header.Set("X-Csrf-Token", csrfToken)
	response, err = app.Test(deleteRequest, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	_ = response.Body.Close()

	response = request(t, app, http.MethodGet, "/applications/"+created.ID, nil, cookies)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted get status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
	_ = response.Body.Close()
}

func TestApplicationValidationAndConflict(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if err := auth.SeedOwner(context.Background(), state, "owner", "password"); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	RegisterRoutes(app, state, &config.Config{PublicIP: "203.0.113.10"}, nil)
	cookies := loginForApplications(t, app)
	csrfToken, cookies := csrfForApplications(t, app, cookies)

	for name, body := range map[string]string{
		"slug":       `{"name":"App","slug":"Bad_slug","repository_url":"https://github.com/example/app.git","exposed_port":8080}`,
		"repository": `{"name":"App","slug":"app","repository_url":"https://user:pass@example.com/app.git","exposed_port":8080}`,
		"traversal":  `{"name":"App","slug":"app","repository_url":"https://github.com/example/app.git","dockerfile_path":"../Dockerfile","exposed_port":8080}`,
		"port":       `{"name":"App","slug":"app","repository_url":"https://github.com/example/app.git","exposed_port":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Cookie", cookies)
			request.Header.Set("X-Csrf-Token", csrfToken)
			response, err := app.Test(request, -1)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
			_ = response.Body.Close()
		})
	}

	body := `{"name":"App","slug":"app","repository_url":"https://github.com/example/app.git","exposed_port":8080}`
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Cookie", cookies)
		request.Header.Set("X-Csrf-Token", csrfToken)
		response, err := app.Test(request, -1)
		if err != nil {
			t.Fatal(err)
		}
		want := http.StatusCreated
		if attempt == 1 {
			want = http.StatusConflict
		}
		if response.StatusCode != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, response.StatusCode, want)
		}
		_ = response.Body.Close()
	}
}

func loginForApplications(t *testing.T, app *fiber.App) string {
	t.Helper()
	response := request(t, app, http.MethodPost, "/login", `{"username":"owner","password":"password"}`, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	cookies := cookieHeader(response)
	_ = response.Body.Close()
	return cookies
}

func csrfForApplications(t *testing.T, app *fiber.App, cookies string) (string, string) {
	t.Helper()
	response := request(t, app, http.MethodGet, "/csrf", nil, cookies)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("csrf status = %d", response.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	cookies = mergeCookies(cookies, response)
	_ = response.Body.Close()
	if body.Token == "" {
		t.Fatal("csrf token is empty")
	}
	return body.Token, cookies
}
