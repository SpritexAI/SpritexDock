package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

var (
	ErrNotFound      = errors.New("application not found")
	ErrDuplicateSlug = errors.New("application slug already exists")
	ErrInvalidInput  = errors.New("invalid application input")
)

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

const (
	defaultBranch       = "main"
	defaultDockerfile   = "Dockerfile"
	defaultBuildContext = "."
	defaultAppStatus    = "created"
)

// Application is the persisted application configuration.
type Application struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Slug                string  `json:"slug"`
	RepositoryURL       string  `json:"repository_url"`
	Branch              string  `json:"branch"`
	DockerfilePath      string  `json:"dockerfile_path"`
	BuildContext        string  `json:"build_context"`
	ExposedPort         int     `json:"exposed_port"`
	GeneratedHostname   string  `json:"generated_hostname"`
	CurrentDeploymentID *string `json:"current_deployment_id,omitempty"`
	Status              string  `json:"status"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

// Input contains fields accepted when creating or updating an application.
type Input struct {
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	RepositoryURL  string `json:"repository_url"`
	Branch         string `json:"branch"`
	DockerfilePath string `json:"dockerfile_path"`
	BuildContext   string `json:"build_context"`
	ExposedPort    int    `json:"exposed_port"`
}

// Normalize applies request-boundary defaults and trimming.
func (in *Input) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.TrimSpace(strings.ToLower(in.Slug))
	in.RepositoryURL = strings.TrimSpace(in.RepositoryURL)
	in.Branch = strings.TrimSpace(in.Branch)
	in.DockerfilePath = strings.TrimSpace(in.DockerfilePath)
	in.BuildContext = strings.TrimSpace(in.BuildContext)
	if in.Branch == "" {
		in.Branch = defaultBranch
	}
	if in.DockerfilePath == "" {
		in.DockerfilePath = defaultDockerfile
	}
	if in.BuildContext == "" {
		in.BuildContext = defaultBuildContext
	}
}

// Validate checks untrusted application configuration before persistence.
func (in Input) Validate() error {
	if in.Name == "" || len(in.Name) > 200 {
		return fmt.Errorf("%w: name must be between 1 and 200 characters", ErrInvalidInput)
	}
	if len(in.Slug) == 0 || len(in.Slug) > 63 || !slugPattern.MatchString(in.Slug) {
		return fmt.Errorf("%w: slug must be lowercase kebab-case and at most 63 characters", ErrInvalidInput)
	}
	if err := validateRepositoryURL(in.RepositoryURL); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	if in.Branch == "" || len(in.Branch) > 250 || !branchPattern.MatchString(in.Branch) || strings.Contains(in.Branch, "..") || strings.HasPrefix(in.Branch, "/") || strings.HasSuffix(in.Branch, "/") {
		return fmt.Errorf("%w: invalid branch", ErrInvalidInput)
	}
	if err := validateRelativePath(in.DockerfilePath); err != nil {
		return fmt.Errorf("%w: dockerfile path: %s", ErrInvalidInput, err)
	}
	if err := validateRelativePath(in.BuildContext); err != nil {
		return fmt.Errorf("%w: build context: %s", ErrInvalidInput, err)
	}
	if in.ExposedPort < 1 || in.ExposedPort > 65535 {
		return fmt.Errorf("%w: exposed port must be between 1 and 65535", ErrInvalidInput)
	}
	return nil
}

func validateRepositoryURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("repository URL must be a valid public URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("repository URL must use http or https")
	}
	if parsed.User != nil {
		return errors.New("repository URL must not include credentials")
	}
	return nil
}

func validateRelativePath(path string) error {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return errors.New("path must be a non-empty relative path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return errors.New("path traversal is not allowed")
		}
	}
	return nil
}

// GenerateHostname returns the deterministic sslip.io hostname for a slug.
func GenerateHostname(slug, publicIP string) string {
	publicIP = strings.TrimSpace(publicIP)
	if publicIP == "" {
		publicIP = "127.0.0.1"
	}
	return slug + "." + strings.ReplaceAll(publicIP, ".", "-") + ".sslip.io"
}

// Create persists the first application configuration.
func Create(ctx context.Context, state *db.DB, input Input, publicIP string) (*Application, error) {
	input.Normalize()
	if err := input.Validate(); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	app := &Application{
		ID:                uuid.NewString(),
		Name:              input.Name,
		Slug:              input.Slug,
		RepositoryURL:     input.RepositoryURL,
		Branch:            input.Branch,
		DockerfilePath:    input.DockerfilePath,
		BuildContext:      input.BuildContext,
		ExposedPort:       input.ExposedPort,
		GeneratedHostname: GenerateHostname(input.Slug, publicIP),
		Status:            defaultAppStatus,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	_, err := state.ExecContext(ctx, `
		INSERT INTO applications
		(id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, app.ID, app.Name, app.Slug, app.RepositoryURL, app.Branch, app.DockerfilePath, app.BuildContext, app.ExposedPort, app.GeneratedHostname, app.Status, app.CreatedAt, app.UpdatedAt)
	if err != nil {
		if isUniqueApplicationError(err) {
			return nil, ErrDuplicateSlug
		}
		return nil, fmt.Errorf("create application: %w", err)
	}
	return app, nil
}

// Get returns one application by ID.
func Get(ctx context.Context, state *db.DB, id string) (*Application, error) {
	app := &Application{}
	err := scanApplication(state.QueryRowContext(ctx, applicationSelect+" WHERE id = ?", id), app)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get application: %w", err)
	}
	return app, nil
}

// List returns applications ordered from newest to oldest.
func List(ctx context.Context, state *db.DB) ([]*Application, error) {
	rows, err := state.QueryContext(ctx, applicationSelect+" ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var applications []*Application
	for rows.Next() {
		app := &Application{}
		if err := scanApplication(rows, app); err != nil {
			return nil, fmt.Errorf("scan application: %w", err)
		}
		applications = append(applications, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	return applications, nil
}

// Update changes mutable application configuration while preserving identity and status.
func Update(ctx context.Context, state *db.DB, id string, input Input, publicIP string) (*Application, error) {
	input.Normalize()
	if err := input.Validate(); err != nil {
		return nil, err
	}
	current, err := Get(ctx, state, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	hostname := GenerateHostname(input.Slug, publicIP)
	_, err = state.ExecContext(ctx, `
		UPDATE applications
		SET name = ?, slug = ?, repository_url = ?, branch = ?, dockerfile_path = ?, build_context = ?, exposed_port = ?, generated_hostname = ?, updated_at = ?
		WHERE id = ?
	`, input.Name, input.Slug, input.RepositoryURL, input.Branch, input.DockerfilePath, input.BuildContext, input.ExposedPort, hostname, now, id)
	if err != nil {
		if isUniqueApplicationError(err) {
			return nil, ErrDuplicateSlug
		}
		return nil, fmt.Errorf("update application: %w", err)
	}
	current.Name = input.Name
	current.Slug = input.Slug
	current.RepositoryURL = input.RepositoryURL
	current.Branch = input.Branch
	current.DockerfilePath = input.DockerfilePath
	current.BuildContext = input.BuildContext
	current.ExposedPort = input.ExposedPort
	current.GeneratedHostname = hostname
	current.UpdatedAt = now
	return current, nil
}

// Delete removes an application and its dependent records through foreign-key cascades.
func Delete(ctx context.Context, state *db.DB, id string) error {
	result, err := state.ExecContext(ctx, "DELETE FROM applications WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete application: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete application: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueApplicationError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed: applications.slug") || strings.Contains(message, "UNIQUE constraint failed: applications.generated_hostname")
}

const applicationSelect = `
	SELECT id, name, slug, repository_url, branch, dockerfile_path, build_context, exposed_port, generated_hostname, current_deployment_id, status, created_at, updated_at
	FROM applications`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanApplication(row rowScanner, app *Application) error {
	return row.Scan(&app.ID, &app.Name, &app.Slug, &app.RepositoryURL, &app.Branch, &app.DockerfilePath, &app.BuildContext, &app.ExposedPort, &app.GeneratedHostname, &app.CurrentDeploymentID, &app.Status, &app.CreatedAt, &app.UpdatedAt)
}
