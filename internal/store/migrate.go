package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations(
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`

type migration struct {
	version int64
	name    string
	up      string
}

var errMigrationConflict = errors.New("migration conflict")

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		raw, err := fs.ReadFile(fsys, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{
			version: version,
			name:    e.Name(),
			up:      upSection(string(raw)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("%w: duplicate version %d (%s, %s)", errMigrationConflict, out[i].version, out[i-1].name, out[i].name)
		}
	}
	return out, nil
}

func parseMigrationVersion(name string) (int64, error) {
	base, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q: want NNNNN_name.sql", name)
	}
	v, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("migration %q: bad version prefix: %w", name, err)
	}
	return v, nil
}

func upSection(content string) string {
	up := content
	if i := strings.Index(content, "-- +goose Down"); i >= 0 {
		up = content[:i]
	}
	if i := strings.Index(up, "-- +goose Up"); i >= 0 {
		up = up[i+len("-- +goose Up"):]
	}
	return up
}

func migrateUp(conn *sql.DB, fsys fs.FS) error {
	if _, err := conn.Exec(schemaMigrationsDDL); err != nil {
		return err
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	var current int64
	if err := conn.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		if err := applyMigration(conn, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

func applyMigration(conn *sql.DB, m migration) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(m.up); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name) VALUES(?, ?)`, m.version, m.name); err != nil {
		return err
	}
	return tx.Commit()
}

func enableForeignKeys(conn *sql.DB) error {
	if _, err := conn.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	return assertForeignKeys(conn)
}

func assertForeignKeys(conn *sql.DB) error {
	var fk int
	if err := conn.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		return err
	}
	if fk != 1 {
		return fmt.Errorf("sqlite refused PRAGMA foreign_keys=ON (got %d)", fk)
	}
	return nil
}

func legacyRebuildRequired(conn *sql.DB) (bool, error) {
	present, err := tableExists(conn, "messages")
	if err != nil || !present {
		return false, err
	}
	has, err := hasColumn(conn, "messages", "parent_id")
	if err != nil {
		return false, err
	}
	return !has, nil
}

func tableExists(conn *sql.DB, table string) (bool, error) {
	var n int
	err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return n > 0, err
}

func dropLegacyTables(conn *sql.DB) error {
	for _, tbl := range []string{"reactions", "messages", "threads", "channels"} {
		if _, err := conn.Exec(`DROP TABLE IF EXISTS ` + tbl); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func dropArchivedAtColumns(conn *sql.DB) error {
	for _, table := range []string{"channels", "threads"} {
		present, err := hasColumn(conn, table, columnArchivedAt)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if _, err := conn.Exec(`ALTER TABLE ` + table + ` DROP COLUMN ` + columnArchivedAt); err != nil {
			return err
		}
	}
	return nil
}

const columnArchivedAt = "archived_at"
