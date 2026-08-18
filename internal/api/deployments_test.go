package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/auth"
	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/docker"
	"github.com/SpritexAI/SpritexDock/internal/worker"
)

// mockDockerAdapter implements docker.Operation for API tests.
type mockDockerAdapter struct {
	buildFn       func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error)
	startFn       func(ctx context.Context, opts docker.StartContainerOptions) (string, error)
	stopFn        func(ctx context.Context, containerID string) error
	removeImageFn func(ctx context.Context, reference string) error
	nameFn        func(slug, deploymentID string) string
	inspectFn     func(ctx context.Context, containerID string) (docker.ContainerInfo, error)
}

func (m *mockDockerAdapter) Build(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
	if m.buildFn != nil {
		return m.buildFn(ctx, req)
	}
	return docker.BuildResult{ImageReference: "spritexdock/" + req.ApplicationSlug + ":" + req.DeploymentID}, nil
}

func (m *mockDockerAdapter) StartContainer(ctx context.Context, opts docker.StartContainerOptions) (string, error) {
	if m.startFn != nil {
		return m.startFn(ctx, opts)
	}
	return "container-123", nil
}

func (m *mockDockerAdapter) StopContainer(ctx context.Context, containerID string) error {
	if m.stopFn != nil {
		return m.stopFn(ctx, containerID)
	}
	return nil
}

func (m *mockDockerAdapter) RemoveImage(ctx context.Context, reference string) error {
	if m.removeImageFn != nil {
		return m.removeImageFn(ctx, reference)
	}
	return nil
}

func (m *mockDockerAdapter) ContainerName(slug, deploymentID string) string {
	if m.nameFn != nil {
		return m.nameFn(slug, deploymentID)
	}
	return docker.ContainerName(slug, deploymentID)
}

func (m *mockDockerAdapter) InspectContainer(ctx context.Context, containerID string) (docker.ContainerInfo, error) {
	if m.inspectFn != nil {
		return m.inspectFn(ctx, containerID)
	}
	return docker.ContainerInfo{ID: containerID, State: "running", Running: true, ExitCode: 0}, nil
}

func TestDeploymentAPIFlow(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	if err := auth.SeedOwner(context.Background(), state, "owner", "password"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		PublicIP:        "203.0.113.10",
		BuildMemLimit:   512 * 1024 * 1024,
		BuildCPULimit:   1.0,
		BuildTimeout:    15 * time.Minute,
		RuntimeMemLimit: 512 * 1024 * 1024,
		RuntimeCPULimit: 1.0,
	}

	mockDockerOp := &mockDockerAdapter{}
	deployWorker := worker.New(state, mockDockerOp, cfg)

	app := fiber.New()
	RegisterRoutes(app, state, cfg, deployWorker)

	cookies := loginForApplications(t, app)
	csrfToken, cookies := csrfForApplications(t, app, cookies)

	// Create an application first
	createReq := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader(`{"name":"Deploy App","slug":"deploy-app","repository_url":"https://github.com/example/app.git","exposed_port":8080}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Cookie", cookies)
	createReq.Header.Set("X-Csrf-Token", csrfToken)
	response, err := app.Test(createReq, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create app status = %d, want 201", response.StatusCode)
	}
	var createdApp application.Application
	if err := json.NewDecoder(response.Body).Decode(&createdApp); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	// Trigger deployment
	deployReq := httptest.NewRequest(http.MethodPost, "/applications/"+createdApp.ID+"/deployments", strings.NewReader(`{"revision_commit_sha":"abcdef1234567890abcdef1234567890abcdef12","trigger_type":"manual"}`))
	deployReq.Header.Set("Content-Type", "application/json")
	deployReq.Header.Set("Cookie", cookies)
	deployReq.Header.Set("X-Csrf-Token", csrfToken)
	response, err = app.Test(deployReq, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("trigger deploy status = %d, want 202; error response: %s", response.StatusCode, responseBody(t, response))
	}
	var queuedDeploy deployment.Deployment
	if err := json.NewDecoder(response.Body).Decode(&queuedDeploy); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	// Try triggering a second deployment immediately - expect StatusConflict (409)
	deployReq2 := httptest.NewRequest(http.MethodPost, "/applications/"+createdApp.ID+"/deployments", strings.NewReader(`{"revision_commit_sha":"1234567890abcdef1234567890abcdef12345678","trigger_type":"webhook"}`))
	deployReq2.Header.Set("Content-Type", "application/json")
	deployReq2.Header.Set("Cookie", cookies)
	deployReq2.Header.Set("X-Csrf-Token", csrfToken)
	response, err = app.Test(deployReq2, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("second deploy status = %d, want 409", response.StatusCode)
	}
	_ = response.Body.Close()

	// Wait for background deployment to progress to running
	time.Sleep(100 * time.Millisecond)

	// Get deployment list
	listReq := httptest.NewRequest(http.MethodGet, "/applications/"+createdApp.ID+"/deployments", nil)
	listReq.Header.Set("Cookie", cookies)
	response, err = app.Test(listReq, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list deployments status = %d, want 200", response.StatusCode)
	}
	var list []deployment.Deployment
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(list) != 1 {
		t.Fatalf("deployments list len = %d, want 1", len(list))
	}

	// Get application status
	statusReq := httptest.NewRequest(http.MethodGet, "/applications/"+createdApp.ID+"/status", nil)
	statusReq.Header.Set("Cookie", cookies)
	response, err = app.Test(statusReq, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status status = %d, want 200", response.StatusCode)
	}
	var report worker.StatusReport
	if err := json.NewDecoder(response.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if report.ApplicationID != createdApp.ID {
		t.Errorf("status report app ID = %s, want %s", report.ApplicationID, createdApp.ID)
	}
}
