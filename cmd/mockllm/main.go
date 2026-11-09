// mockllm is a deterministic OpenAI-compatible testing upstream.
// It is not a language model; it returns a synthetic response without API cost.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	addr := getenv("MOCK_ADDR", "127.0.0.1:8091")
	label := getenv("MOCK_NAME", "local-mock")
	delay, _ := strconv.Atoi(getenv("MOCK_DELAY_MS", "30"))
	if delay < 0 || delay > 5000 {
		log.Fatal("MOCK_DELAY_MS must be 0–5000")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var p struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Messages) == 0 {
			http.Error(w, "invalid mock request", 400)
			return
		}
		prompt := p.Messages[len(p.Messages)-1].Content
		if len(prompt) > 120 {
			prompt = prompt[:120]
		}
		reply := fmt.Sprintf("[%s] synthetic reply to: %s", label, prompt)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Duration(delay) * time.Millisecond):
		}
		if !p.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-mock", "object": "chat.completion", "model": p.Model,
				"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		for _, word := range strings.SplitAfter(reply, " ") {
			chunk, _ := json.Marshal(map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
				"model": p.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": word}, "finish_reason": nil}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(15 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	log.Printf("mock upstream %s listening on %s", label, addr)
	log.Fatal((&http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}).ListenAndServe())
}
func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
