package runner

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrTimeout = errors.New("agent timed out")

const (
	MaxStreamBytes = 1 << 20
	ElisionMarker  = "\n...[fluffle: output truncated]...\n"
)

type Request struct {
	Command string
	Args    []string
	Stdin   string
	Cwd     string
	Env     []string
	Timeout time.Duration
	OnChunk func(stream string, b []byte)
}

type Result struct {
	ExitCode int
}

type Runner interface {
	Run(ctx context.Context, req Request) (Result, error)
}

type limiter struct {
	mu      sync.Mutex
	written int
	sealed  bool
}

func (l *limiter) push(b []byte) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sealed {
		return nil
	}
	remaining := MaxStreamBytes - l.written
	if len(b) <= remaining {
		l.written += len(b)
		return b
	}
	l.sealed = true
	l.written = MaxStreamBytes
	out := make([]byte, 0, remaining+len(ElisionMarker))
	out = append(out, b[:remaining]...)
	return append(out, ElisionMarker...)
}
