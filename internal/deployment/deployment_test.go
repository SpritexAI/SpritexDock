package deployment

import (
	"context"
	"strings"
	"testing"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

func TestDeploymentLifecycleAndRecovery(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	appID := "app-1"
	_, err = state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appID, "App", "app", "https://example.com/repo", "main", "Dockerfile", ".", 8080, "app.127-0-0-1.sslip.io", "created", "now", "now")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	item, err := CreateQueued(ctx, state, appID, strings.Repeat("a", 40), "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := Transition(ctx, state, item.ID, StatusBuilding, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Transition(ctx, state, item.ID, StatusDeploying, "spritexdock/app:one", ""); err != nil {
		t.Fatal(err)
	}
	if err := Transition(ctx, state, item.ID, StatusRunning, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Transition(ctx, state, item.ID, StatusFailed, "", "too late"); err == nil {
		t.Fatal("terminal deployment transitioned")
	}

	interrupted, err := CreateQueued(ctx, state, appID, strings.Repeat("b", 40), "startup")
	if err != nil {
		t.Fatal(err)
	}
	if err := Transition(ctx, state, interrupted.ID, StatusBuilding, "", ""); err != nil {
		t.Fatal(err)
	}
	count, err := RecoverInterrupted(ctx, state)
	if err != nil || count != 1 {
		t.Fatalf("RecoverInterrupted() = %d, %v", count, err)
	}
	got, err := Get(ctx, state, interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed || got.FailureReason == nil || !strings.Contains(*got.FailureReason, "restart") {
		t.Fatalf("recovered deployment = %+v", got)
	}
}

func TestTransitionRejectsMissingImage(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	_, err = state.Exec(`INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES ('app-2', 'App', 'app-two', 'https://example.com/repo', 'main', 'Dockerfile', '.', 8081, 'app-two.127-0-0-1.sslip.io', 'created', 'now', 'now')`)
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateQueued(context.Background(), state, "app-2", strings.Repeat("c", 40), "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := Transition(context.Background(), state, item.ID, StatusBuilding, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Transition(context.Background(), state, item.ID, StatusDeploying, "", ""); err == nil {
		t.Fatal("deploying accepted an empty image")
	}
}
