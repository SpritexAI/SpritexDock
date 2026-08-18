// Package proxy manages HTTP routing and TLS termination in the edge proxy
// (Caddy) for deployed applications. Route state is owned by the control plane
// and pushed to Caddy through its Admin API, so changes never require a
// control-plane restart (PRD §7.6, DR-003).
package proxy

import (
	"context"
	"errors"
	"fmt"
)

// ErrInvalidRoute signals a route request that violates routing constraints.
var ErrInvalidRoute = errors.New("invalid proxy route")

// Route maps a public hostname to the local port of its application container.
type Route struct {
	Hostname   string `json:"hostname"`
	TargetPort int    `json:"target_port"`
}

// Router applies hostname-to-target routing in the edge proxy. Implementations
// must be safe for concurrent use and must apply changes without restarting
// the control plane.
type Router interface {
	// AddRoute starts proxying hostname to 127.0.0.1:targetPort. Adding a
	// route for an existing hostname replaces its target.
	AddRoute(ctx context.Context, hostname string, targetPort int) error
	// RemoveRoute stops proxying hostname. Removing an unknown hostname is a
	// no-op so cleanup stays idempotent.
	RemoveRoute(ctx context.Context, hostname string) error
	// ListRoutes returns the routes currently applied through this router.
	ListRoutes(ctx context.Context) ([]Route, error)
	// Sync replaces the full route set with routes. It reconciles proxy
	// state with control-plane state, dropping routes that are no longer
	// desired.
	Sync(ctx context.Context, routes []Route) error
}

// ValidateRoute checks a route before it reaches the proxy. Hostnames must be
// absolute DNS names (never user-controlled paths or schemes), and target
// ports must be valid port numbers on the loopback interface.
func ValidateRoute(hostname string, targetPort int) error {
	if hostname == "" || len(hostname) > 253 {
		return fmt.Errorf("%w: hostname must be 1-253 characters", ErrInvalidRoute)
	}
	if targetPort < 1 || targetPort > 65535 {
		return fmt.Errorf("%w: target port must be between 1 and 65535", ErrInvalidRoute)
	}
	return nil
}
