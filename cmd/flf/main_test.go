package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTuiDaemonRequired(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(home, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLUFFLE_HOME", home)
	if got := run([]string{"tui"}); got != 2 {
		t.Fatalf("want exit 2 (daemon required) got %d", got)
	}
}

func TestInitOutsideGitFails(t *testing.T) {
	dir := t.TempDir()
	if got := run([]string{"init", "--repo", dir}); got != 1 {
		t.Fatalf("want exit 1 got %d", got)
	}
}
