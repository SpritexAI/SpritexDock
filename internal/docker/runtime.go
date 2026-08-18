package docker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// RuntimeLimits holds resource limits applied to application containers.
type RuntimeLimits struct {
	MemoryBytes int64
	CPUs        float64
	PIDs        int64
}

// StartContainer creates and starts a container from an already-built image.
//
// Container naming uses the application slug and deployment ID so it can be
// inspected, stopped, and replaced idempotently. Secrets from the env vars
// slice are never logged or returned by errors. The caller is responsible for
// removing the container later.
func StartContainer(ctx context.Context, cli *client.Client, opts StartContainerOptions) (containerID string, err error) {
	if cli == nil {
		return "", fmt.Errorf("docker client must not be nil")
	}
	if err := validateStartOptions(opts); err != nil {
		return "", err
	}

	containerPort := nat.Port(strconv.Itoa(opts.ExposedPort) + "/tcp")

	containerConfig := &container.Config{
		Image: opts.ImageReference,
		ExposedPorts: nat.PortSet{
			containerPort: struct{}{},
		},
		Env: opts.EnvVars,
		Labels: map[string]string{
			"spritexdock.app-id":    opts.ApplicationID,
			"spritexdock.deploy-id": opts.DeploymentID,
			"spritexdock.slug":      opts.ApplicationSlug,
		},
	}
	hostConfig := &container.HostConfig{
		PortBindings: nat.PortMap{
			containerPort: []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: strconv.Itoa(opts.HostPort)},
			},
		},
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyUnlessStopped,
		},
		SecurityOpt: []string{"no-new-privileges"},
		Resources: container.Resources{
			Memory:   opts.Limits.MemoryBytes,
			NanoCPUs: runtimeNanoCPUs(opts.Limits.CPUs),
		},
	}

	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, ContainerName(opts.ApplicationSlug, opts.DeploymentID))
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	if resp.ID == "" {
		return "", fmt.Errorf("create container returned empty id")
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup: remove the failed container so it does not
		// linger while preserving the outer error.
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("start container: %w", err)
	}
	return resp.ID, nil
}

// StartContainerOptions captures the required inputs for StartContainer.
type StartContainerOptions struct {
	ApplicationID   string
	ApplicationSlug string
	DeploymentID    string
	ImageReference  string
	ExposedPort     int
	HostPort        int
	EnvVars         []string
	Limits          RuntimeLimits
}

func validateStartOptions(opts StartContainerOptions) error {
	if opts.ApplicationID == "" || opts.ApplicationSlug == "" || opts.DeploymentID == "" {
		return fmt.Errorf("application and deployment identifiers are required")
	}
	if opts.ImageReference == "" {
		return fmt.Errorf("image reference is required")
	}
	if opts.ExposedPort < 1 || opts.ExposedPort > 65535 {
		return fmt.Errorf("exposed port must be between 1 and 65535")
	}
	if opts.HostPort < 1 || opts.HostPort > 65535 {
		return fmt.Errorf("host port must be between 1 and 65535")
	}
	if opts.Limits.MemoryBytes <= 0 {
		return fmt.Errorf("runtime memory limit must be positive")
	}
	if opts.Limits.CPUs <= 0 {
		return fmt.Errorf("runtime cpu limit must be positive")
	}
	if opts.Limits.PIDs < 1 {
		return fmt.Errorf("runtime pid limit must be positive")
	}
	return nil
}

// ContainerName returns the deterministic container name for a deployment.
func ContainerName(slug, deploymentID string) string {
	return "spritexdock-" + slug + "-" + deploymentID
}

// InspectContainer returns the durable state information for a container,
// including running status and exit code.
func InspectContainer(ctx context.Context, cli *client.Client, containerID string) (state string, running bool, exitCode int, err error) {
	if cli == nil {
		return "", false, 0, fmt.Errorf("docker client must not be nil")
	}
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", false, 0, fmt.Errorf("inspect container: %w", err)
	}
	return info.State.Status, info.State.Running, info.State.ExitCode, nil
}

// StopContainer stops and removes a container. It returns the removal error
// separately so callers can record it without masking a prior stop error.
func StopContainer(ctx context.Context, cli *client.Client, containerID string) error {
	if cli == nil {
		return fmt.Errorf("docker client must not be nil")
	}
	stopCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := cli.ContainerStop(stopCtx, containerID, container.StopOptions{Timeout: nil}); err != nil && !client.IsErrNotFound(err) {
		return fmt.Errorf("stop container: %w", err)
	}
	if err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	return nil
}

func runtimeNanoCPUs(cpus float64) int64 {
	nano := int64(cpus * 1e9)
	if nano < 1 {
		return 1
	}
	return nano
}
