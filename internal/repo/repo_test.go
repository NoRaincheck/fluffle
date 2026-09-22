package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNonGitDirReportsFalse(t *testing.T) {
	dir := t.TempDir()
	_, _, isGit := InspectGitDir(dir)
	if isGit {
		t.Fatal("expected not-git")
	}
}

func TestGitDirReportsTrue(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, isGit := InspectGitDir(dir)
	if !isGit {
		t.Fatal("expected git")
	}
}
