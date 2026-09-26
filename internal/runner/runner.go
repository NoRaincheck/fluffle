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
	tailBytes      = MaxStreamBytes / 4
	headBytes      = MaxStreamBytes - tailBytes - len(ElisionMarker)
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
	mu       sync.Mutex
	head     int
	tail     []byte
	overflow bool
	sealed   bool
}

func (l *limiter) push(b []byte) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sealed {
		return nil
	}
	if l.overflow {
		l.keepTail(b)
		return nil
	}
	if len(b) <= headBytes-l.head {
		l.head += len(b)
		return b
	}
	room := headBytes - l.head
	l.head = headBytes
	l.overflow = true
	l.keepTail(b[room:])
	return b[:room]
}

func (l *limiter) keepTail(b []byte) {
	if len(b) >= tailBytes {
		l.tail = append(l.tail[:0], b[len(b)-tailBytes:]...)
		return
	}
	if drop := len(l.tail) + len(b) - tailBytes; drop > 0 {
		l.tail = append(l.tail[:0], l.tail[drop:]...)
	}
	l.tail = append(l.tail, b...)
}

func (l *limiter) Seal() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sealed {
		return nil
	}
	l.sealed = true
	if !l.overflow {
		return nil
	}
	out := make([]byte, 0, len(ElisionMarker)+len(l.tail))
	out = append(out, ElisionMarker...)
	return append(out, l.tail...)
}
