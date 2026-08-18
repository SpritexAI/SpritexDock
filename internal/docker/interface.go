package docker

import (
	"context"

	"github.com/docker/docker/client"
)

// ContainerInfo is a decoupled view of runtime container state.
type ContainerInfo struct {
	ID       string
	State    string
	Running  bool
	ExitCode int
}

// Operation is the minimal interface for docker operations used by the worker.
// It exists to enable testing without a live Docker daemon.
type Operation interface {
	Build(ctx context.Context, req BuildRequest) (BuildResult, error)
	StartContainer(ctx context.Context, opts StartContainerOptions) (string, error)
	StopContainer(ctx context.Context, containerID string) error
	RemoveImage(ctx context.Context, reference string) error
	ContainerName(slug, deploymentID string) string
	InspectContainer(ctx context.Context, containerID string) (ContainerInfo, error)
}

// ClientAdapter wraps a concrete *client.Client for testing.
type ClientAdapter struct {
	Real *client.Client
}

func (a *ClientAdapter) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	return Build(ctx, a.Real, req)
}

func (a *ClientAdapter) StartContainer(ctx context.Context, opts StartContainerOptions) (string, error) {
	return StartContainer(ctx, a.Real, opts)
}

func (a *ClientAdapter) StopContainer(ctx context.Context, containerID string) error {
	return StopContainer(ctx, a.Real, containerID)
}

func (a *ClientAdapter) RemoveImage(ctx context.Context, reference string) error {
	return RemoveImage(ctx, a.Real, reference)
}

func (a *ClientAdapter) ContainerName(slug, deploymentID string) string {
	return ContainerName(slug, deploymentID)
}

func (a *ClientAdapter) InspectContainer(ctx context.Context, containerID string) (ContainerInfo, error) {
	state, running, exitCode, err := InspectContainer(ctx, a.Real, containerID)
	if err != nil {
		return ContainerInfo{}, err
	}
	return ContainerInfo{ID: containerID, State: state, Running: running, ExitCode: exitCode}, nil
}
