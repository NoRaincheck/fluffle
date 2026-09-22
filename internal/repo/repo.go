package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Canonicalize(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func InspectGitDir(absPath string) (remote, headSHA string, isGit bool) {
	fi, err := os.Stat(filepath.Join(absPath, ".git"))
	if err != nil {
		return "", "", false
	}
	if !fi.IsDir() && fi.Size() == 0 {
		return "", "", false
	}
	out, err := exec.Command("git", "-C", absPath, "remote", "get-url", "origin").Output()
	if err == nil {
		remote = strings.TrimSpace(string(out))
	}
	out, err = exec.Command("git", "-C", absPath, "rev-parse", "HEAD").Output()
	if err == nil {
		headSHA = strings.TrimSpace(string(out))
	}
	return remote, headSHA, true
}
