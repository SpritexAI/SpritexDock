package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const databaseFile = "spritexdock.db"

// migrations contains the schema shipped with the control-plane binary.
//
//go:embed migrations/*.sql
var migrations embed.FS

// DB owns the SQLite connection used by the control plane.
type DB struct {
	*sql.DB
}

// Open creates the data directory, opens its SQLite database, and applies
// pending schema migrations. The caller owns the returned handle.
func Open(dataDir string) (*DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("database data directory must not be empty")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create database data directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", filepath.Join(dataDir, databaseFile))
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// ponytail: one connection avoids SQLite writer contention until workload needs a pool.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	state := &DB{DB: sqlDB}
	if err := state.configure(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := state.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return state, nil
}

func (db *DB) configure() error {
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("configure sqlite with %q: %w", pragma, err)
		}
	}
	return nil
}

func (db *DB) migrate() error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	files, err := migrationFiles()
	if err != nil {
		return err
	}

	var current int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for _, migration := range files {
		if migration.version <= current {
			continue
		}
		if migration.version != current+1 {
			return fmt.Errorf("missing migration %04d before %04d", current+1, migration.version)
		}
		if err := db.applyMigration(migration); err != nil {
			return err
		}
		current = migration.version
	}
	return nil
}

type migration struct {
	version int
	name    string
	body    []byte
}

func migrationFiles() ([]migration, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	result := make([]migration, 0, len(entries))
	seen := make(map[int]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		if _, exists := seen[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %04d", version)
		}
		body, err := fs.ReadFile(migrations, filepath.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		seen[version] = struct{}{}
		result = append(result, migration{version: version, name: entry.Name(), body: body})
	}

	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	return result, nil
}

func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok || len(prefix) != 4 {
		return 0, fmt.Errorf("invalid migration filename %q", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version < 1 {
		return 0, fmt.Errorf("invalid migration version in %q", name)
	}
	return version, nil
}

func (db *DB) applyMigration(migration migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %04d: %w", migration.version, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(string(migration.body)); err != nil {
		return fmt.Errorf("apply migration %s: %w", migration.name, err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", migration.version); err != nil {
		return fmt.Errorf("record migration %04d: %w", migration.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %04d: %w", migration.version, err)
	}
	return nil
}
