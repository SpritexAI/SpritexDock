package application

import (
	"context"
	"errors"
	"testing"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

func TestInputNormalizeAndValidate(t *testing.T) {
	input := Input{
		Name:          " Example App ",
		Slug:          "Example-App",
		RepositoryURL: " https://github.com/example/app.git ",
		ExposedPort:   8080,
	}
	input.Normalize()
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if input.Name != "Example App" || input.Slug != "example-app" || input.Branch != defaultBranch || input.DockerfilePath != defaultDockerfile || input.BuildContext != defaultBuildContext {
		t.Fatalf("normalized input = %+v", input)
	}
}

func TestInvalidInputs(t *testing.T) {
	valid := Input{Name: "App", Slug: "app", RepositoryURL: "https://github.com/example/app.git", Branch: "main", DockerfilePath: "Dockerfile", BuildContext: ".", ExposedPort: 8080}
	tests := map[string]Input{
		"slug":       validWith(valid, func(in *Input) { in.Slug = "Bad_slug" }),
		"repository": validWith(valid, func(in *Input) { in.RepositoryURL = "https://user:pass@example.com/app.git" }),
		"branch":     validWith(valid, func(in *Input) { in.Branch = "../main" }),
		"dockerfile": validWith(valid, func(in *Input) { in.DockerfilePath = "../Dockerfile" }),
		"context":    validWith(valid, func(in *Input) { in.BuildContext = "/tmp" }),
		"port":       validWith(valid, func(in *Input) { in.ExposedPort = 65536 }),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if err := input.Validate(); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func validWith(input Input, mutate func(*Input)) Input {
	mutate(&input)
	return input
}

func TestGenerateHostname(t *testing.T) {
	if got := GenerateHostname("my-app", "203.0.113.10"); got != "my-app.203-0-113-10.sslip.io" {
		t.Fatalf("hostname = %q", got)
	}
	if got := GenerateHostname("my-app", ""); got != "my-app.127-0-0-1.sslip.io" {
		t.Fatalf("empty-IP hostname = %q", got)
	}
}

func TestCRUD(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()

	created, err := Create(ctx, state, Input{Name: "My App", Slug: "my-app", RepositoryURL: "https://github.com/example/app.git", ExposedPort: 8080}, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if created.Branch != "main" || created.DockerfilePath != "Dockerfile" || created.BuildContext != "." || created.GeneratedHostname != "my-app.203-0-113-10.sslip.io" {
		t.Fatalf("created application defaults = %+v", created)
	}

	got, err := Get(ctx, state, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.Name != "My App" {
		t.Fatalf("got = %+v", got)
	}

	items, err := List(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("list = %+v", items)
	}

	updated, err := Update(ctx, state, created.ID, Input{Name: "Updated", Slug: "updated", RepositoryURL: "https://git.example.com/repo", Branch: "release/v1", ExposedPort: 9090}, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Updated" || updated.GeneratedHostname != "updated.203-0-113-10.sslip.io" || updated.ExposedPort != 9090 {
		t.Fatalf("updated = %+v", updated)
	}

	if err := Delete(ctx, state, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(ctx, state, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete error = %v", err)
	}
	if err := Delete(ctx, state, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
}

func TestDuplicateSlug(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	input := Input{Name: "App", Slug: "app", RepositoryURL: "https://github.com/example/app.git", ExposedPort: 8080}
	if _, err := Create(context.Background(), state, input, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), state, input, "203.0.113.10"); !errors.Is(err, ErrDuplicateSlug) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestMissingUpdate(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	_, err = Update(context.Background(), state, "missing", Input{Name: "App", Slug: "app", RepositoryURL: "https://github.com/example/app.git", ExposedPort: 8080}, "203.0.113.10")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update error = %v", err)
	}
}

func TestGetAccessibleURLs(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()

	created, err := Create(ctx, state, Input{Name: "My App", Slug: "my-app", RepositoryURL: "https://github.com/example/app.git", ExposedPort: 8080}, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}

	urls, err := created.GetAccessibleURLs(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != "https://my-app.203-0-113-10.sslip.io" {
		t.Fatalf("urls without domains = %v", urls)
	}

	now := "2026-08-18T00:00:00Z"
	domains := []struct {
		hostname string
		status   string
	}{
		{"alpha.example.com", "verified"},
		{"beta.example.com", "verified"},
		{"gamma.example.com", "pending"},
		{"delta.example.com", "failed"},
	}
	for _, item := range domains {
		if _, err := state.Exec(`INSERT INTO domains
			(id, application_id, hostname, type, verification_status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"dom-"+item.hostname, created.ID, item.hostname, "custom", item.status, now, now); err != nil {
			t.Fatal(err)
		}
	}

	urls, err = created.GetAccessibleURLs(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://my-app.203-0-113-10.sslip.io",
		"https://alpha.example.com",
		"https://beta.example.com",
	}
	if len(urls) != len(want) {
		t.Fatalf("urls = %v, want %v", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Fatalf("urls = %v, want %v", urls, want)
		}
	}
}

func TestSetCurrentDeployedURL(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ctx := context.Background()

	created, err := Create(ctx, state, Input{Name: "My App", Slug: "my-app", RepositoryURL: "https://github.com/example/app.git", ExposedPort: 8080}, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}

	deployed := "https://my-app.203-0-113-10.sslip.io"
	if err := SetCurrentDeployedURL(ctx, state, created.ID, deployed); err != nil {
		t.Fatal(err)
	}

	got, err := Get(ctx, state, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentDeployedURL == nil || *got.CurrentDeployedURL != deployed {
		t.Fatalf("current_deployed_url = %v, want %q", got.CurrentDeployedURL, deployed)
	}

	if err := SetCurrentDeployedURL(ctx, state, created.ID, ""); err == nil {
		t.Fatal("empty URL must be rejected")
	}
}
