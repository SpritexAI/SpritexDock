package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/docker"
	"github.com/SpritexAI/SpritexDock/internal/proxy"
)

type recordingRouter struct {
	added   map[string]int
	removed map[string]bool
	err     error
}

func newRecordingRouter() *recordingRouter {
	return &recordingRouter{
		added:   make(map[string]int),
		removed: make(map[string]bool),
	}
}

func (r *recordingRouter) AddRoute(_ context.Context, hostname string, targetPort int) error {
	if r.err != nil {
		return r.err
	}
	r.added[hostname] = targetPort
	return nil
}

func (r *recordingRouter) RemoveRoute(_ context.Context, hostname string) error {
	r.removed[hostname] = true
	return nil
}

func (r *recordingRouter) ListRoutes(_ context.Context) ([]proxy.Route, error) {
	routes := make([]proxy.Route, 0, len(r.added))
	for hostname, port := range r.added {
		routes = append(routes, proxy.Route{Hostname: hostname, TargetPort: port})
	}
	return routes, nil
}

func (r *recordingRouter) Sync(_ context.Context, routes []proxy.Route) error {
	r.added = make(map[string]int)
	for _, route := range routes {
		r.added[route.Hostname] = route.TargetPort
	}
	return nil
}

func TestWorkerRouterIntegrationSuccess(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	cfg := &config.Config{
		BuildMemLimit:   512 * 1024 * 1024,
		BuildCPULimit:   1.0,
		BuildTimeout:    15 * time.Minute,
		RuntimeMemLimit: 512 * 1024 * 1024,
		RuntimeCPULimit: 1.0,
	}

	appID := "app-router-1"
	_, err = state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.203-0-113-10.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	// Insert verified custom domain.
	_, err = state.Exec(`INSERT INTO domains
		(id, application_id, hostname, type, verification_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"dom-1", appID, "www.example.com", "custom", "verified", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	deploy, err := deployment.CreateQueued(context.Background(), state, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}

	mockDockerOp := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			return docker.BuildResult{ImageReference: "spritexdock/myapp:" + req.DeploymentID, Logs: "build ok"}, nil
		},
		startFn: func(ctx context.Context, opts docker.StartContainerOptions) (string, error) {
			return "container-xyz", nil
		},
	}
	router := newRecordingRouter()
	w := New(state, mockDockerOp, router, cfg)

	result, err := w.Run(context.Background(), RunRequest{
		DeploymentID:      deploy.ID,
		ApplicationID:     appID,
		Slug:              "myapp",
		GeneratedHostname: "myapp.203-0-113-10.sslip.io",
		RepositoryURL:     "https://github.com/example/app.git",
		Branch:            "main",
		CommitSHA:         "abcdef1234567890abcdef1234567890abcdef12",
		DockerfilePath:    "Dockerfile",
		BuildContext:      ".",
		ExposedPort:       8080,
		TriggerType:       "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DeploySuccess {
		t.Fatal("expected successful deployment")
	}

	// Verify route was registered.
	if port, ok := router.added["myapp.203-0-113-10.sslip.io"]; !ok || port != 8080 {
		t.Errorf("router did not register generated hostname: %v", router.added)
	}
	if port, ok := router.added["www.example.com"]; !ok || port != 8080 {
		t.Errorf("router did not register custom domain: %v", router.added)
	}

	// Verify current deployed URL was set.
	app, err := application.Get(context.Background(), state, appID)
	if err != nil {
		t.Fatal(err)
	}
	if app.CurrentDeployedURL == nil || *app.CurrentDeployedURL != "https://myapp.203-0-113-10.sslip.io" {
		t.Errorf("current_deployed_url = %v, want https://myapp.203-0-113-10.sslip.io", app.CurrentDeployedURL)
	}
}

func TestWorkerRouterActivateFailureCleansUpAndFailsCleanly(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	cfg := &config.Config{
		BuildMemLimit:   512 * 1024 * 1024,
		BuildCPULimit:   1.0,
		BuildTimeout:    15 * time.Minute,
		RuntimeMemLimit: 512 * 1024 * 1024,
		RuntimeCPULimit: 1.0,
	}

	appID := "app-router-2"
	_, err = state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.203-0-113-10.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	deploy, err := deployment.CreateQueued(context.Background(), state, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}

	stopContainerCalled := false
	removeImageCalled := false
	mockDockerOp := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			return docker.BuildResult{ImageReference: "spritexdock/myapp:" + req.DeploymentID, Logs: "build ok"}, nil
		},
		startFn: func(ctx context.Context, opts docker.StartContainerOptions) (string, error) {
			return "container-xyz", nil
		},
		stopFn: func(ctx context.Context, containerID string) error {
			if containerID == "spritexdock-myapp-"+deploy.ID {
				stopContainerCalled = true
			}
			return nil
		},
		removeImageFn: func(ctx context.Context, reference string) error {
			if reference == "spritexdock/myapp:"+deploy.ID {
				removeImageCalled = true
			}
			return nil
		},
	}
	router := newRecordingRouter()
	router.err = errors.New("caddy is offline")
	w := New(state, mockDockerOp, router, cfg)

	_, err = w.Run(context.Background(), RunRequest{
		DeploymentID:      deploy.ID,
		ApplicationID:     appID,
		Slug:              "myapp",
		GeneratedHostname: "myapp.203-0-113-10.sslip.io",
		RepositoryURL:     "https://github.com/example/app.git",
		Branch:            "main",
		CommitSHA:         "abcdef1234567890abcdef1234567890abcdef12",
		DockerfilePath:    "Dockerfile",
		BuildContext:      ".",
		ExposedPort:       8080,
		TriggerType:       "manual",
	})
	if err == nil {
		t.Fatal("expected route failure to raise error")
	}

	// Verify deployment state is Failed.
	item, err := deployment.Get(context.Background(), state, deploy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != deployment.StatusFailed {
		t.Errorf("status = %q, want failed", item.Status)
	}
	if item.FailureReason == nil || !strings.Contains(*item.FailureReason, router.err.Error()) {
		t.Errorf("failure reason = %v, want proxy error containing %q", item.FailureReason, router.err.Error())
	}

	// Verify cleanup was called.
	if !stopContainerCalled {
		t.Error("expected container to be stopped on failure")
	}
	if !removeImageCalled {
		t.Error("expected image to be removed on failure")
	}
}

func TestWorkerRebuildRoutes(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	// Rebuild with empty DB -> no routes.
	router := newRecordingRouter()
	w := New(state, nil, router, nil)
	count, err := w.RebuildRoutes(context.Background())
	if err != nil {
		t.Fatalf("RebuildRoutes (empty): %v", err)
	}
	if count != 0 || len(router.added) != 0 {
		t.Fatalf("routes added = %v, want 0", router.added)
	}

	// Dynamic test applications:
	// App 1: Running, exposing 8080, has 1 verified custom domain.
	app1 := "app-1"
	_, err = state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, current_deployment_id, current_deployed_url, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app1, "App 1", "app1", "https://github.com/example/app1.git", "main", "Dockerfile", ".", 8080, "app1.203-0-113-10.sslip.io", "deploy-1", "https://app1.203-0-113-10.sslip.io", "running", "now", "now")
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.Exec(`INSERT INTO deployments
		(id, application_id, revision_commit_sha, trigger_type, status, container_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"deploy-1", app1, "commit1", "manual", "running", "container-1", "now")
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.Exec(`INSERT INTO domains
		(id, application_id, hostname, type, verification_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"dom-1", app1, "www.app1.com", "custom", "verified", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	// App 2: Not running (has no current deployment pointer), exposing 9090.
	app2 := "app-2"
	_, err = state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app2, "App 2", "app2", "https://github.com/example/app2.git", "main", "Dockerfile", ".", 9090, "app2.203-0-113-10.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	count, err = w.RebuildRoutes(context.Background())
	if err != nil {
		t.Fatalf("RebuildRoutes: %v", err)
	}
	if count != 2 {
		t.Errorf("routes reconciled = %d, want 2", count)
	}
	if port, ok := router.added["app1.203-0-113-10.sslip.io"]; !ok || port != 8080 {
		t.Errorf("missing app1 generated route: %v", router.added)
	}
	if port, ok := router.added["www.app1.com"]; !ok || port != 8080 {
		t.Errorf("missing app1 custom domain route: %v", router.added)
	}
	if _, ok := router.added["app2.203-0-113-10.sslip.io"]; ok {
		t.Error("unexpected route for non-running application app2")
	}
}
