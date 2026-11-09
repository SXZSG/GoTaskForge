package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotaskforge/internal/provider"
	"gotaskforge/internal/rediskit"
	"gotaskforge/internal/runner"
	"gotaskforge/internal/task"
)

func TestAPIAuthenticationAndRateLimit(t *testing.T) {
	s, err := task.Open(filepath.Join(t.TempDir(), "jobs.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	service := runner.New(s, provider.Mock{Delay: time.Millisecond}, 1, 2, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.Start(context.Background())
	defer service.Shutdown()
	a := &API{Store: s, Runner: service, Limiter: rediskit.NewMemory(), APIKey: "secret", RatePerMinute: 1}
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("want 401: %d", resp.StatusCode)
	}
	resp.Body.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/jobs", strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("X-API-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 202 {
		t.Fatalf("first submission: %d", resp.StatusCode)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("POST", ts.URL+"/v1/jobs", strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("X-API-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
