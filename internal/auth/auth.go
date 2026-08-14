package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

var errInvalidCredentials = errors.New("invalid credentials")

// SeedOwner creates the first local owner when the users table is empty.
func SeedOwner(ctx context.Context, state *db.DB, username, password string) error {
	var count int
	if err := state.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("check owner account: %w", err)
	}
	if count > 0 {
		return nil
	}

	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("admin username must not be empty")
	}
	if password == "" {
		return fmt.Errorf("admin password must not be empty")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash owner password: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := state.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, uuid.NewString(), username, string(hash), now, now); err != nil {
		return fmt.Errorf("create owner account: %w", err)
	}
	return nil
}

// Authenticate returns the user ID for valid credentials.
func Authenticate(ctx context.Context, state *db.DB, username, password string) (string, error) {
	var id, hash string
	if err := state.QueryRowContext(ctx,
		"SELECT id, password_hash FROM users WHERE username = ?",
		strings.TrimSpace(username),
	).Scan(&id, &hash); err != nil {
		return "", errInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", errInvalidCredentials
	}
	return id, nil
}
