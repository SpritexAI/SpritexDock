package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/docker"
)

// mockDocker is a test double for docker.Operation.
type mockDocker struct {
	buildFn       func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error)
	startFn       func(ctx context.Context, opts docker.StartContainerOptions) (string, error)
	stopFn        func(ctx context.Context, containerID string) error
	removeImageFn func(ctx context.Context, reference string) error
	nameFn        func(slug, deploymentID string) string
	inspectFn     func(ctx context.Context, containerID string) (docker.ContainerInfo, error)
}

func (m *mockDocker) Build(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
	if m.buildFn != nil {
		return m.buildFn(ctx, req)
	}
	return docker.BuildResult{}, errors.New("build not mocked")
}

func (m *mockDocker) StartContainer(ctx context.Context, opts docker.StartContainerOptions) (string, error) {
	if m.startFn != nil {
		return m.startFn(ctx, opts)
	}
	return "", errors.New("start not mocked")
}

func (m *mockDocker) StopContainer(ctx context.Context, containerID string) error {
	if m.stopFn != nil {
		return m.stopFn(ctx, containerID)
	}
	return nil
}

func (m *mockDocker) RemoveImage(ctx context.Context, reference string) error {
	if m.removeImageFn != nil {
		return m.removeImageFn(ctx, reference)
	}
	return nil
}

func (m *mockDocker) ContainerName(slug, deploymentID string) string {
	if m.nameFn != nil {
		return m.nameFn(slug, deploymentID)
	}
	return docker.ContainerName(slug, deploymentID)
}

func (m *mockDocker) InspectContainer(ctx context.Context, containerID string) (docker.ContainerInfo, error) {
	if m.inspectFn != nil {
		return m.inspectFn(ctx, containerID)
	}
	return docker.ContainerInfo{}, errors.New("inspect not mocked")
}

func newTestWorker(t *testing.T) (*Worker, *db.DB) {
	t.Helper()
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		BuildMemLimit:   512 * 1024 * 1024,
		BuildCPULimit:   1.0,
		BuildTimeout:    15 * time.Minute,
		RuntimeMemLimit: 512 * 1024 * 1024,
		RuntimeCPULimit: 1.0,
	}
	mock := &mockDocker{}
	w := New(state, mock, nil, cfg)
	return w, state
}

func TestWorkerSuccess(t *testing.T) {
	w, state := newTestWorker(t)
	appID := "app-1"

	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	// Create deployment
	deploy, err := deployment.CreateQueued(context.Background(), state, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}
	deployID := deploy.ID

	mock := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			return docker.BuildResult{ImageReference: "spritexdock/myapp:" + req.DeploymentID, Logs: "build logs"}, nil
		},
		startFn: func(ctx context.Context, opts docker.StartContainerOptions) (string, error) {
			return "container-abc123", nil
		},
	}
	w.docker = mock

	result, err := w.Run(context.Background(), RunRequest{
		DeploymentID:   deployID,
		ApplicationID:  appID,
		Slug:           "myapp",
		RepositoryURL:  "https://github.com/example/app.git",
		Branch:         "main",
		CommitSHA:      "abcdef1234567890abcdef1234567890abcdef12",
		DockerfilePath: "Dockerfile",
		BuildContext:   ".",
		ExposedPort:    8080,
		EnvVars:        []string{"APP_ENV=prod", "SECRET=hidden"},
		TriggerType:    "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.BuildSuccess {
		t.Error("expected build to succeed")
	}
	if !result.DeploySuccess {
		t.Error("expected deploy to succeed")
	}
	if result.ContainerID != "container-abc123" {
		t.Errorf("container id = %q, want container-abc123", result.ContainerID)
	}
	if result.CleanupError != nil {
		t.Errorf("unexpected cleanup error: %v", result.CleanupError)
	}

	// Verify state transitions
	deploy, err = deployment.Get(context.Background(), state, deployID)
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Status != deployment.StatusRunning {
		t.Errorf("status = %q, want %q", deploy.Status, deployment.StatusRunning)
	}
	if deploy.ContainerID == nil || *deploy.ContainerID != "container-abc123" {
		t.Errorf("container_id = %v, want container-abc123", deploy.ContainerID)
	}
}

func TestWorkerBuildFailure(t *testing.T) {
	w, state := newTestWorker(t)
	appID := "app-2"

	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	deploy, err := deployment.CreateQueued(context.Background(), state, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}
	deployID := deploy.ID

	mock := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			return docker.BuildResult{Logs: "build error output"}, errors.New("docker build failed")
		},
	}
	w.docker = mock

	result, err := w.Run(context.Background(), RunRequest{
		DeploymentID:   deployID,
		ApplicationID:  appID,
		Slug:           "myapp",
		RepositoryURL:  "https://github.com/example/app.git",
		Branch:         "main",
		CommitSHA:      "abcdef1234567890abcdef1234567890abcdef12",
		DockerfilePath: "Dockerfile",
		BuildContext:   ".",
		ExposedPort:    8080,
		TriggerType:    "manual",
	})
	if err == nil {
		t.Fatal("expected error from build failure")
	}
	if result.BuildSuccess {
		t.Error("expected build to fail")
	}
	if !strings.Contains(result.BuildLogs, "build error output") {
		t.Errorf("build logs = %q, want build error output", result.BuildLogs)
	}
	if result.ContainerID != "" {
		t.Errorf("unexpected container id: %q", result.ContainerID)
	}

	// Verify deployment is marked failed
	deploy, err = deployment.Get(context.Background(), state, deployID)
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Status != deployment.StatusFailed {
		t.Errorf("status = %q, want %q", deploy.Status, deployment.StatusFailed)
	}
	if deploy.ContainerID != nil {
		t.Errorf("container_id = %v, want nil", deploy.ContainerID)
	}
}

func TestWorkerContainerStartFailure(t *testing.T) {
	w, state := newTestWorker(t)
	appID := "app-3"

	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	deploy, err := deployment.CreateQueued(context.Background(), state, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}
	deployID := deploy.ID

	mock := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			return docker.BuildResult{ImageReference: "spritexdock/myapp:" + req.DeploymentID, Logs: "build ok"}, nil
		},
		startFn: func(ctx context.Context, opts docker.StartContainerOptions) (string, error) {
			return "", errors.New("docker start failed")
		},
	}
	w.docker = mock

	result, err := w.Run(context.Background(), RunRequest{
		DeploymentID:   deployID,
		ApplicationID:  appID,
		Slug:           "myapp",
		RepositoryURL:  "https://github.com/example/app.git",
		Branch:         "main",
		CommitSHA:      "abcdef1234567890abcdef1234567890abcdef12",
		DockerfilePath: "Dockerfile",
		BuildContext:   ".",
		ExposedPort:    8080,
		TriggerType:    "manual",
	})
	if err == nil {
		t.Fatal("expected error from container start failure")
	}
	if !result.BuildSuccess {
		t.Error("expected build to succeed")
	}
	if result.DeploySuccess {
		t.Error("expected deploy to fail")
	}
	if result.ContainerID != "" {
		t.Errorf("unexpected container id: %q", result.ContainerID)
	}

	// Verify deployment is marked failed
	deploy, err = deployment.Get(context.Background(), state, deployID)
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Status != deployment.StatusFailed {
		t.Errorf("status = %q, want %q", deploy.Status, deployment.StatusFailed)
	}
}

func TestWorkerMidDeployRecovery(t *testing.T) {
	_, state := newTestWorker(t)
	appID := "app-4"

	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a build that was in progress when server crashed
	deploy, err := deployment.CreateQueued(context.Background(), state, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}
	deployID := deploy.ID

	err = deployment.Transition(context.Background(), state, deployID, deployment.StatusBuilding, "", "")
	if err != nil {
		t.Fatal(err)
	}

	count, err := deployment.RecoverInterrupted(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("recovered %d, want 1", count)
	}

	deploy, err = deployment.Get(context.Background(), state, deployID)
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Status != deployment.StatusFailed {
		t.Errorf("status = %q, want %q", deploy.Status, deployment.StatusFailed)
	}
	if deploy.FailureReason == nil || !strings.Contains(*deploy.FailureReason, "restart") {
		t.Errorf("failure reason = %v, want restart message", deploy.FailureReason)
	}
}

func TestWorkerSingleActiveDeployment(t *testing.T) {
	w, state := newTestWorker(t)
	appID := "app-5"
	deployID1 := "deploy-5a"
	deployID2 := "deploy-5b"

	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	// First deployment is already in progress (queued)
	_, err = state.Exec(`INSERT INTO deployments
		(id, application_id, revision_commit_sha, trigger_type, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deployID1, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual", "queued", "now")
	if err != nil {
		t.Fatal(err)
	}

	// Second deployment should be rejected
	_, err = state.Exec(`INSERT INTO deployments
		(id, application_id, revision_commit_sha, trigger_type, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deployID2, appID, "1234567890abcdef1234567890abcdef12345678", "manual", "queued", "now")
	if err != nil {
		t.Fatal(err)
	}

	// Try to run second deployment
	mock := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			return docker.BuildResult{ImageReference: "spritexdock/myapp:" + req.DeploymentID, Logs: ""}, nil
		},
		startFn: func(ctx context.Context, opts docker.StartContainerOptions) (string, error) {
			return "container-xyz", nil
		},
	}
	w.docker = mock

	result, err := w.Run(context.Background(), RunRequest{
		DeploymentID:  deployID2,
		ApplicationID: appID,
		Slug:          "myapp",
		ExposedPort:   8080,
		TriggerType:   "manual",
	})
	if err == nil {
		t.Fatal("expected error when another deployment is in progress")
	}
	if !errors.Is(err, deployment.ErrDeployQueued) {
		t.Errorf("error = %v, want ErrDeployQueued", err)
	}
	if result.BuildSuccess {
		t.Error("expected build to not start")
	}
}

func TestWorkerCleanupOnError(t *testing.T) {
	w, state := newTestWorker(t)
	appID := "app-6"

	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	deploy, err := deployment.CreateQueued(context.Background(), state, appID, "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}
	deployID := deploy.ID

	cleanupCalled := false
	stopCalled := false
	mock := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			return docker.BuildResult{ImageReference: "spritexdock/myapp:" + req.DeploymentID, Logs: "build failed"}, errors.New("build error")
		},
		removeImageFn: func(ctx context.Context, reference string) error {
			cleanupCalled = true
			return nil
		},
		stopFn: func(ctx context.Context, containerID string) error {
			stopCalled = true
			return nil
		},
	}
	w.docker = mock

	result, err := w.Run(context.Background(), RunRequest{
		DeploymentID:  deployID,
		ApplicationID: appID,
		Slug:          "myapp",
		ExposedPort:   8080,
		TriggerType:   "manual",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.BuildSuccess {
		t.Error("expected build to fail")
	}
	// Verify cleanup was attempted (either image removal or container stop)
	if !cleanupCalled && !stopCalled {
		t.Error("expected cleanup to be called on failure")
	}
	if result.CleanupError != nil {
		t.Logf("cleanup error (expected if container not found): %v", result.CleanupError)
	}
}

func TestWorkerConcurrentSerializes(t *testing.T) {
	w, state := newTestWorker(t)
	appID := "app-concurrent"
	deployID := "deploy-7"

	// Create application and deployment
	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "myapp", "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, "myapp.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}

	_, err = state.Exec(`INSERT INTO deployments
		(id, application_id, revision_commit_sha, trigger_type, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deployID, appID, "abc123", "manual", "queued", "now")
	if err != nil {
		t.Fatal(err)
	}

	// Block the first run inside the build step so the in-flight slot is held.
	buildStarted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	mock := &mockDocker{
		buildFn: func(ctx context.Context, req docker.BuildRequest) (docker.BuildResult, error) {
			close(buildStarted)
			<-release
			return docker.BuildResult{}, errors.New("intentional failure")
		},
	}
	w.docker = mock

	go func() {
		defer close(done)
		_, _ = w.Run(context.Background(), RunRequest{DeploymentID: deployID, ApplicationID: appID, Slug: "myapp", ExposedPort: 8080, TriggerType: "manual"})
	}()

	<-buildStarted

	// Second call must fail: the first run still holds the in-flight slot.
	_, err = w.Run(context.Background(), RunRequest{DeploymentID: deployID, ApplicationID: appID, Slug: "myapp", ExposedPort: 8080, TriggerType: "manual"})
	if err == nil {
		t.Fatal("expected concurrent deployment to fail")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("error = %v, want 'already in progress'", err)
	}

	close(release)
	<-done
}
