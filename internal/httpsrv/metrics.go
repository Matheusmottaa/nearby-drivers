package httpsrv

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
)

type httpKey struct {
	method string
	route  string
	code   int
}

type httpStats struct {
	mu     sync.Mutex
	counts map[httpKey]int64
}

func newHTTPStats() *httpStats {
	return &httpStats{counts: map[httpKey]int64{}}
}

func (s *httpStats) add(method, route string, code int) {
	s.mu.Lock()
	s.counts[httpKey{method: method, route: route, code: code}]++
	s.mu.Unlock()
}

func (s *httpStats) write(w io.Writer) {
	s.mu.Lock()
	keys := make([]httpKey, 0, len(s.counts))
	for key := range s.counts {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b httpKey) int {
		if a.route != b.route {
			if a.route < b.route {
				return -1
			}
			return 1
		}
		if a.method != b.method {
			if a.method < b.method {
				return -1
			}
			return 1
		}
		return a.code - b.code
	})
	fmt.Fprintf(w, "# HELP http_requests_total HTTP requests served by this process, excluding probes.\n# TYPE http_requests_total counter\n")
	for _, key := range keys {
		fmt.Fprintf(w, "http_requests_total{method=%q,route=%q,code=%q} %d\n", key.method, key.route, fmt.Sprintf("%d", key.code), s.counts[key])
	}
	s.mu.Unlock()
}

// statusRecorder captures the status code and still flushes server-sent events.
type statusRecorder struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.code = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
