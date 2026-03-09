package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSSEKeepaliveHandler_SendsPings(t *testing.T) {
	// Create a handler that blocks until we signal it.
	unblock := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set SSE headers so the test is realistic.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-unblock
	})

	handler := SSEKeepaliveHandler(inner, 50*time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sse", nil)

	// Run handler in a goroutine; wait for it to finish before reading body.
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	// Wait enough time for at least 2 keepalive pings.
	time.Sleep(150 * time.Millisecond)
	close(unblock)
	<-done // handler fully returned; safe to read rec.Body

	body := rec.Body.String()
	count := strings.Count(body, ": keepalive")
	if count < 2 {
		t.Errorf("expected at least 2 keepalive pings, got %d; body: %q", count, body)
	}
}

func TestSSEKeepaliveWriter_ConcurrentSafe(t *testing.T) {
	rec := httptest.NewRecorder()
	kw := &sseKeepaliveWriter{ResponseWriter: rec}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			kw.Write([]byte("data: test\n\n"))
		}()
	}
	wg.Wait()
	// If we get here without a race panic, the test passes.
}
