package session

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type gatedAppender struct {
	inner *store.Store

	mu    sync.Mutex
	calls []string

	first    chan struct{}
	firstOne sync.Once
	gate     chan struct{}
	gateOnce sync.Once
}

func newGatedAppender(inner *store.Store) *gatedAppender {
	return &gatedAppender{inner: inner, first: make(chan struct{}), gate: make(chan struct{})}
}

func (g *gatedAppender) AppendSessionEvent(sessionID int64, eventType, content string) (int64, int64, error) {
	g.mu.Lock()
	g.calls = append(g.calls, content)
	g.mu.Unlock()
	g.firstOne.Do(func() { close(g.first) })
	<-g.gate
	return g.inner.AppendSessionEvent(sessionID, eventType, content)
}

func (g *gatedAppender) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *gatedAppender) release() { g.gateOnce.Do(func() { close(g.gate) }) }

func newStreamFixture(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, err := s.CreateChannel("c", "/repo", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(chID, "t")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.AppendMessage(thID, "alice", "human", "user", "@probe hi")
	if err != nil {
		t.Fatal(err)
	}
	msgID, err := s.MessageIDBySeq(thID, seq)
	if err != nil {
		t.Fatal(err)
	}
	sessID, err := s.CreateSession(thID, msgID, "probe", store.SessionQueued, "stdout", "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, sessID
}

func TestStreamWriterSerializesSameStreamFlushes(t *testing.T) {
	s, sessID := newStreamFixture(t)
	g := newGatedAppender(s)
	w := newStreamWriter(g, sessID)
	t.Cleanup(w.closeAll)
	t.Cleanup(g.release)

	first := strings.Repeat("a", StreamFlushBytes)
	second := strings.Repeat("b", StreamFlushBytes)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		w.write("stdout", []byte(first))
	}()
	<-g.first
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		w.write("stdout", []byte(second))
	}()

	time.Sleep(100 * time.Millisecond)
	if n := g.callCount(); n != 1 {
		t.Fatalf("appends in flight = %d, want 1: a same-stream flush must not be overtaken", n)
	}

	g.release()
	<-firstDone
	<-secondDone
	w.closeAll()

	events, err := s.ListSessionEvents(sessID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Type == store.SessionEventStdout {
			out = append(out, e.Content)
		}
	}
	if len(out) != 2 {
		t.Fatalf("stdout events = %d, want 2", len(out))
	}
	if out[0] != first || out[1] != second {
		t.Fatalf("transcript is out of emission order: first event starts %q, second starts %q", out[0][:8], out[1][:8])
	}
}

func TestStreamWriterCloseAllIsIdempotent(t *testing.T) {
	s, sessID := newStreamFixture(t)
	w := newStreamWriter(s, sessID)
	w.write("stdout", []byte("tail"))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.closeAll()
		}()
	}
	wg.Wait()
	events, err := s.ListSessionEvents(sessID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Content != "tail" {
		t.Fatalf("events = %+v, want exactly one stdout event holding the buffered tail", events)
	}
}
