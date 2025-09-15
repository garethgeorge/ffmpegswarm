package ioutil

import (
	"net/http"
	"sync"
)

type ThreadSafeResponseWriter struct {
	W  http.ResponseWriter
	mu sync.Mutex
}

func (tsrw *ThreadSafeResponseWriter) Write(p []byte) (int, error) {
	tsrw.mu.Lock()
	defer tsrw.mu.Unlock()
	return tsrw.W.Write(p)
}

func (tsrw *ThreadSafeResponseWriter) WriteHeader(statusCode int) {
	tsrw.mu.Lock()
	defer tsrw.mu.Unlock()
	tsrw.W.WriteHeader(statusCode)
}

func (tsrw *ThreadSafeResponseWriter) Header() http.Header {
	tsrw.mu.Lock()
	defer tsrw.mu.Unlock()
	return tsrw.W.Header()
}

func (tsrw *ThreadSafeResponseWriter) Flush() {
	tsrw.mu.Lock()
	defer tsrw.mu.Unlock()
	if flusher, ok := tsrw.W.(http.Flusher); ok {
		flusher.Flush()
	}
}
