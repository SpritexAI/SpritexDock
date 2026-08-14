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
