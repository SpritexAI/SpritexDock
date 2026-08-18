package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/client"
)

// NewClient creates a Docker client for the explicitly configured endpoint.
func NewClient(endpoint string) (*client.Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("docker endpoint must not be empty")
	}
	cli, err := client.NewClientWithOpts(
		client.WithHost(endpoint),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return cli, nil
}

// Ping verifies that the configured Docker Engine endpoint is reachable.
func Ping(ctx context.Context, cli *client.Client) error {
	if cli == nil {
		return fmt.Errorf("docker client must not be nil")
	}
	if _, err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("ping docker engine: %w", err)
	}
	return nil
}
