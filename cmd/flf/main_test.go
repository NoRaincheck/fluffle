package main

import "testing"

func TestTuiDaemonRequired(t *testing.T) {
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
