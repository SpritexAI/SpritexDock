package auth

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

func TestSeedOwnerAndAuthenticate(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	ctx := context.Background()
	if err := SeedOwner(ctx, state, "owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}

	id, err := Authenticate(ctx, state, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("Authenticate returned an empty user ID")
	}

	if _, err := Authenticate(ctx, state, "owner", "wrong password"); err == nil {
		t.Fatal("Authenticate accepted an invalid password")
	}
}

func TestSeedOwnerIsIdempotent(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	ctx := context.Background()
	if err := SeedOwner(ctx, state, "owner", "first password"); err != nil {
		t.Fatal(err)
	}
	if err := SeedOwner(ctx, state, "other", "second password"); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := state.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user count = %d, want 1", count)
	}

	if _, err := Authenticate(ctx, state, "owner", "first password"); err != nil {
		t.Fatal("original owner credentials no longer work")
	}
}

func TestSeedOwnerValidatesCredentials(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	for _, test := range []struct {
		name     string
		username string
		password string
	}{
		{name: "missing username", password: "password"},
		{name: "missing password", username: "owner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := SeedOwner(context.Background(), state, test.username, test.password); err == nil {
				t.Fatal("SeedOwner accepted invalid credentials")
			}
		})
	}
}

func TestSeedOwnerStoresBcryptHash(t *testing.T) {
	state, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	password := "not stored in plaintext"
	if err := SeedOwner(context.Background(), state, "owner", password); err != nil {
		t.Fatal(err)
	}

	var hash string
	if err := state.QueryRow("SELECT password_hash FROM users WHERE username = ?", "owner").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, password) {
		t.Fatal("password was stored in plaintext")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		t.Fatalf("stored password is not a valid bcrypt hash: %v", err)
	}
}
