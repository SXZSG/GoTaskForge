//go:build integration

package rediskit

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRedisLiveRateLimit(t *testing.T) {
	raw := os.Getenv("REDIS_URL")
	if raw == "" {
		t.Fatal("REDIS_URL required for Redis integration test")
	}
	r, err := NewRedis(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	unique := "redis-integration-" + time.Now().Format(time.RFC3339Nano)
	ok, _, err := r.Allow(context.Background(), unique, 1)
	if err != nil || !ok {
		t.Fatalf("first request: %v %v", ok, err)
	}
	ok, _, err = r.Allow(context.Background(), unique, 1)
	if err != nil || ok {
		t.Fatalf("second should be rejected: %v %v", ok, err)
	}
}
