// Package rediskit implements a minimal RESP2-based Redis fixed-window rate
// limiter. This deliberately supports only the PING and EVAL operations used
// by this service; it is not a general Redis client.
package rediskit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limiter can reject an API request before it reaches the task repository.
// retry is a duration rounded up to seconds.
type Limiter interface {
	Allow(ctx context.Context, identity string, limit int) (ok bool, retry int, err error)
	Ping(ctx context.Context) error
}

type Memory struct {
	mu       sync.Mutex
	counters map[string]bucket
}
type bucket struct {
	used    int
	expires time.Time
}

func NewMemory() *Memory { return &Memory{counters: map[string]bucket{}} }
func (m *Memory) Allow(_ context.Context, identity string, limit int) (bool, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	window := now.Truncate(time.Minute)
	key := identity + "|" + window.Format(time.RFC3339)
	b := m.counters[key]
	if b.expires.Before(now) {
		b = bucket{expires: window.Add(time.Minute)}
	}
	b.used++
	m.counters[key] = b
	if len(m.counters) > 10000 {
		for k, v := range m.counters {
			if v.expires.Before(now) {
				delete(m.counters, k)
			}
		}
	}
	retry := int(time.Until(b.expires).Seconds()) + 1
	if retry < 1 {
		retry = 1
	}
	return b.used <= limit, retry, nil
}
func (m *Memory) Ping(context.Context) error { return nil }

type Redis struct {
	address, username, password, database string
	secure                                bool
}

func NewRedis(rawURL string) (*Redis, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "redis" && u.Scheme != "rediss" {
		return nil, errors.New("REDIS_URL must start with redis:// or rediss://")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("REDIS_URL missing host")
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	db := "0"
	if path := strings.TrimPrefix(u.Path, "/"); path != "" {
		n, err := strconv.Atoi(path)
		if err != nil || n < 0 || n > 15 {
			return nil, errors.New("REDIS_URL database must be 0..15")
		}
		db = path
	}
	r := &Redis{address: net.JoinHostPort(host, port), database: db, secure: u.Scheme == "rediss"}
	if u.User != nil {
		r.username = u.User.Username()
		r.password, _ = u.User.Password()
	}
	return r, nil
}

func encode(parts []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(parts))
	for _, s := range parts {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(s), s)
	}
	return []byte(b.String())
}
func readResponse(br *bufio.Reader) (string, error) {
	prefix, err := br.ReadByte()
	if err != nil {
		return "", err
	}
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("bad RESP line")
	}
	line = strings.TrimSuffix(line, "\r\n")
	switch prefix {
	case '+', ':':
		return line, nil
	case '-':
		return "", fmt.Errorf("redis error: %s", line)
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil || n > 4096 {
			return "", errors.New("invalid Redis response length")
		}
		if n < 0 {
			return "", errors.New("redis nil response")
		}
		b := make([]byte, n+2)
		if _, err = io.ReadFull(br, b); err != nil {
			return "", err
		}
		if string(b[n:]) != "\r\n" {
			return "", errors.New("bad bulk termination")
		}
		return string(b[:n]), nil
	default:
		return "", errors.New("unsupported RESP response")
	}
}
func (r *Redis) exec(ctx context.Context, command ...string) (string, error) {
	dialer := &net.Dialer{Timeout: 1500 * time.Millisecond}
	var conn net.Conn
	var err error
	if r.secure {
		host, _, _ := net.SplitHostPort(r.address)
		conn, err = tls.DialWithDialer(dialer, "tcp", r.address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", r.address)
	}
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return "", err
	}
	reader := bufio.NewReader(conn)
	send := func(args ...string) (string, error) {
		if _, err := conn.Write(encode(args)); err != nil {
			return "", err
		}
		return readResponse(reader)
	}
	if r.password != "" {
		args := []string{"AUTH"}
		if r.username != "" {
			args = append(args, r.username)
		}
		args = append(args, r.password)
		if _, err = send(args...); err != nil {
			return "", err
		}
	}
	if r.database != "0" {
		if _, err = send("SELECT", r.database); err != nil {
			return "", err
		}
	}
	return send(command...)
}
func (r *Redis) Ping(ctx context.Context) error {
	result, err := r.exec(ctx, "PING")
	if err != nil {
		return err
	}
	if result != "PONG" {
		return errors.New("invalid Redis PING response")
	}
	return nil
}

// Atomic Redis Lua script: count each request and set a short TTL on first hit.
// Fixed window provides an easy-to-explain quota, not a sliding-window SLA.
const rateScript = `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`

func (r *Redis) Allow(ctx context.Context, identity string, limit int) (bool, int, error) {
	minute := time.Now().UTC().Unix() / 60
	hash := sha256.Sum256([]byte(identity))
	key := "taskforge:rate:" + hex.EncodeToString(hash[:12]) + ":" + strconv.FormatInt(minute, 10)
	v, err := r.exec(ctx, "EVAL", rateScript, "1", key)
	if err != nil {
		return false, 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return false, 0, fmt.Errorf("redis invalid counter: %w", err)
	}
	seconds := 60 - int(time.Now().UTC().Unix()%60)
	if seconds < 1 {
		seconds = 1
	}
	return n <= limit, seconds, nil
}
