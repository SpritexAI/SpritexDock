package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeCaddyAdmin records pushed configs like the Caddy Admin API would.
type fakeCaddyAdmin struct {
	server    *httptest.Server
	loads     []caddyConfig
	failLoads bool
}

func newFakeCaddyAdmin(t *testing.T) *fakeCaddyAdmin {
	t.Helper()
	fake := &fakeCaddyAdmin{}
	mux := http.NewServeMux()
	mux.HandleFunc("/load", func(w http.ResponseWriter, r *http.Request) {
		if fake.failLoads {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("simulated caddy failure"))
			return
		}
		var config caddyConfig
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fake.loads = append(fake.loads, config)
		w.WriteHeader(http.StatusOK)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeCaddyAdmin) lastLoad() caddyConfig {
	if len(f.loads) == 0 {
		return caddyConfig{}
	}
	return f.loads[len(f.loads)-1]
}

func (f *fakeCaddyAdmin) routeHosts() []string {
	server := f.lastLoad().Apps.HTTP.Servers[caddyServerName]
	hosts := make([]string, 0, len(server.Routes))
	for _, route := range server.Routes {
		hosts = append(hosts, route.Match[0].Host[0])
	}
	return hosts
}

func TestCaddyRouterAddRoutePushesConfig(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	router := NewCaddyRouter(fake.server.URL)
	ctx := context.Background()

	if err := router.AddRoute(ctx, "myapp.203-0-113-10.sslip.io", 8080); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if err := router.AddRoute(ctx, "www.example.com", 9090); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}

	load := fake.lastLoad()
	if load.Admin.Listen == "" {
		t.Error("pushed config must preserve the admin listener")
	}
	if got, want := load.Apps.HTTP.Servers[caddyServerName].Listen, []string{":80", ":443"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("server listen = %v, want %v", got, want)
	}
	if hosts := fake.routeHosts(); len(hosts) != 2 || hosts[0] != "myapp.203-0-113-10.sslip.io" || hosts[1] != "www.example.com" {
		t.Errorf("route hosts = %v, want sorted [myapp.203-0-113-10.sslip.io www.example.com]", hosts)
	}

	server := load.Apps.HTTP.Servers[caddyServerName]
	if server.Routes[0].Handle[0].Handler != "reverse_proxy" {
		t.Errorf("handler = %q, want reverse_proxy", server.Routes[0].Handle[0].Handler)
	}
	if dial := server.Routes[0].Handle[0].Upstreams[0].Dial; dial != "127.0.0.1:8080" {
		t.Errorf("upstream dial = %q, want 127.0.0.1:8080", dial)
	}
}

func TestCaddyRouterAddRouteReplacesTarget(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	router := NewCaddyRouter(fake.server.URL)
	ctx := context.Background()

	if err := router.AddRoute(ctx, "myapp.203-0-113-10.sslip.io", 8080); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if err := router.AddRoute(ctx, "myapp.203-0-113-10.sslip.io", 9090); err != nil {
		t.Fatalf("AddRoute replace: %v", err)
	}

	routes, err := router.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(routes) != 1 || routes[0].TargetPort != 9090 {
		t.Fatalf("routes = %v, want single route on port 9090", routes)
	}
}

func TestCaddyRouterAddRouteRejectsInvalidInput(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	router := NewCaddyRouter(fake.server.URL)
	ctx := context.Background()

	if err := router.AddRoute(ctx, "", 8080); err == nil {
		t.Error("empty hostname must be rejected")
	}
	if err := router.AddRoute(ctx, "ok.example.com", 0); err == nil {
		t.Error("port 0 must be rejected")
	}
	if loads := len(fake.loads); loads != 0 {
		t.Errorf("loads = %d, want 0 (no push on invalid input)", loads)
	}
}

func TestCaddyRouterLoadFailureKeepsPreviousState(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	router := NewCaddyRouter(fake.server.URL)
	ctx := context.Background()

	if err := router.AddRoute(ctx, "myapp.203-0-113-10.sslip.io", 8080); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}

	fake.failLoads = true
	if err := router.AddRoute(ctx, "www.example.com", 9090); err == nil {
		t.Fatal("AddRoute must fail when Caddy rejects the config")
	}
	fake.failLoads = false

	routes, err := router.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(routes) != 1 || routes[0].Hostname != "myapp.203-0-113-10.sslip.io" {
		t.Fatalf("routes = %v, want only the pre-failure route", routes)
	}
}

func TestCaddyRouterRemoveRoute(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	router := NewCaddyRouter(fake.server.URL)
	ctx := context.Background()

	if err := router.AddRoute(ctx, "myapp.203-0-113-10.sslip.io", 8080); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	loadsBefore := len(fake.loads)

	// Removing an unknown hostname is a no-op without a config push.
	if err := router.RemoveRoute(ctx, "unknown.example.com"); err != nil {
		t.Fatalf("RemoveRoute unknown: %v", err)
	}
	if loadsAfter := len(fake.loads); loadsAfter != loadsBefore {
		t.Fatalf("loads = %d, want %d (no push for unknown hostname)", loadsAfter, loadsBefore)
	}

	if err := router.RemoveRoute(ctx, "myapp.203-0-113-10.sslip.io"); err != nil {
		t.Fatalf("RemoveRoute: %v", err)
	}
	if hosts := fake.routeHosts(); len(hosts) != 0 {
		t.Errorf("route hosts = %v, want empty after removal", hosts)
	}

	routes, err := router.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %v, want empty", routes)
	}
}

func TestCaddyRouterSyncReplacesRouteSet(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	router := NewCaddyRouter(fake.server.URL)
	ctx := context.Background()

	if err := router.AddRoute(ctx, "stale.example.com", 8080); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if err := router.Sync(ctx, []Route{
		{Hostname: "myapp.203-0-113-10.sslip.io", TargetPort: 8080},
		{Hostname: "www.example.com", TargetPort: 8080},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if hosts := fake.routeHosts(); len(hosts) != 2 || hosts[0] != "myapp.203-0-113-10.sslip.io" || hosts[1] != "www.example.com" {
		t.Errorf("route hosts = %v, want synced set without stale.example.com", hosts)
	}
}

func TestCaddyRouterUnreachableAdmin(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	fake.server.Close() // simulate Caddy being down
	router := NewCaddyRouter(fake.server.URL)

	err := router.AddRoute(context.Background(), "myapp.203-0-113-10.sslip.io", 8080)
	if err == nil {
		t.Fatal("AddRoute must fail when the admin endpoint is unreachable")
	}
	routes, listErr := router.ListRoutes(context.Background())
	if listErr != nil {
		t.Fatalf("ListRoutes: %v", listErr)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %v, want empty after failed add", routes)
	}
}

func TestCaddyRouterNormalizesHostname(t *testing.T) {
	fake := newFakeCaddyAdmin(t)
	router := NewCaddyRouter(fake.server.URL)
	ctx := context.Background()

	if err := router.AddRoute(ctx, "  MYAPP.203-0-113-10.SSLIP.IO ", 8080); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if hosts := fake.routeHosts(); len(hosts) != 1 || hosts[0] != "myapp.203-0-113-10.sslip.io" {
		t.Errorf("route hosts = %v, want normalized lowercase hostname", hosts)
	}
}
