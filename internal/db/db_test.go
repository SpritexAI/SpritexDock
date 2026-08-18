package db

import (
	"path/filepath"
	"testing"
)

func TestOpenAppliesInitialSchema(t *testing.T) {
	state, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = state.Close()
	}()

	var version int
	if err := state.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("migration version = %d, want 3", version)
	}

	for _, table := range []string{
		"users",
		"applications",
		"deployments",
		"domains",
		"environment_variables",
	} {
		var name string
		err := state.QueryRow(
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q is missing: %v", table, err)
		}
	}
}

func TestOpenConfiguresSQLite(t *testing.T) {
	state, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = state.Close()
	}()

	var foreignKeys int
	if err := state.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var journalMode string
	if err := state.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()

	first, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = second.Close()
	}()

	var count int
	if err := second.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("migration count = %d, want 3", count)
	}
}

func TestOpenRejectsDataDirectoryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := writeTestFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open succeeded with a file as data directory")
	}
}
