package session

import (
	"strings"
	"sync"
	"time"
)

type eventAppender interface {
	AppendSessionEvent(sessionID int64, eventType, content string) (int64, int64, error)
}

type streamWriter struct {
	store   eventAppender
	session int64

	mu      sync.Mutex
	bufs    map[string]*strings.Builder
	flushes map[string]*sync.Mutex
	done    bool

	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

func newStreamWriter(s eventAppender, sessionID int64) *streamWriter {
	w := &streamWriter{
		store:   s,
		session: sessionID,
		bufs:    map[string]*strings.Builder{"stdout": {}, "stderr": {}},
		flushes: map[string]*sync.Mutex{"stdout": {}, "stderr": {}},
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
	flushMu, known := w.flushes[stream]
	if !known {
		return
	}
	flushMu.Lock()
	defer flushMu.Unlock()
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
	w.once.Do(func() {
		close(w.stop)
		w.wg.Wait()
		w.flushAll()
		w.mu.Lock()
		w.done = true
		w.mu.Unlock()
	})
}
