package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/NoRaincheck/fluffle/internal/apiserver"
	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/store"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flf <daemon|channel|thread|message|react|agent|tui>")
		return 1
	}
	switch args[0] {
	case "daemon":
		return daemonCmd(args[1:])
	case "tui":
		fmt.Fprintln(os.Stderr, "backend-only milestone: tui deferred")
		return 1
	default:
		fmt.Fprintln(os.Stderr, "unknown command (backend slice implements daemon + tui stub; rest in Task 7-8)")
		return 1
	}
}

func daemonCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flf daemon <start|stop|status>")
		return 1
	}
	switch args[0] {
	case "status":
		base, err := client.DaemonBaseURL()
		if err != nil {
			fmt.Fprintln(os.Stderr, "daemon down")
			return 2
		}
		fmt.Println("daemon up at", base)
		return 0
	case "start":
		background := len(args) > 1 && args[1] == "--background"
		return daemonStart(background)
	case "stop":
		return daemonStop()
	}
	return 1
}

func daemonStop() int {
	b, err := os.ReadFile(filepath.Join(client.FluffleHome(), "daemon.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN: not running")
		return 2
	}
	var df struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(b, &df); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN: bad daemon.json")
		return 2
	}
	proc, err := os.FindProcess(df.PID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	if err := proc.Kill(); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		os.Remove(filepath.Join(client.FluffleHome(), "daemon.json"))
		return 2
	}
	os.Remove(filepath.Join(client.FluffleHome(), "daemon.json"))
	fmt.Println("daemon stopped")
	return 0
}

func daemonStart(background bool) int {
	home := client.FluffleHome()
	if err := os.MkdirAll(home, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	dbPath := filepath.Join(home, "fluffle.db")
	s, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	port := ln.Addr().(*net.TCPAddr).Port
	payload, err := json.Marshal(map[string]any{"port": port, "pid": os.Getpid(), "started_at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	if err := os.WriteFile(filepath.Join(home, "daemon.json"), payload, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	srv := &http.Server{Handler: apiserver.NewHandler(s)}
	if !background {
		fmt.Println("fluffle daemon on 127.0.0.1:" + strconv.Itoa(port))
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
		return 2
	}
	return 0
}
