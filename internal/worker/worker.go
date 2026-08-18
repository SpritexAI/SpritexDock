package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/docker"
	"github.com/SpritexAI/SpritexDock/internal/domain"
	"github.com/SpritexAI/SpritexDock/internal/proxy"
)

// Worker is the deployment orchestration worker.
type Worker struct {
	mu       sync.Mutex
	state    *db.DB
	docker   docker.Operation
	Router   proxy.Router
	cfg      *config.Config
	inFlight map[string]bool
}

// New creates a Worker backed by the given state, docker client, proxy
// router, and config. A nil router disables proxy-route management (used in
// tests).
func New(state *db.DB, d docker.Operation, router proxy.Router, cfg *config.Config) *Worker {
	return &Worker{
		state:    state,
		docker:   d,
		Router:   router,
		cfg:      cfg,
		inFlight: make(map[string]bool),
	}
}

// RunRequest holds all inputs required to execute one deployment.
type RunRequest struct {
	DeploymentID      string
	ApplicationID     string
	Slug              string
	GeneratedHostname string
	RepositoryURL     string
	Branch            string
	CommitSHA         string
	DockerfilePath    string
	BuildContext      string
	ExposedPort       int
	EnvVars           []string
	TriggerType       string
}

// RunResult records the final outcome and any cleanup errors observed.
type RunResult struct {
	BuildSuccess  bool
	BuildLogs     string
	DeploySuccess bool
	ContainerID   string
	CleanupError  error
	DeployError   error
}

// ContainerInfo reports live runtime container state.
type ContainerInfo struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Running  bool   `json:"running"`
	ExitCode int    `json:"exit_code"`
}

// StatusReport is the application-facing deployment/container view (PRD §7.4).
type StatusReport struct {
	ApplicationID     string                 `json:"application_id"`
	ApplicationStatus string                 `json:"application_status"`
	CurrentDeployment *deployment.Deployment `json:"current_deployment,omitempty"`
	Container         *ContainerInfo         `json:"container,omitempty"`
}

// Enqueue records a queued deployment for the application and starts the
// worker in the background. It returns the queued deployment record, or
// deployment.ErrDeployQueued when another deployment is already active
// (PRD §7.3 single-active rule).
func (w *Worker) Enqueue(ctx context.Context, app *application.Application, revisionSHA, triggerType string) (*deployment.Deployment, error) {
	if app == nil {
		return nil, fmt.Errorf("application must not be nil")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	active, err := deployment.ActiveDeploymentIDs(ctx, w.state, app.ID)
	if err != nil {
		return nil, fmt.Errorf("check active deployments: %w", err)
	}
	if len(active) > 0 {
		return nil, deployment.ErrDeployQueued
	}

	envVars, err := LoadEnvVars(ctx, w.state, app.ID)
	if err != nil {
		return nil, err
	}

	item, err := deployment.CreateQueued(ctx, w.state, app.ID, revisionSHA, triggerType)
	if err != nil {
		return nil, err
	}

	req := RunRequest{
		DeploymentID:      item.ID,
		ApplicationID:     app.ID,
		Slug:              app.Slug,
		GeneratedHostname: app.GeneratedHostname,
		RepositoryURL:     app.RepositoryURL,
		Branch:            app.Branch,
		CommitSHA:         revisionSHA,
		DockerfilePath:    app.DockerfilePath,
		BuildContext:      app.BuildContext,
		ExposedPort:       app.ExposedPort,
		EnvVars:           envVars,
		TriggerType:       triggerType,
	}

	go func() {
		if _, runErr := w.Run(context.Background(), req); runErr != nil {
			slog.Error("deployment failed", "deployment", item.ID, "error", runErr)
		}
	}()
	return item, nil
}

// Status returns the application's current deployment and live container
// state for the application API.
func (w *Worker) Status(ctx context.Context, appID string) (*StatusReport, error) {
	app, err := application.Get(ctx, w.state, appID)
	if err != nil {
		return nil, err
	}
	report := &StatusReport{
		ApplicationID:     app.ID,
		ApplicationStatus: app.Status,
	}
	if app.CurrentDeploymentID != nil && *app.CurrentDeploymentID != "" {
		item, err := deployment.Get(ctx, w.state, *app.CurrentDeploymentID)
		if err == nil {
			report.CurrentDeployment = item
			if item.ContainerID != nil && *item.ContainerID != "" {
				info, err := w.docker.InspectContainer(ctx, *item.ContainerID)
				if err == nil {
					report.Container = &ContainerInfo{
						ID:       info.ID,
						State:    info.State,
						Running:  info.Running,
						ExitCode: info.ExitCode,
					}
				}
			}
		}
	}
	return report, nil
}

// Run executes one deployment end-to-end. It is safe to call concurrently for
// different deployment IDs but rejects duplicate runs per deployment.
func (w *Worker) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	w.mu.Lock()
	if w.inFlight[req.DeploymentID] {
		w.mu.Unlock()
		return RunResult{}, fmt.Errorf("deployment %s is already in progress", req.DeploymentID)
	}
	w.inFlight[req.DeploymentID] = true
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		delete(w.inFlight, req.DeploymentID)
		w.mu.Unlock()
	}()

	// Enforce the single-active rule (PRD §7.3): reject if any other
	// deployment for this application is still queued/building/deploying.
	activeIDs, err := deployment.ActiveDeploymentIDs(ctx, w.state, req.ApplicationID)
	if err != nil {
		return RunResult{}, fmt.Errorf("check active deployment: %w", err)
	}
	for _, id := range activeIDs {
		if id != req.DeploymentID {
			return RunResult{}, deployment.ErrDeployQueued
		}
	}

	ctx, cancel := context.WithTimeout(ctx, w.cfg.BuildTimeout+5*time.Minute)
	defer cancel()

	return w.run(ctx, req)
}

func (w *Worker) run(ctx context.Context, req RunRequest) (RunResult, error) {
	result := RunResult{}

	// Step 1: transition queued -> building.
	if err := deployment.Transition(ctx, w.state, req.DeploymentID, deployment.StatusBuilding, "", ""); err != nil {
		return result, fmt.Errorf("transition to building: %w", err)
	}

	// Step 2: checkout + sandboxed build (internal/docker).
	imageRef, logs, buildErr := w.build(ctx, req)
	if buildErr != nil {
		slog.ErrorContext(ctx, "worker: build failed", "deployment", req.DeploymentID, "error", buildErr)
		// A failed build must not attempt a container swap (PRD §12).
		if terr := deployment.Transition(ctx, w.state, req.DeploymentID, deployment.StatusFailed, "", buildErr.Error()); terr != nil {
			slog.ErrorContext(ctx, "worker: failed to record build failure", "deployment", req.DeploymentID, "error", terr)
		}
		result.BuildSuccess = false
		result.BuildLogs = logs
		result.DeployError = fmt.Errorf("build: %w", buildErr)
		// Cleanup result is recorded separately from the build/deploy result.
		result.CleanupError = w.cleanupOnFailure(ctx, req, "")
		return result, result.DeployError
	}
	result.BuildSuccess = true
	result.BuildLogs = logs

	// Step 3: transition building -> deploying.
	if err := deployment.Transition(ctx, w.state, req.DeploymentID, deployment.StatusDeploying, imageRef, ""); err != nil {
		result.DeployError = fmt.Errorf("transition to deploying: %w", err)
		result.CleanupError = w.cleanupOnFailure(ctx, req, imageRef)
		return result, result.DeployError
	}

	// Step 4: start the new runtime container. A failed start must not
	// remove the previous container (PRD §7.3 ordering rule).
	containerID, startErr := w.startContainer(ctx, req, imageRef)
	if startErr != nil {
		slog.ErrorContext(ctx, "worker: container start failed", "deployment", req.DeploymentID, "error", startErr)
		if terr := deployment.Transition(ctx, w.state, req.DeploymentID, deployment.StatusFailed, imageRef, startErr.Error()); terr != nil {
			slog.ErrorContext(ctx, "worker: failed to record start failure", "deployment", req.DeploymentID, "error", terr)
		}
		result.DeploySuccess = false
		result.DeployError = fmt.Errorf("container start: %w", startErr)
		result.CleanupError = w.cleanupOnFailure(ctx, req, imageRef)
		return result, result.DeployError
	}
	result.ContainerID = containerID
	result.DeploySuccess = true

	// Step 5: confirm running and persist container.
	if err := deployment.SetContainerID(ctx, w.state, req.DeploymentID, containerID); err != nil {
		slog.WarnContext(ctx, "worker: failed to record container id", "deployment", req.DeploymentID, "error", err)
	}

	// Step 6: activate proxy routes. If Caddy is unreachable, we fail the
	// deployment cleanly, removing the new container to avoid orphans
	// (PRD §7.6, §12).
	if w.Router != nil {
		if err := w.Router.AddRoute(ctx, req.GeneratedHostname, req.ExposedPort); err != nil {
			routeErr := fmt.Errorf("proxy route for %s: %w", req.GeneratedHostname, err)
			slog.ErrorContext(ctx, "worker: route activation failed", "deployment", req.DeploymentID, "error", err)
			if terr := deployment.Transition(ctx, w.state, req.DeploymentID, deployment.StatusFailed, imageRef, routeErr.Error()); terr != nil {
				slog.ErrorContext(ctx, "worker: failed to record route failure", "deployment", req.DeploymentID, "error", terr)
			}
			result.DeploySuccess = false
			result.DeployError = routeErr
			result.CleanupError = w.cleanupOnFailure(ctx, req, imageRef)
			return result, result.DeployError
		}
		if err := domain.ActivateVerifiedRoutes(ctx, w.state, w.Router, req.ApplicationID, req.ExposedPort); err != nil {
			slog.WarnContext(ctx, "worker: custom domain routes failed", "deployment", req.DeploymentID, "error", err)
		}
	}

	if err := deployment.SetCurrentDeployment(ctx, w.state, req.ApplicationID, req.DeploymentID); err != nil {
		slog.WarnContext(ctx, "worker: failed to update current deployment", "deployment", req.DeploymentID, "error", err)
	}
	if err := deployment.Transition(ctx, w.state, req.DeploymentID, deployment.StatusRunning, imageRef, ""); err != nil {
		result.DeployError = err
		result.CleanupError = w.cleanupOnFailure(ctx, req, imageRef)
		return result, result.DeployError
	}
	if err := application.SetCurrentDeployedURL(ctx, w.state, req.ApplicationID, "https://"+req.GeneratedHostname); err != nil {
		slog.WarnContext(ctx, "worker: failed to record deployed url", "deployment", req.DeploymentID, "error", err)
	}

	// Step 7: stop the previous container only now that the new one is
	// confirmed running (PRD §7.3 ordering rule).
	if stopErr := w.stopPreviousContainer(ctx, req); stopErr != nil {
		slog.WarnContext(ctx, "worker: failed to stop previous container", "deployment", req.DeploymentID, "error", stopErr)
		if result.CleanupError == nil {
			result.CleanupError = stopErr
		} else {
			result.CleanupError = fmt.Errorf("%v; previous container stop: %v", result.CleanupError, stopErr)
		}
	}

	slog.InfoContext(ctx, "worker: deployment complete", "deployment", req.DeploymentID, "container", containerID)
	return result, nil
}

func (w *Worker) build(ctx context.Context, req RunRequest) (string, string, error) {
	if _, err := docker.ImageReference(req.Slug, req.DeploymentID); err != nil {
		return "", "", fmt.Errorf("generate image reference: %w", err)
	}

	buildReq := docker.BuildRequest{
		ApplicationSlug: req.Slug,
		DeploymentID:    req.DeploymentID,
		RepositoryURL:   req.RepositoryURL,
		Branch:          req.Branch,
		CommitSHA:       req.CommitSHA,
		DockerfilePath:  req.DockerfilePath,
		BuildContext:    req.BuildContext,
		Limits: docker.Limits{
			MemoryBytes: w.cfg.BuildMemLimit,
			CPUs:        w.cfg.BuildCPULimit,
			Timeout:     w.cfg.BuildTimeout,
		},
	}

	result, err := w.docker.Build(ctx, buildReq)
	if err != nil {
		return "", result.Logs, err
	}
	return result.ImageReference, result.Logs, nil
}

func (w *Worker) startContainer(ctx context.Context, req RunRequest, imageRef string) (string, error) {
	opts := docker.StartContainerOptions{
		ApplicationID:   req.ApplicationID,
		ApplicationSlug: req.Slug,
		DeploymentID:    req.DeploymentID,
		ImageReference:  imageRef,
		ExposedPort:     req.ExposedPort,
		HostPort:        req.ExposedPort,
		EnvVars:         req.EnvVars,
		Limits: docker.RuntimeLimits{
			MemoryBytes: w.cfg.RuntimeMemLimit,
			CPUs:        w.cfg.RuntimeCPULimit,
			PIDs:        docker.MaxPIDs(),
		},
	}
	return w.docker.StartContainer(ctx, opts)
}

func (w *Worker) stopPreviousContainer(ctx context.Context, req RunRequest) error {
	current, err := deployment.GetByApplicationStatus(ctx, w.state, req.ApplicationID, deployment.StatusRunning)
	if err != nil {
		if deployment.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get previous running deployment: %w", err)
	}
	if current == nil || current.ContainerID == nil || current.ID == req.DeploymentID {
		return nil
	}
	return w.docker.StopContainer(ctx, *current.ContainerID)
}

// cleanupOnFailure removes leftover images/containers from a failed attempt.
// Errors are returned to the caller so they can be recorded separately from
// the build/deploy failure (PRD §12).
func (w *Worker) cleanupOnFailure(ctx context.Context, req RunRequest, imageRef string) error {
	var errs []error

	if imageRef != "" {
		if err := w.docker.RemoveImage(ctx, imageRef); err != nil {
			errs = append(errs, fmt.Errorf("remove image %s: %w", imageRef, err))
		}
	}

	containerName := w.docker.ContainerName(req.Slug, req.DeploymentID)
	if err := w.docker.StopContainer(ctx, containerName); err != nil {
		errs = append(errs, fmt.Errorf("cleanup container %s: %w", containerName, err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}
	return nil
}

// RebuildRoutes queries the control plane database to rebuild all active
// proxy routes in the edge router at startup, reconciling proxy state with the
// source of truth (DR-003). It returns the reconciled routes count.
func (w *Worker) RebuildRoutes(ctx context.Context) (int, error) {
	if w.Router == nil {
		return 0, nil
	}

	apps, err := application.List(ctx, w.state)
	if err != nil {
		return 0, fmt.Errorf("list applications: %w", err)
	}

	var routes []proxy.Route
	for _, app := range apps {
		if app.CurrentDeploymentID == nil || *app.CurrentDeploymentID == "" {
			continue
		}
		current, err := deployment.Get(ctx, w.state, *app.CurrentDeploymentID)
		if err != nil {
			slog.WarnContext(ctx, "route rebuild: missing deployment pointer", "application", app.ID, "deployment", *app.CurrentDeploymentID, "error", err)
			continue
		}
		if current.Status != deployment.StatusRunning || current.ContainerID == nil || *current.ContainerID == "" {
			continue
		}

		// Re-apply the generated URL route.
		routes = append(routes, proxy.Route{Hostname: app.GeneratedHostname, TargetPort: app.ExposedPort})

		// Re-apply all verified custom domain routes on the application's exposed port.
		customs, err := domain.ListVerified(ctx, w.state, app.ID)
		if err != nil {
			slog.WarnContext(ctx, "route rebuild: failed to list custom domains", "application", app.ID, "error", err)
			continue
		}
		for _, custom := range customs {
			routes = append(routes, proxy.Route{Hostname: custom, TargetPort: app.ExposedPort})
		}
	}

	if err := w.Router.Sync(ctx, routes); err != nil {
		return 0, fmt.Errorf("sync caddy config: %w", err)
	}
	return len(routes), nil
}

// LoadEnvVars returns the application's environment variables as KEY=VALUE
// strings ordered by key. Secret values are returned but must never be logged.
func LoadEnvVars(ctx context.Context, state *db.DB, applicationID string) ([]string, error) {
	rows, err := state.QueryContext(ctx, `
		SELECT key, value FROM environment_variables
		WHERE application_id = ?
		ORDER BY key ASC
	`, applicationID)
	if err != nil {
		return nil, fmt.Errorf("load environment variables: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var vars []string
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan environment variable: %w", err)
		}
		vars = append(vars, key+"="+value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate environment variables: %w", err)
	}
	return vars, nil
}
