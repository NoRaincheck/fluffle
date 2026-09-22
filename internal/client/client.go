package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type daemonFile struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
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
	b, err := os.ReadFile(daemonFilePath())
	if err != nil {
		return "", errors.New("DAEMON_DOWN: no daemon.json")
	}
	var df daemonFile
	if err := json.Unmarshal(b, &df); err != nil {
		return "", errors.New("DAEMON_DOWN: bad daemon.json")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(df.Port)
	probe, err := http.Get(base + "/v1/health")
	if err != nil || probe.StatusCode != 200 {
		return "", errors.New("DAEMON_DOWN: probe failed")
	}
	probe.Body.Close()
	return base, nil
}

func EnsureDaemon() (string, error) {
	if base, err := DaemonBaseURL(); err == nil {
		return base, nil
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
		time.Sleep(300 * time.Millisecond)
		if base, err := DaemonBaseURL(); err == nil {
			return base, nil
		} else {
			last = err
		}
	}
	return "", last
}
