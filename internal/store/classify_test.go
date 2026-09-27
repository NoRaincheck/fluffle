package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestClassifyPassesThroughNonSQLiteError(t *testing.T) {
	sentinel := errors.New("boom")
	if got := classify(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("got %v, want the original error", got)
	}
}

func TestClassifyPassesThroughUnrelatedSQLError(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	_, err = conn.Exec(`SELECT * FROM nonexistent_table`)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := classify(err)
	if errors.Is(got, ErrNotFound) || errors.Is(got, ErrConflict) || errors.Is(got, ErrInvalid) {
		t.Fatalf("unexpectedly classified as a sentinel: %v", got)
	}
}

func TestClassifyConstraintCodes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateChannel("c", "/r", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		query        string
		want         error
		wantContains string
	}{
		{"unique", `INSERT INTO channels(name, repo_abs_path, is_orphaned) VALUES('c', '/r', 0)`, ErrConflict, "UNIQUE constraint failed"},
		{"check", `INSERT INTO channels(name, repo_abs_path, is_orphaned) VALUES('   ', '/r', 0)`, ErrInvalid, "CHECK constraint failed"},
		{"notnull", `INSERT INTO channels(name, repo_abs_path, is_orphaned) VALUES(NULL, '/r', 0)`, ErrInvalid, "NOT NULL constraint failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.db.Exec(tc.query)
			if err == nil {
				t.Fatal("expected a constraint violation")
			}
			got := classify(err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("classify(%v) = %v, want %v", err, got, tc.want)
			}
			if msg := got.Error(); !strings.Contains(msg, tc.wantContains) {
				t.Fatalf("classify message = %q, want it to contain %q", msg, tc.wantContains)
			}
		})
	}
}

func TestClassifyForeignKeyViolation(t *testing.T) {
	t.Skip("PRAGMA foreign_keys=ON lands in task 10; un-skip this test there")
}
