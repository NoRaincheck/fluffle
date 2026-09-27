package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrateUpAppliesInitialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("version = %d, want 1", v)
	}
	channels, err := s.ListChannels(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 0 {
		t.Fatalf("want empty, got %d", len(channels))
	}
}

func TestMigrateUpIsIdempotentOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.CreateChannel(context.Background(), "c", "/r", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	channels, err := s2.ListChannels(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("want 1 channel, got %d", len(channels))
	}
}

func TestMigrateUpAppliesInAscendingOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_one.sql":   &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS one(id INTEGER);\n-- +goose Down\nDROP TABLE one;\n")},
		"migrations/00002_two.sql":   &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS two(id INTEGER);\n-- +goose Down\nDROP TABLE two;\n")},
		"migrations/00003_three.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS three(id INTEGER);\n-- +goose Down\nDROP TABLE three;\n")},
	}
	path := filepath.Join(t.TempDir(), "c.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if err := migrateUp(conn, fsys); err != nil {
		t.Fatal(err)
	}
	var v int64
	if err := conn.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
	for _, tbl := range []string{"one", "two", "three"} {
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("table %s missing", tbl)
		}
	}
}

func TestMigrateUpLeavesVersionUnchangedOnFailure(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_ok.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS ok(id INTEGER);\n")},
		"migrations/00002_bad.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS ok(id INTEGER);\nTHIS IS NOT SQL;\n")},
	}
	path := filepath.Join(t.TempDir(), "d.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if err := migrateUp(conn, fsys); err == nil {
		t.Fatal("want error from malformed migration")
	}
	var v int64
	if err := conn.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("version = %d, want 1 (failed migration must not record)", v)
	}
}

func TestMigrateUpAdoptsCurrentShapeWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.CreateChannel(context.Background(), "keepme", "/r", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	s1.Close()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	channels, err := s2.ListChannels(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].Name != "keepme" {
		t.Fatalf("adoption lost data: %+v", channels)
	}
}

func TestOpenEnablesForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fk.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1", fk)
	}
}

func TestForeignKeyViolationMapsToNotFound(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fk2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateThread(context.Background(), 99999, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestLoadMigrationsSortsByParsedVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00010_ten.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 10;\n-- +goose Down\nSELECT 10;\n")},
		"migrations/00002_two.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 2;\n-- +goose Down\nSELECT 2;\n")},
		"migrations/00009_nine.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 9;\n-- +goose Down\nSELECT 9;\n")},
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d migrations, want 3", len(ms))
	}
	if ms[0].version != 2 || ms[1].version != 9 || ms[2].version != 10 {
		t.Fatalf("wrong order: %d %d %d", ms[0].version, ms[1].version, ms[2].version)
	}
}

func TestLoadMigrationsStripsDownSection(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_x.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE t(id INTEGER);\n-- +goose Down\nDROP TABLE t;\n")},
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d, want 1", len(ms))
	}
	if strings.Contains(ms[0].up, "DROP TABLE") {
		t.Fatalf("down section leaked into up: %q", ms[0].up)
	}
	if !strings.Contains(ms[0].up, "CREATE TABLE t") {
		t.Fatalf("up section missing: %q", ms[0].up)
	}
}
