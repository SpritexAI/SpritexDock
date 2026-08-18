package domain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/proxy"
)

type fakeResolver struct {
	addresses []string
	err       error
}

func (f fakeResolver) LookupHost(_ context.Context, _ string) ([]string, error) {
	return f.addresses, f.err
}

type recordingRouter struct {
	added   map[string]int
	removed map[string]bool
	err     error
}

func newRecordingRouter() *recordingRouter {
	return &recordingRouter{added: make(map[string]int), removed: make(map[string]bool)}
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

func insertTestApp(t *testing.T, state *db.DB, id, hostname string) {
	t.Helper()
	_, err := state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "App", id, "https://github.com/example/app.git", "main", "Dockerfile", ".", 8080, hostname, "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeHostname(t *testing.T) {
	valid := map[string]string{
		"www.example.com":           "www.example.com",
		"  APP.Example.COM ":        "app.example.com",
		"trailing-dot.example.com.": "trailing-dot.example.com",
		"a-b.example.co.uk":         "a-b.example.co.uk",
		"my-app.example.io":         "my-app.example.io",
		"x-y-z.a.b.example.net":     "x-y-z.a.b.example.net",
	}
	for raw, want := range valid {
		got, err := NormalizeHostname(raw)
		if err != nil {
			t.Errorf("NormalizeHostname(%q) error: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeHostname(%q) = %q, want %q", raw, got, want)
		}
	}

	invalid := []string{
		"",
		"localhost",
		"no-dots",
		"192.168.1.1",
		"myapp.203-0-113-10.sslip.io",
		"sslip.io",
		"-leading.example.com",
		"trailing-.example.com",
		"under_score.example.com",
		"sp ace.example.com",
		"a..b.example.com",
		toolongLabel(),
		strings.Repeat("a", 250) + ".example.com",
	}
	for _, raw := range invalid {
		if _, err := NormalizeHostname(raw); err == nil {
			t.Errorf("NormalizeHostname(%q) accepted invalid input", raw)
		}
	}
}

func toolongLabel() string {
	return strings.Repeat("a", 64) + ".example.com"
}

func TestDomainCRUD(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()
	insertTestApp(t, state, "app-1", "app1.203-0-113-10.sslip.io")

	item, err := Add(ctx, state, "app-1", "WWW.Example.COM")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if item.Hostname != "www.example.com" || item.VerificationStatus != StatusPending || item.Type != "custom" {
		t.Fatalf("added domain = %+v", item)
	}

	if _, err := Add(ctx, state, "app-1", "www.example.com"); !errors.Is(err, ErrDuplicateDomain) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateDomain", err)
	}
	if _, err := Add(ctx, state, "missing-app", "other.example.com"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("missing app error = %v, want application not found", err)
	}

	items, err := List(ctx, state, "app-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Hostname != "www.example.com" {
		t.Fatalf("list = %+v", items)
	}

	fetched, err := Get(ctx, state, "app-1", "www.example.com")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.ID != item.ID {
		t.Fatalf("get id = %s, want %s", fetched.ID, item.ID)
	}
	if _, err := Get(ctx, state, "app-1", "unknown.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing domain error = %v, want ErrNotFound", err)
	}

	verified, err := ListVerified(ctx, state, "app-1")
	if err != nil {
		t.Fatalf("ListVerified: %v", err)
	}
	if len(verified) != 0 {
		t.Fatalf("verified = %v, want empty before verification", verified)
	}

	if err := Delete(ctx, state, "app-1", "www.example.com"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := Delete(ctx, state, "app-1", "www.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}
}

func TestVerifyDomainSuccessActivatesRouteWhenRunning(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()

	insertTestApp(t, state, "app-1", "app1.203-0-113-10.sslip.io")
	deploy, err := deployment.CreateQueued(ctx, state, "app-1", "abcdef1234567890abcdef1234567890abcdef12", "manual")
	if err != nil {
		t.Fatal(err)
	}
	for _, transition := range []struct {
		status string
		image  string
	}{
		{deployment.StatusBuilding, ""},
		{deployment.StatusDeploying, "spritexdock/app1:tag"},
		{deployment.StatusRunning, "spritexdock/app1:tag"},
	} {
		if err := deployment.Transition(ctx, state, deploy.ID, transition.status, transition.image, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := deployment.SetContainerID(ctx, state, deploy.ID, "container-1"); err != nil {
		t.Fatal(err)
	}
	if err := deployment.SetCurrentDeployment(ctx, state, "app-1", deploy.ID); err != nil {
		t.Fatal(err)
	}

	item, err := Add(ctx, state, "app-1", "www.example.com")
	if err != nil {
		t.Fatal(err)
	}

	router := newRecordingRouter()
	service := &Service{State: state, PublicIP: "203.0.113.10", Router: router, Resolver: fakeResolver{addresses: []string{"198.51.100.7", "203.0.113.10"}}}

	verified, err := service.VerifyDomain(ctx, "app-1", "www.example.com")
	if err != nil {
		t.Fatalf("VerifyDomain: %v", err)
	}
	if verified.VerificationStatus != StatusVerified || verified.VerifiedAt == nil {
		t.Fatalf("verified domain = %+v", verified)
	}
	if port, ok := router.added["www.example.com"]; !ok || port != 8080 {
		t.Fatalf("router added = %v, want www.example.com -> 8080", router.added)
	}
	if item.VerificationStatus != StatusPending {
		t.Fatalf("Add-returned record was mutated: %+v", item)
	}

	// Verified hostnames are returned by ListVerified for route rebuilds.
	hostnames, err := ListVerified(ctx, state, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hostnames) != 1 || hostnames[0] != "www.example.com" {
		t.Fatalf("verified hostnames = %v", hostnames)
	}
}

func TestVerifyDomainWrongAddressRecordsFailure(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()
	insertTestApp(t, state, "app-1", "app1.203-0-113-10.sslip.io")
	if _, err := Add(ctx, state, "app-1", "www.example.com"); err != nil {
		t.Fatal(err)
	}

	router := newRecordingRouter()
	service := &Service{State: state, PublicIP: "203.0.113.10", Router: router, Resolver: fakeResolver{addresses: []string{"198.51.100.7"}}}

	failed, err := service.VerifyDomain(ctx, "app-1", "www.example.com")
	if err == nil {
		t.Fatal("expected verification error for wrong address")
	}
	if failed.VerificationStatus != StatusFailed || failed.LastVerificationError == nil {
		t.Fatalf("failed domain = %+v", failed)
	}
	if !strings.Contains(*failed.LastVerificationError, "203.0.113.10") {
		t.Errorf("error = %q, want expected IP mentioned", *failed.LastVerificationError)
	}
	if len(router.added) != 0 {
		t.Errorf("router added = %v, want no routes on failure", router.added)
	}

	stored, listErr := List(ctx, state, "app-1")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if stored[0].VerificationStatus != StatusFailed {
		t.Errorf("stored status = %q, want failed", stored[0].VerificationStatus)
	}
}

func TestVerifyDomainLookupError(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()
	insertTestApp(t, state, "app-1", "app1.203-0-113-10.sslip.io")
	if _, err := Add(ctx, state, "app-1", "www.example.com"); err != nil {
		t.Fatal(err)
	}

	service := &Service{State: state, PublicIP: "203.0.113.10", Resolver: fakeResolver{err: errors.New("no such host")}}
	failed, err := service.VerifyDomain(ctx, "app-1", "www.example.com")
	if err == nil {
		t.Fatal("expected verification error for lookup failure")
	}
	if failed.VerificationStatus != StatusFailed || !strings.Contains(*failed.LastVerificationError, "no such host") {
		t.Fatalf("failed domain = %+v", failed)
	}
}

func TestVerifyDomainRequiresPublicIP(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()
	insertTestApp(t, state, "app-1", "app1.203-0-113-10.sslip.io")
	if _, err := Add(ctx, state, "app-1", "www.example.com"); err != nil {
		t.Fatal(err)
	}

	service := &Service{State: state, Resolver: fakeResolver{addresses: []string{"203.0.113.10"}}}
	failed, err := service.VerifyDomain(ctx, "app-1", "www.example.com")
	if err == nil {
		t.Fatal("expected verification error without a public IP")
	}
	if !strings.Contains(*failed.LastVerificationError, "public IP") {
		t.Errorf("error = %q, want public IP message", *failed.LastVerificationError)
	}
}

func TestActivateVerifiedRoutes(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()
	insertTestApp(t, state, "app-1", "app1.203-0-113-10.sslip.io")

	now := "2026-08-18T00:00:00Z"
	for _, hostname := range []string{"verified.example.com", "pending.example.com", "failed.example.com"} {
		status := StatusPending
		if hostname == "failed.example.com" {
			status = StatusFailed
		}
		if _, err := state.Exec(`INSERT INTO domains
			(id, application_id, hostname, type, verification_status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"dom-"+hostname, "app-1", hostname, "custom", status, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.Exec(`UPDATE domains SET verification_status = ?, verified_at = ? WHERE hostname = ?`,
		StatusVerified, now, "verified.example.com"); err != nil {
		t.Fatal(err)
	}

	router := newRecordingRouter()
	if err := ActivateVerifiedRoutes(ctx, state, router, "app-1", 8080); err != nil {
		t.Fatalf("ActivateVerifiedRoutes: %v", err)
	}
	if len(router.added) != 1 || router.added["verified.example.com"] != 8080 {
		t.Fatalf("router added = %v, want only verified.example.com -> 8080", router.added)
	}

	failing := &recordingRouter{err: errors.New("caddy down")}
	if err := ActivateVerifiedRoutes(ctx, state, failing, "app-1", 8080); err == nil {
		t.Fatal("expected route activation error to surface")
	}
}
