package mcp

import (
	"net/http"
	"sync"
	"time"
)

// sseKeepaliveWriter wraps an http.ResponseWriter with a mutex to allow
// concurrent writes from the keepalive goroutine and the SDK's SSE handler.
type sseKeepaliveWriter struct {
	http.ResponseWriter
	mu sync.Mutex
}

func (w *sseKeepaliveWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.Write(p)
}

func (w *sseKeepaliveWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *sseKeepaliveWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// SSEKeepaliveHandler wraps an SSE handler to:
// 1. Clear the HTTP write deadline (SSE connections are long-lived)
// 2. Send periodic SSE comment pings to prevent proxy/client timeouts
func SSEKeepaliveHandler(handler http.Handler, interval time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clear the write deadline so the HTTP server's WriteTimeout
		// doesn't kill this long-lived SSE connection.
		rc := http.NewResponseController(w)
		rc.SetWriteDeadline(time.Time{})

		kw := &sseKeepaliveWriter{ResponseWriter: w}

		// Start keepalive pings in background.
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					kw.mu.Lock()
					_, err := kw.ResponseWriter.Write([]byte(": keepalive\n\n"))
					if err != nil {
						kw.mu.Unlock()
						return
					}
					if f, ok := kw.ResponseWriter.(http.Flusher); ok {
						f.Flush()
					}
					kw.mu.Unlock()
				case <-done:
					return
				case <-r.Context().Done():
					return
				}
			}
		}()

		handler.ServeHTTP(kw, r)

		// Signal keepalive goroutine to stop, then wait for it to finish
		// so no writes race with callers after ServeHTTP returns.
		close(done)
		wg.Wait()
	})
}
