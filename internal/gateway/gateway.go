// Package gateway routes OpenAI-compatible chat requests to a small, fixed pool
// of model servers. This is an inference *gateway*, not a GPU inference engine.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxBodyBytes = 1 << 20
const sampleCount = 256

type Config struct {
	Backends         []string
	UpstreamKey      string
	MaxPerBackend    int
	Timeout          time.Duration
	FailureThreshold int
	Cooldown         time.Duration
	AllowHTTP        bool // explicit opt-in for development / private network HTTP
	Client           *http.Client
}

type backend struct {
	endpoint                         string
	inflight                         int
	total, success, failed, rejected uint64
	consecutive                      int
	openUntil                        time.Time
	latencies                        [sampleCount]float64
	samples                          int
	sampleNext                       int
	bytesOut                         uint64
}

type Gateway struct {
	mu               sync.Mutex
	nodes            []backend
	key              string
	maxPerBackend    int
	timeout          time.Duration
	failureThreshold int
	cooldown         time.Duration
	cursor           int
	client           *http.Client
}

type BackendStats struct {
	ID            string  `json:"id"`
	InFlight      int     `json:"in_flight"`
	Available     bool    `json:"available"`
	CircuitOpen   bool    `json:"circuit_open"`
	Total         uint64  `json:"total"`
	Succeeded     uint64  `json:"succeeded"`
	Failed        uint64  `json:"failed"`
	Rejected      uint64  `json:"rejected"`
	ResponseBytes uint64  `json:"response_bytes"`
	P50LatencyMS  float64 `json:"p50_latency_ms"`
	P95LatencyMS  float64 `json:"p95_latency_ms"`
}

func New(cfg Config) (*Gateway, error) {
	if len(cfg.Backends) == 0 || len(cfg.Backends) > 16 {
		return nil, errors.New("GATEWAY_BACKENDS must contain 1–16 upstreams")
	}
	if cfg.MaxPerBackend < 1 || cfg.MaxPerBackend > 10000 || cfg.Timeout <= 0 || cfg.FailureThreshold < 1 || cfg.Cooldown <= 0 {
		return nil, errors.New("invalid gateway concurrency, timeout, failure threshold, or cooldown")
	}
	g := &Gateway{key: cfg.UpstreamKey, maxPerBackend: cfg.MaxPerBackend,
		timeout: cfg.Timeout, failureThreshold: cfg.FailureThreshold, cooldown: cfg.Cooldown,
		client: cfg.Client}
	if g.client == nil {
		// A per-request context sets the deadline; never follow an upstream redirect
		// (which could leak the upstream authorization header to another host).
		g.client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 32, IdleConnTimeout: 45 * time.Second,
				ResponseHeaderTimeout: cfg.Timeout, ForceAttemptHTTP2: true}}
	}
	unique := make(map[string]bool)
	for _, raw := range cfg.Backends {
		raw = strings.TrimSpace(raw)
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("invalid gateway upstream URL: %q", raw)
		}
		if u.Scheme == "http" && !cfg.AllowHTTP {
			switch u.Hostname() {
			case "localhost", "127.0.0.1", "::1":
			default:
				return nil, errors.New("non-local HTTP upstream requires GATEWAY_ALLOW_HTTP=true")
			}
		}
		endpoint := strings.TrimRight(raw, "/") + "/chat/completions"
		if unique[endpoint] {
			return nil, errors.New("duplicate gateway upstream URL")
		}
		unique[endpoint] = true
		g.nodes = append(g.nodes, backend{endpoint: endpoint})
	}
	return g, nil
}

// reserve uses least-in-flight with a rotating tie-breaker. An open circuit and
// a full concurrency budget both prevent admission. No speculative retry is
// made after forwarding a non-idempotent generation request.
func (g *Gateway) reserve() (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	chosen := -1
	for j := 0; j < len(g.nodes); j++ {
		i := (g.cursor + j) % len(g.nodes)
		n := &g.nodes[i]
		if n.inflight >= g.maxPerBackend || now.Before(n.openUntil) {
			continue
		}
		if chosen < 0 || n.inflight < g.nodes[chosen].inflight {
			chosen = i
		}
	}
	if chosen < 0 {
		for i := range g.nodes {
			g.nodes[i].rejected++
		}
		return 0, false
	}
	g.nodes[chosen].inflight++
	g.nodes[chosen].total++
	g.cursor = (chosen + 1) % len(g.nodes)
	return chosen, true
}

func (g *Gateway) finish(idx int, status int, latency time.Duration, responseBytes uint64, unhealthy bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := &g.nodes[idx]
	n.inflight--
	n.bytesOut += responseBytes
	if status >= 200 && status < 300 && !unhealthy {
		n.success++
	} else {
		n.failed++
	}
	if unhealthy {
		n.consecutive++
		if n.consecutive >= g.failureThreshold {
			n.openUntil = time.Now().Add(g.cooldown)
			n.consecutive = 0
		}
	} else {
		n.consecutive = 0
	}
	ms := float64(latency.Microseconds()) / 1000
	n.latencies[n.sampleNext] = ms
	n.sampleNext = (n.sampleNext + 1) % sampleCount
	if n.samples < sampleCount {
		n.samples++
	}
}

func percentile(n *backend, p float64) float64 {
	if n.samples == 0 {
		return 0
	}
	values := make([]float64, n.samples)
	copy(values, n.latencies[:n.samples])
	sort.Float64s(values)
	pos := int(math.Ceil(p*float64(n.samples))) - 1
	if pos < 0 {
		pos = 0
	}
	return math.Round(values[pos]*100) / 100
}

func (g *Gateway) Snapshot() []BackendStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]BackendStats, 0, len(g.nodes))
	for i := range g.nodes {
		n := &g.nodes[i]
		opened := time.Now().Before(n.openUntil)
		out = append(out, BackendStats{ID: fmt.Sprintf("backend-%d", i), InFlight: n.inflight,
			Available: !opened && n.inflight < g.maxPerBackend, CircuitOpen: opened,
			Total: n.total, Succeeded: n.success, Failed: n.failed, Rejected: n.rejected,
			ResponseBytes: n.bytesOut, P50LatencyMS: percentile(n, .5), P95LatencyMS: percentile(n, .95)})
	}
	return out
}

func sendJSON(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": "gateway_error"}})
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sendJSON(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		sendJSON(w, http.StatusRequestEntityTooLarge, "invalid or oversized request (max 1 MiB)")
		return
	}
	var payload struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
		Stream   bool              `json:"stream"`
	}
	if json.Unmarshal(raw, &payload) != nil || strings.TrimSpace(payload.Model) == "" || len(payload.Messages) == 0 {
		sendJSON(w, http.StatusBadRequest, "model and messages are required")
		return
	}
	index, ok := g.reserve()
	if !ok {
		w.Header().Set("Retry-After", "1")
		sendJSON(w, http.StatusServiceUnavailable, "all inference backends busy or temporarily unhealthy")
		return
	}
	start := time.Now()
	code, unhealthy, out := g.forward(w, r, g.nodes[index].endpoint, raw, payload.Stream)
	// Client cancellations do not signal an upstream outage.
	if r.Context().Err() != nil {
		unhealthy = false
		code = 499 // client closed request: accounted as failed, not backend unhealthy
	}
	g.finish(index, code, time.Since(start), out, unhealthy)
}

func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, endpoint string, raw []byte, stream bool) (int, bool, uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), g.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		sendJSON(w, 502, "upstream request construction failed")
		return 502, false, 0
	}
	req.Header.Set("Content-Type", "application/json")
	if g.key != "" {
		req.Header.Set("Authorization", "Bearer "+g.key)
	}
	response, err := g.client.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			sendJSON(w, http.StatusGatewayTimeout, "upstream request timed out")
			return 504, true, 0
		}
		if r.Context().Err() != nil {
			return 499, false, 0
		}
		sendJSON(w, 502, "upstream request failed")
		return 502, true, 0
	}
	defer response.Body.Close()
	code := response.StatusCode
	unhealthy := code == 429 || code >= 500
	if code < 200 || code >= 300 {
		// Do not forward provider error payloads: these may contain raw prompts
		// or internal upstream information. Preserve standard status codes.
		if code == 429 {
			w.Header().Set("Retry-After", "1")
		}
		sendJSON(w, code, fmt.Sprintf("upstream returned HTTP %d", code))
		return code, unhealthy, 0
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		if stream {
			contentType = "text/event-stream; charset=utf-8"
		} else {
			contentType = "application/json"
		}
	}
	// For a non-streamed response, buffer up to the documented response cap
	// before writing status 200. Never silently return truncated JSON as success.
	if !stream {
		const maxResponse = 8 << 20
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
		if readErr != nil || len(data) > maxResponse {
			sendJSON(w, 502, "invalid or oversized upstream response")
			return 502, true, 0
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(code)
		_, _ = w.Write(data)
		return code, false, uint64(len(data)) // client disconnect is accounted by caller
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(code)
	var total uint64
	if stream {
		flusher, canFlush := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, readErr := response.Body.Read(buf)
			if n > 0 {
				if _, writeErr := w.Write(buf[:n]); writeErr != nil {
					return code, false, total
				}
				total += uint64(n)
				if canFlush {
					flusher.Flush()
				}
			}
			if readErr == io.EOF {
				return code, false, total
			}
			if readErr != nil {
				return code, true, total
			}
		}
	}
	return code, false, total
}

// Prometheus implements a dependency-free text exposition with stable label IDs.
// Only request-level measurements are exposed; no fake GPU/token metrics.
func (g *Gateway) Prometheus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	stats := g.Snapshot()
	for _, stat := range stats {
		label := fmt.Sprintf("{backend=%q}", stat.ID)
		fmt.Fprintf(w, "gotaskforge_gateway_requests_total%s %d\n", label, stat.Total)
		fmt.Fprintf(w, "gotaskforge_gateway_requests_succeeded_total%s %d\n", label, stat.Succeeded)
		fmt.Fprintf(w, "gotaskforge_gateway_requests_failed_total%s %d\n", label, stat.Failed)
		fmt.Fprintf(w, "gotaskforge_gateway_admission_rejections_total%s %d\n", label, stat.Rejected)
		fmt.Fprintf(w, "gotaskforge_gateway_inflight%s %d\n", label, stat.InFlight)
		fmt.Fprintf(w, "gotaskforge_gateway_circuit_open%s %d\n", label, boolInt(stat.CircuitOpen))
		fmt.Fprintf(w, "gotaskforge_gateway_response_bytes_total%s %d\n", label, stat.ResponseBytes)
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
