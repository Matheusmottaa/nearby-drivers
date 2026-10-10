package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteUnprocessable(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, http.StatusUnprocessableEntity, "unprocessable entity", "k must be <= 100", "req-abc123")

	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}

	var got Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := Problem{
		Type:    "about:blank",
		Title:   "unprocessable entity",
		Status:  422,
		Detail:  "k must be <= 100",
		TraceID: "req-abc123",
	}
	if got != want {
		t.Fatalf("problem = %#v, want %#v", got, want)
	}
}

func TestTraceID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := TraceID(req); got != "" {
		t.Fatalf("TraceID() = %q, want empty", got)
	}
	req.Header.Set("X-Request-Id", "req-abc123")
	if got := TraceID(req); got != "req-abc123" {
		t.Fatalf("TraceID() = %q, want req-abc123", got)
	}
	if got := TraceID(nil); got != "" {
		t.Fatalf("TraceID(nil) = %q, want empty", got)
	}
}
