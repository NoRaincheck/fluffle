package session

import (
	"strings"
	"sync"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type streamWriter struct {
	store   *store.Store
	session int64

	mu   sync.Mutex
	bufs map[string]*strings.Builder
	done bool

	stop chan struct{}
	wg   sync.WaitGroup
}

func newStreamWriter(s *store.Store, sessionID int64) *streamWriter {
	w := &streamWriter{
		store:   s,
		session: sessionID,
		bufs:    map[string]*strings.Builder{"stdout": {}, "stderr": {}},
		stop:    make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

func (w *streamWriter) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(StreamFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.flushAll()
		}
	}
}

func (w *streamWriter) write(stream string, b []byte) {
	w.mu.Lock()
	buf, ok := w.bufs[stream]
	if !ok || w.done {
		w.mu.Unlock()
		return
	}
	buf.Write(b)
	over := buf.Len() >= StreamFlushBytes
	w.mu.Unlock()
	if over {
		w.flush(stream)
	}
}

func (w *streamWriter) flush(stream string) {
	w.mu.Lock()
	buf, ok := w.bufs[stream]
	if !ok || w.done || buf.Len() == 0 {
		w.mu.Unlock()
		return
	}
	payload := buf.String()
	buf.Reset()
	w.mu.Unlock()
	w.store.AppendSessionEvent(w.session, stream, payload)
}

func (w *streamWriter) flushAll() {
	for stream := range w.bufs {
		w.flush(stream)
	}
}

func (w *streamWriter) closeAll() {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	close(w.stop)
	w.wg.Wait()
	w.flushAll()
	w.mu.Lock()
	w.done = true
	w.mu.Unlock()
}
