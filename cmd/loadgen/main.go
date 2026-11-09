// loadgen is a small, reproducible HTTP benchmark for a LOCAL demo gateway.
// Results are not generalizable to GPU throughput or a production environment.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type outcome struct {
	latency time.Duration
	code    int
	err     error
}

func main() {
	address := flag.String("url", "http://127.0.0.1:8080", "gateway root URL (default: local only)")
	requests := flag.Int("n", 20, "total requests")
	parallel := flag.Int("c", 4, "max concurrent requests")
	model := flag.String("model", "local-demo", "model name")
	apiKey := flag.String("api-key", "", "X-API-Key for gateway")
	stream := flag.Bool("stream", false, "exercise SSE streaming")
	flag.Parse()
	if *requests < 1 || *requests > 100000 || *parallel < 1 || *parallel > 1000 {
		fmt.Fprintln(os.Stderr, "invalid n or c")
		os.Exit(2)
	}
	body, _ := json.Marshal(map[string]any{"model": *model, "stream": *stream, "messages": []any{map[string]string{"role": "user", "content": "benchmark: describe goroutines"}}})
	client := &http.Client{Timeout: 120 * time.Second}
	sem := make(chan struct{}, *parallel)
	outcomes := make(chan outcome, *requests)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < *requests; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			begin := time.Now()
			req, err := http.NewRequest("POST", *address+"/v1/chat/completions", bytes.NewReader(body))
			if err != nil {
				outcomes <- outcome{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if *apiKey != "" {
				req.Header.Set("X-API-Key", *apiKey)
			}
			resp, err := client.Do(req)
			if err != nil {
				outcomes <- outcome{latency: time.Since(begin), err: err}
				return
			}
			content, copyErr := io.Copy(io.Discard, resp.Body)
			_ = content
			_ = resp.Body.Close()
			outcomes <- outcome{latency: time.Since(begin), code: resp.StatusCode, err: copyErr}
		}()
	}
	wg.Wait()
	close(outcomes)
	elapsed := time.Since(start)
	count := 0
	successes := 0
	failures := 0
	latencies := make([]float64, 0, *requests)
	statusCounts := map[int]int{}
	var firstErr error
	for o := range outcomes {
		count++
		if o.err == nil && o.code == 200 {
			successes++
			latencies = append(latencies, float64(o.latency.Microseconds())/1000)
		} else {
			failures++
			if firstErr == nil {
				firstErr = o.err
			}
		}
		statusCounts[o.code]++
	}
	sort.Float64s(latencies)
	percentile := func(p float64) float64 {
		if len(latencies) == 0 {
			return 0
		}
		i := int(p * float64(len(latencies)-1))
		return latencies[i]
	}
	// Two different throughput definitions are reported on purpose. A gateway
	// that rejects overloaded requests answers 503 immediately, so "completed"
	// includes work the server refused to do. Only success_req_s describes
	// served capacity; quoting the completed rate alone overstates throughput.
	successRate := float64(successes) / elapsed.Seconds()
	completedRate := float64(count) / elapsed.Seconds()
	fmt.Printf("mode=synthetic_or_configured_backend requests=%d concurrency=%d stream=%v elapsed=%s\n", count, *parallel, *stream, elapsed)
	fmt.Printf("success=%d fail=%d status=%v\n", successes, failures, statusCounts)
	fmt.Printf("throughput success_req_s=%.2f completed_req_s=%.2f (completed includes rejected/failed responses)\n", successRate, completedRate)
	fmt.Printf("latency success_only n=%d p50=%.2fms p95=%.2fms p99=%.2fms\n", len(latencies), percentile(.50), percentile(.95), percentile(.99))
	if firstErr != nil {
		fmt.Printf("first_error=%v\n", firstErr)
	}
	fmt.Println("NOTE: gateway-level HTTP benchmark, not token throughput, CUDA performance, or production SLA.")
	if failures > 0 {
		os.Exit(1)
	}
}
