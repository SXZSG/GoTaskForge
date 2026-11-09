package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gotaskforge/internal/gateway"
	"gotaskforge/internal/provider"
	"gotaskforge/internal/runner"
	"gotaskforge/internal/task"
)

func TestJobLifecycleHTTP(t *testing.T) {
	store, err := task.Open(filepath.Join(t.TempDir(), "jobs.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	r := runner.New(store, provider.Mock{Delay: time.Millisecond}, 1, 3, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	api := &API{Store: store, Runner: r}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	// Test with runner stopped: the test can make deterministic queued assertions.
	client := server.Client()
	body := []byte(`{"prompt":"Explain goroutines"}`)
	makeReq := func(key string) (*http.Response, task.Job) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/jobs", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Idempotency-Key", key)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var job task.Job
		if err := json.NewDecoder(res.Body).Decode(&job); err != nil {
			t.Fatal(err)
		}
		return res, job
	}
	first, job := makeReq("one-key")
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", first.StatusCode)
	}
	repeated, same := makeReq("one-key")
	if repeated.StatusCode != http.StatusOK || same.ID != job.ID {
		t.Fatalf("idempotent HTTP: %d, %+v", repeated.StatusCode, same)
	}
	res, err := client.Get(server.URL + "/v1/jobs/" + job.ID)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatal(res.StatusCode)
	}
	cancel, err := client.Post(server.URL+"/v1/jobs/"+job.ID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel.Body.Close()
	if cancel.StatusCode != http.StatusOK {
		t.Fatalf("cancel HTTP %d", cancel.StatusCode)
	}
	list, err := client.Get(server.URL + "/v1/jobs?status=cancelled")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	var result struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(list.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Fatalf("unexpected list: %+v", result)
	}
	bad, err := client.Post(server.URL+"/v1/jobs", "application/json", bytes.NewBufferString(`{"prompt":""}`))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad prompt returned %d", bad.StatusCode)
	}
}

func TestGatewayRouteIsProtectedBySameAPIKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"local"}}]}`))
	}))
	defer upstream.Close()
	gw, err := gateway.New(gateway.Config{Backends: []string{upstream.URL + "/v1"}, MaxPerBackend: 1,
		Timeout: time.Second, FailureThreshold: 2, Cooldown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store, err := task.Open(filepath.Join(t.TempDir(), "jobs.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := &API{Store: store, APIKey: "test-only-secret", Gateway: gw, RatePerMinute: 100}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", res.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-API-Key", "test-only-secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/gateway/backends", nil)
	req.Header.Set("X-API-Key", "test-only-secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Backends []gateway.BackendStats `json:"backends"`
	}
	err = json.NewDecoder(res.Body).Decode(&st)
	_ = res.Body.Close()
	if err != nil || len(st.Backends) != 1 || st.Backends[0].Succeeded != 1 {
		t.Fatalf("gateway stats: %+v %v", st, err)
	}
}
