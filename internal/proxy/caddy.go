package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// caddyHTTPListen covers the standard edge ports Caddy binds for HTTP and
	// HTTPS traffic.
	caddyHTTPListen = ":80"
	caddyTLSListen  = ":443"
	// caddyServerName is the single server block SpritexDock owns in Caddy.
	caddyServerName = "spritexdock"
)

// CaddyRouter manages routes in a managed Caddy process (DR-003) by pushing a
// complete JSON config to the Caddy Admin API. Caddy keeps no durable copy of
// API-pushed config, so the router tracks the desired route set in memory and
// the control plane re-pushes it (Sync) at startup and on deployment events.
type CaddyRouter struct {
	adminURL string
	client   *http.Client

	mu     sync.Mutex
	routes map[string]int
}

// NewCaddyRouter creates a router for the Caddy admin endpoint. adminAddr is a
// host:port pair such as 127.0.0.1:2019; a full URL is also accepted. The
// endpoint must be local/private (PRD §11.8).
func NewCaddyRouter(adminAddr string) *CaddyRouter {
	adminAddr = strings.TrimSpace(adminAddr)
	if adminAddr == "" {
		adminAddr = "127.0.0.1:2019"
	}
	url := adminAddr
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	return &CaddyRouter{
		adminURL: strings.TrimRight(url, "/"),
		client:   &http.Client{Timeout: 10 * time.Second},
		routes:   make(map[string]int),
	}
}

// AddRoute applies the route by replacing the config known to this router.
func (r *CaddyRouter) AddRoute(ctx context.Context, hostname string, targetPort int) error {
	hostname = normalizeHostname(hostname)
	if err := ValidateRoute(hostname, targetPort); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[hostname] = targetPort
	if err := r.load(ctx); err != nil {
		delete(r.routes, hostname)
		return err
	}
	return nil
}

// RemoveRoute deletes the route for hostname. Removing an unknown hostname is
// a no-op and does not trigger a config load.
func (r *CaddyRouter) RemoveRoute(ctx context.Context, hostname string) error {
	hostname = normalizeHostname(hostname)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.routes[hostname]; !ok {
		return nil
	}
	port, had := r.routes[hostname]
	delete(r.routes, hostname)
	if err := r.load(ctx); err != nil {
		if had {
			r.routes[hostname] = port
		}
		return err
	}
	return nil
}

// ListRoutes returns the applied routes ordered by hostname.
func (r *CaddyRouter) ListRoutes(_ context.Context) ([]Route, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sortedRoutes(r.routes), nil
}

// Sync replaces the full route set, dropping any route not present in routes.
func (r *CaddyRouter) Sync(ctx context.Context, routes []Route) error {
	desired := make(map[string]int, len(routes))
	for _, route := range routes {
		hostname := normalizeHostname(route.Hostname)
		if err := ValidateRoute(hostname, route.TargetPort); err != nil {
			return err
		}
		desired[hostname] = route.TargetPort
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.routes
	r.routes = desired
	if err := r.load(ctx); err != nil {
		r.routes = previous
		return err
	}
	return nil
}

// load pushes the complete Caddy config for the current route set to the
// admin API. Callers must hold r.mu.
func (r *CaddyRouter) load(ctx context.Context) error {
	config := caddyConfigFor(r.adminURL, r.routes)
	body, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode caddy config: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.adminURL+"/load", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build caddy load request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin unreachable at %s: %w", r.adminURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	non2xx := resp.StatusCode < 200 || resp.StatusCode > 299
	if !non2xx {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("caddy load rejected (status %d): %s", resp.StatusCode, strings.TrimSpace(string(detail)))
}

func caddyConfigFor(adminURL string, routes map[string]int) caddyConfig {
	config := caddyConfig{
		Admin: caddyAdmin{Listen: adminListenFromURL(adminURL)},
		Apps: caddyApps{HTTP: caddyHTTP{
			Servers: map[string]caddyServer{
				caddyServerName: {
					Listen: []string{caddyHTTPListen, caddyTLSListen},
					Routes: routeMatchers(routes),
				},
			},
		}},
	}
	return config
}

func routeMatchers(routes map[string]int) []caddyRoute {
	ordered := sortedRoutes(routes)
	matchers := make([]caddyRoute, 0, len(ordered))
	for _, route := range ordered {
		matchers = append(matchers, caddyRoute{
			Match: []caddyMatch{{Host: []string{route.Hostname}}},
			Handle: []caddyHandle{{
				Handler:   "reverse_proxy",
				Upstreams: []caddyUpstream{{Dial: fmt.Sprintf("127.0.0.1:%d", route.TargetPort)}},
			}},
		})
	}
	return matchers
}

func sortedRoutes(routes map[string]int) []Route {
	result := make([]Route, 0, len(routes))
	for hostname, port := range routes {
		result = append(result, Route{Hostname: hostname, TargetPort: port})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Hostname < result[j].Hostname })
	return result
}

// adminListenFromURL converts the admin base URL back into the host:port form
// Caddy's admin.listen field expects, so a config push never changes or loses
// the admin endpoint itself.
func adminListenFromURL(adminURL string) string {
	addr := strings.TrimPrefix(adminURL, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimRight(addr, "/")
	if addr == "" {
		return "127.0.0.1:2019"
	}
	return addr
}

func normalizeHostname(hostname string) string {
	return strings.ToLower(strings.TrimSpace(hostname))
}

// caddyConfig and its nested types model the subset of the Caddy JSON config
// schema SpritexDock manages: an admin listener plus one HTTP server whose
// routes reverse-proxy matched hosts to loopback upstreams. Automatic HTTPS
// stays enabled by Caddy's defaults, which provisions certificates for every
// routed hostname.
type caddyConfig struct {
	Admin caddyAdmin `json:"admin"`
	Apps  caddyApps  `json:"apps"`
}

type caddyAdmin struct {
	Listen string `json:"listen"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
}

type caddyHTTP struct {
	Servers map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen []string     `json:"listen"`
	Routes []caddyRoute `json:"routes,omitempty"`
}

type caddyRoute struct {
	Match  []caddyMatch  `json:"match,omitempty"`
	Handle []caddyHandle `json:"handle"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

type caddyHandle struct {
	Handler   string          `json:"handler"`
	Upstreams []caddyUpstream `json:"upstreams"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}
