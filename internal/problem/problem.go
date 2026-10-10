// Package problem writes the HTTP error body shared by the four services.
package problem

import (
	"encoding/json"
	"net/http"
)

// Problem is an RFC 9457 body. trace_id is the X-Request-Id of the request.
type Problem struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Status  int    `json:"status"`
	Detail  string `json:"detail"`
	TraceID string `json:"trace_id"`
}

// TraceID returns the request's X-Request-Id. An empty header stays an empty trace.
func TraceID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Header.Get("X-Request-Id")
}

// Write sends problem+json. The type is about:blank, the same value as the docs.
func Write(w http.ResponseWriter, status int, title, detail, traceID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:    "about:blank",
		Title:   title,
		Status:  status,
		Detail:  detail,
		TraceID: traceID,
	})
}
