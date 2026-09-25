package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const (
	httpClientTimeout   = 5 * time.Second
	daemonHealthTimeout = time.Second
)

type daemonFile struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: httpClientTimeout}
}

func FluffleHome() string {
	if v := os.Getenv("FLUFFLE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".fluffle")
}

func daemonFilePath() string { return filepath.Join(FluffleHome(), "daemon.json") }

func DaemonBaseURL() (string, error) {
	return DaemonBaseURLContext(context.Background())
}

func DaemonBaseURLContext(ctx context.Context) (string, error) {
	b, err := os.ReadFile(daemonFilePath())
	if err != nil {
		return "", errors.New("DAEMON_DOWN: no daemon.json")
	}
	var df daemonFile
	if err := json.Unmarshal(b, &df); err != nil {
		return "", errors.New("DAEMON_DOWN: bad daemon.json")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(df.Port)
	probeCtx, cancel := context.WithTimeout(ctx, daemonHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, base+"/v1/health", nil)
	if err != nil {
		return "", errors.New("DAEMON_DOWN: probe failed")
	}
	probe, err := NewHTTPClient().Do(req)
	if err != nil {
		return "", errors.New("DAEMON_DOWN: probe failed")
	}
	defer probe.Body.Close()
	if probe.StatusCode != http.StatusOK {
		return "", errors.New("DAEMON_DOWN: probe failed")
	}
	return base, nil
}

func EnsureDaemon() (string, error) {
	return EnsureDaemonContext(context.Background())
}

func EnsureDaemonContext(ctx context.Context) (string, error) {
	if base, err := DaemonBaseURLContext(ctx); err == nil {
		return base, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, "daemon", "start", "--background")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return "", errors.New("DAEMON_DOWN: spawn failed")
	}
	var last error
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
		if base, err := DaemonBaseURLContext(ctx); err == nil {
			return base, nil
		} else {
			last = err
		}
	}
	return "", last
}
