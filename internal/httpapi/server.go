package httpapi

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gotaskforge/internal/gateway"
	"gotaskforge/internal/rediskit"
	"gotaskforge/internal/runner"
	"gotaskforge/internal/task"
)

//go:embed web/index.html
var dashboard embed.FS

type API struct {
	Store         task.Repository
	Runner        *runner.Runner
	Limiter       rediskit.Limiter
	APIKey        string
	RatePerMinute int
	Gateway       *gateway.Gateway
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.Ping(); err != nil {
			fail(w, 503, "storage unavailable")
			return
		}
		if a.Limiter != nil {
			if err := a.Limiter.Ping(r.Context()); err != nil {
				fail(w, 503, "rate limiter unavailable")
				return
			}
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		raw, err := dashboard.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "dashboard unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		stats, err := a.Store.Counts()
		if err != nil {
			fail(w, 500, "storage unavailable")
			return
		}
		write(w, 200, map[string]any{"jobs": stats})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if a.Gateway == nil {
			fail(w, 503, "inference gateway not configured")
			return
		}
		a.Gateway.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /v1/gateway/backends", func(w http.ResponseWriter, r *http.Request) {
		if a.Gateway == nil {
			fail(w, 503, "inference gateway not configured")
			return
		}
		write(w, 200, map[string]any{"backends": a.Gateway.Snapshot()})
	})
	mux.HandleFunc("GET /metrics/prometheus", func(w http.ResponseWriter, r *http.Request) {
		if a.Gateway == nil {
			fail(w, 503, "inference gateway not configured")
			return
		}
		a.Gateway.Prometheus(w)
	})
	mux.HandleFunc("POST /v1/jobs", a.create)
	mux.HandleFunc("GET /v1/jobs", a.list)
	mux.HandleFunc("GET /v1/jobs/{id}", a.get)
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", a.cancel)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		// The dashboard is public HTML only; data endpoints are protected.
		if r.URL.Path != "/" && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			if a.APIKey != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(a.APIKey)) != 1 {
				fail(w, 401, "invalid API key")
				return
			}
			limiter := a.Limiter
			if limiter != nil {
				ip, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					ip = r.RemoteAddr
				}
				identity := fmt.Sprintf("%s|%s", a.APIKey, ip)
				cap := a.RatePerMinute
				if cap < 1 {
					cap = 60
				}
				ok, retry, err := limiter.Allow(r.Context(), identity, cap)
				if err != nil {
					fail(w, 503, "rate limiter unavailable")
					return
				}
				if !ok {
					w.Header().Set("Retry-After", strconv.Itoa(retry))
					fail(w, 429, "rate limit exceeded")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, msg string) {
	write(w, code, map[string]string{"error": msg})
}
func exposed(j task.Job) task.Job { j.IdempotencyKey = ""; return j }
func (a *API) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var input struct {
		Prompt string `json:"prompt"`
	}
	if err := dec.Decode(&input); err != nil {
		fail(w, 400, "invalid JSON body (max 16 KiB)")
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		fail(w, 400, "only one JSON object is allowed")
		return
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" || !utf8.ValidString(prompt) || utf8.RuneCountInString(prompt) > 4000 {
		fail(w, 400, "prompt must be 1–4000 characters")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 || strings.ContainsAny(key, "\r\n") {
		fail(w, 400, "invalid Idempotency-Key")
		return
	}
	j, created, err := a.Store.Create(prompt, key)
	switch {
	case errors.Is(err, task.ErrFull):
		fail(w, 503, "job queue full")
		return
	case errors.Is(err, task.ErrConflict):
		fail(w, 409, err.Error())
		return
	case err != nil:
		fail(w, 500, "could not persist job")
		return
	}
	if created {
		a.Runner.Notify()
	}
	code := 202
	if !created {
		code = 200
	}
	w.Header().Set("Location", "/v1/jobs/"+j.ID)
	write(w, code, exposed(j))
}
func (a *API) get(w http.ResponseWriter, r *http.Request) {
	j, err := a.Store.Lookup(r.PathValue("id"))
	if errors.Is(err, task.ErrNotFound) {
		fail(w, 404, "job not found")
		return
	}
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	write(w, 200, exposed(j))
}
func (a *API) list(w http.ResponseWriter, r *http.Request) {
	status := task.Status(r.URL.Query().Get("status"))
	switch status {
	case "", task.Queued, task.Running, task.Succeeded, task.Failed, task.Cancelled:
	default:
		fail(w, 400, "invalid status filter")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			fail(w, 400, "limit must be 1–100")
			return
		}
		limit = n
	}
	jobs, err := a.Store.Query(status, limit)
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	for i := range jobs {
		jobs[i] = exposed(jobs[i])
	}
	write(w, 200, map[string]any{"jobs": jobs, "count": len(jobs)})
}
func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	j, err := a.Runner.Cancel(r.PathValue("id"))
	switch {
	case errors.Is(err, task.ErrNotFound):
		fail(w, 404, err.Error())
		return
	case errors.Is(err, task.ErrTerminal):
		fail(w, 409, err.Error())
		return
	case err != nil:
		fail(w, 500, "could not cancel job")
		return
	}
	write(w, 200, exposed(j))
}
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 130 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
}
