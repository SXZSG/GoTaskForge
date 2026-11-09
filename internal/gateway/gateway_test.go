package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const chatBody = `{"model":"local-demo","messages":[{"role":"user","content":"hello"}]}`

func setupGateway(t *testing.T, urls []string, max, threshold int, timeout, cooldown time.Duration) *Gateway {
	t.Helper()
	g, err := New(Config{Backends: urls, MaxPerBackend: max, FailureThreshold: threshold,
		Timeout: timeout, Cooldown: cooldown, UpstreamKey: "upstream-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func post(t *testing.T, address, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, address+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer user-secret")
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func TestValidationAndBackendAuthentication(t *testing.T) {
	if _, err := New(Config{Backends: []string{"http://remote.example/v1"}, MaxPerBackend: 1, Timeout: time.Second, FailureThreshold: 1, Cooldown: time.Second}); err == nil {
		t.Fatal("nonlocal insecure upstream accepted")
	}
	if _, err := New(Config{Backends: []string{"http://localhost:1/v1", "http://localhost:1/v1"}, MaxPerBackend: 1, Timeout: time.Second, FailureThreshold: 1, Cooldown: time.Second}); err == nil {
		t.Fatal("duplicate upstream accepted")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("upstream header/path invalid: %s, %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	g := setupGateway(t, []string{upstream.URL + "/v1"}, 2, 2, time.Second, time.Second)
	server := httptest.NewServer(g)
	defer server.Close()
	response := post(t, server.URL, chatBody)
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(raw, []byte("ok")) {
		t.Fatalf("response: %d %s", response.StatusCode, raw)
	}
	stats := g.Snapshot()
	if stats[0].Total != 1 || stats[0].Succeeded != 1 || stats[0].P95LatencyMS < 0 {
		t.Fatalf("stats: %+v", stats)
	}
	rec := httptest.NewRecorder()
	g.Prometheus(rec)
	if !strings.Contains(rec.Body.String(), `gotaskforge_gateway_requests_total{backend="backend-0"} 1`) {
		t.Fatal(rec.Body.String())
	}
}
func TestLoadBalanceAndBoundedAdmission(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	makeUpstream := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- name
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}))
	}
	a, b := makeUpstream("a"), makeUpstream("b")
	defer a.Close()
	defer b.Close()
	g := setupGateway(t, []string{a.URL + "/v1", b.URL + "/v1"}, 1, 3, 3*time.Second, time.Second)
	server := httptest.NewServer(g)
	defer server.Close()
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(chatBody))
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			statuses <- res.StatusCode
			_ = res.Body.Close()
		}()
	}
	one, two := <-started, <-started
	if one == two {
		t.Fatalf("requests both went to %s", one)
	}
	full := post(t, server.URL, chatBody)
	_ = full.Body.Close()
	if full.StatusCode != 503 {
		t.Fatalf("expected full -> 503, got %d", full.StatusCode)
	}
	close(release)
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Fatalf("request returned %d", status)
		}
	}
	stats := g.Snapshot()
	if stats[0].InFlight != 0 || stats[1].InFlight != 0 || stats[0].Succeeded != 1 || stats[1].Succeeded != 1 {
		t.Fatalf("unexpected final stats %+v", stats)
	}
}
func TestCircuitBreakerCooldown(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	g := setupGateway(t, []string{upstream.URL + "/v1"}, 1, 1, time.Second, 70*time.Millisecond)
	srv := httptest.NewServer(g)
	defer srv.Close()
	first := post(t, srv.URL, chatBody)
	_ = first.Body.Close()
	if first.StatusCode != 503 || !g.Snapshot()[0].CircuitOpen {
		t.Fatal("first upstream failure must open breaker")
	}
	second := post(t, srv.URL, chatBody)
	_ = second.Body.Close()
	if second.StatusCode != 503 || hits.Load() != 1 {
		t.Fatalf("circuit not blocking %d calls", hits.Load())
	}
	time.Sleep(95 * time.Millisecond)
	third := post(t, srv.URL, chatBody)
	_ = third.Body.Close()
	if third.StatusCode != 200 || hits.Load() != 2 {
		t.Fatalf("circuit did not recover: %d %d", third.StatusCode, hits.Load())
	}
}
func TestSSEIsActuallyFlushed(t *testing.T) {
	upstreamStarted := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		if !p.Stream {
			t.Error("stream flag missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
		close(upstreamStarted)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	g := setupGateway(t, []string{upstream.URL + "/v1"}, 1, 2, 3*time.Second, time.Second)
	server := httptest.NewServer(g)
	defer server.Close()
	resp := post(t, server.URL, `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer resp.Body.Close()
	<-upstreamStarted
	received := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(resp.Body).ReadString('\n'); received <- line }()
	select {
	case line := <-received:
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("bad stream %q", line)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first SSE chunk not flushed before upstream completed")
	}
	close(release)
}
func TestTimeoutAndInputValidation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	g := setupGateway(t, []string{upstream.URL + "/v1"}, 1, 1, 35*time.Millisecond, time.Second)
	srv := httptest.NewServer(g)
	defer srv.Close()
	invalid := post(t, srv.URL, `{"model":"m","messages":[],"stream":true}`)
	_ = invalid.Body.Close()
	if invalid.StatusCode != 400 {
		t.Fatalf("invalid request: %d", invalid.StatusCode)
	}
	tooLarge := post(t, srv.URL, `{"model":"m","messages":["`+strings.Repeat("x", maxBodyBytes)+`"]}`)
	_ = tooLarge.Body.Close()
	if tooLarge.StatusCode != 413 {
		t.Fatalf("large request: %d", tooLarge.StatusCode)
	}
	res := post(t, srv.URL, chatBody)
	_ = res.Body.Close()
	if res.StatusCode != 504 {
		t.Fatalf("expected timeout, got %d", res.StatusCode)
	}
	if g.Snapshot()[0].Failed != 1 || !g.Snapshot()[0].CircuitOpen {
		t.Fatal("timeout should trip circuit")
	}
}
func TestCancelledRequestReleasesCapacity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	g := setupGateway(t, []string{upstream.URL + "/v1"}, 1, 1, time.Second, time.Second)
	srv := httptest.NewServer(g)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(chatBody))
	done := make(chan struct{})
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = res.Body.Close()
		}
		close(done)
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && g.Snapshot()[0].InFlight == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && g.Snapshot()[0].InFlight != 0 {
		time.Sleep(time.Millisecond)
	}
	if g.Snapshot()[0].InFlight != 0 || g.Snapshot()[0].CircuitOpen {
		t.Fatalf("cancellation incorrectly holds capacity or trips breaker: %+v", g.Snapshot())
	}
}
