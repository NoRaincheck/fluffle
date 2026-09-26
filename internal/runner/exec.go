package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type execRunner struct{}

func NewExec() Runner { return execRunner{} }

func (execRunner) Run(ctx context.Context, req Request) (Result, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(req.Command, req.Args...)
	cmd.Dir = req.Cwd
	cmd.Env = mergeEnv(os.Environ(), req.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	} else {
		cmd.Stdin = strings.NewReader("")
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("spawn %s: %w", req.Command, err)
	}

	pgid := cmd.Process.Pid
	var killOnce sync.Once
	killGroup := func() {
		killOnce.Do(func() {
			if pgid > 0 {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		})
	}

	watchDone := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			killGroup()
		case <-watchDone:
		}
	}()

	limits := map[string]*limiter{"stdout": {}, "stderr": {}}
	var wg sync.WaitGroup
	drain := func(stream string, r io.Reader) {
		defer wg.Done()
		lim := limits[stream]
		buf := make([]byte, 32*1024)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				if out := lim.push(buf[:n]); len(out) > 0 && req.OnChunk != nil {
					req.OnChunk(stream, out)
				}
			}
			if readErr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go drain("stdout", stdout)
	go drain("stderr", stderr)

	waitErr := cmd.Wait()
	killGroup()
	close(watchDone)
	wg.Wait()

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return Result{}, ErrTimeout
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return Result{ExitCode: exitErr.ExitCode()}, nil
		}
		return Result{}, waitErr
	}
	return Result{ExitCode: 0}, nil
}

func mergeEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	overrides := map[string]bool{}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		overrides[k] = true
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !overrides[k] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}
