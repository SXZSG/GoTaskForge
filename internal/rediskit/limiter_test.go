package rediskit

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMemoryLimiter(t *testing.T) {
	l := NewMemory()
	if ok, _, _ := l.Allow(context.Background(), "client1", 1); !ok {
		t.Fatal("first must pass")
	}
	if ok, _, _ := l.Allow(context.Background(), "client1", 1); ok {
		t.Fatal("second must fail")
	}
	if ok, _, _ := l.Allow(context.Background(), "client2", 1); !ok {
		t.Fatal("other identity should pass")
	}
}
func TestRedisProtocol(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var hits atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			rd := bufio.NewReader(c)
			line, _ := rd.ReadString('\n')
			if !strings.HasPrefix(line, "*") {
				c.Close()
				return
			}
			n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
			parts := make([]string, n)
			for j := 0; j < n; j++ {
				head, _ := rd.ReadString('\n')
				size, _ := strconv.Atoi(strings.TrimSpace(head[1:]))
				buf := make([]byte, size+2)
				if _, err = io.ReadFull(rd, buf); err != nil {
					break
				}
				parts[j] = string(buf[:size])
			}
			if len(parts) > 0 && parts[0] == "PING" {
				_, _ = c.Write([]byte("+PONG\r\n"))
			} else {
				hits.Add(1)
				_, _ = c.Write([]byte(":1\r\n"))
			}
			c.Close()
		}
	}()
	r, err := NewRedis("redis://" + ln.Addr().String() + "/0")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok, _, err := r.Allow(context.Background(), "a", 2)
	if err != nil || !ok {
		t.Fatalf("allow: %v %v", ok, err)
	}
	<-done
	if hits.Load() != 1 {
		t.Fatal("EVAL did not execute")
	}
}
func TestRedisURL(t *testing.T) {
	for _, bad := range []string{"http://127.0.0.1", "redis://", "redis://host/abc"} {
		if _, err := NewRedis(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
