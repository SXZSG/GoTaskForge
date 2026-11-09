// Package mysqlstore implements a durable, multi-worker task repository for
// MySQL 8.0+. A mysql database/sql driver must be registered by the binary.
package mysqlstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gotaskforge/internal/task"
)

//go:embed schema.sql
var schema embed.FS

const fields = `id,prompt,idempotency_key,status,result,error,attempts,next_run_at,created_at,updated_at,cancel_requested`

type Store struct {
	db          *sql.DB
	maxPending  int
	maxAttempts int
	lease       time.Duration
	lastSweep   atomic.Int64
}

// Open requires a registered mysql driver, MYSQL_DSN with parseTime=true,
// and a database created before startup. Tables are created idempotently.
func Open(dsn string, capacity, attempts int, lease time.Duration) (*Store, error) {
	if capacity < 1 || attempts < 1 || lease < time.Second {
		return nil, errors.New("invalid mysql store settings")
	}
	if !strings.Contains(dsn, "parseTime=true") {
		return nil, errors.New("MYSQL_DSN must contain parseTime=true")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("mysql driver missing? compile with -tags mysql: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(15 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql ping: %w", err)
	}
	ddl, err := schema.ReadFile("schema.sql")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, part := range strings.Split(string(ddl), ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, part); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("mysql schema: %w", err)
		}
	}
	return &Store{db: db, maxPending: capacity, maxAttempts: attempts, lease: lease}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
}
func timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func readJob(row interface{ Scan(...any) error }) (task.Job, error) {
	var j task.Job
	var key, result, message sql.NullString
	var status string
	err := row.Scan(&j.ID, &j.Prompt, &key, &status, &result, &message, &j.Attempts, &j.NextRunAt, &j.CreatedAt, &j.UpdatedAt, &j.CancelRequested)
	if err != nil {
		return j, err
	}
	j.Status = task.Status(status)
	j.IdempotencyKey = key.String
	j.Result = result.String
	j.Error = message.String
	return j, nil
}

func (s *Store) Create(prompt, key string) (task.Job, bool, error) {
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Job{}, false, err
	}
	defer tx.Rollback()
	// Serialize only submissions. Idempotency lookup precedes capacity check,
	// so retries of an accepted request work even when the queue is full.
	var gate int
	if err := tx.QueryRowContext(ctx, `SELECT id FROM queue_gate WHERE id=1 FOR UPDATE`).Scan(&gate); err != nil {
		return task.Job{}, false, err
	}
	if key != "" {
		found, err := readJob(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM jobs WHERE idempotency_key=?`, key))
		if err == nil {
			if found.Prompt != prompt {
				return task.Job{}, false, task.ErrConflict
			}
			if err := tx.Commit(); err != nil {
				return task.Job{}, false, err
			}
			return found, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return task.Job{}, false, err
		}
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running')`).Scan(&n); err != nil {
		return task.Job{}, false, err
	}
	if n >= s.maxPending {
		return task.Job{}, false, task.ErrFull
	}
	id, err := task.NewID()
	if err != nil {
		return task.Job{}, false, err
	}
	now := time.Now().UTC()
	j := task.Job{ID: id, Prompt: prompt, IdempotencyKey: key, Status: task.Queued, NextRunAt: now, CreatedAt: now, UpdatedAt: now}
	var nullableKey any
	if key != "" {
		nullableKey = key
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,idempotency_key,status,attempts,next_run_at,created_at,updated_at,cancel_requested) VALUES(?,?,?,'queued',0,?,?,?,FALSE)`, id, prompt, nullableKey, now, now, now)
	if err != nil {
		return task.Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return task.Job{}, false, err
	}
	return j, true, nil
}

func (s *Store) Lookup(id string) (task.Job, error) {
	ctx, cancel := timeout()
	defer cancel()
	j, err := readJob(s.db.QueryRowContext(ctx, `SELECT `+fields+` FROM jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, task.ErrNotFound
	}
	return j, err
}
func (s *Store) Query(status task.Status, limit int) ([]task.Job, error) {
	ctx, cancel := timeout()
	defer cancel()
	query := `SELECT ` + fields + ` FROM jobs`
	args := []any{}
	if status != "" {
		query += ` WHERE status=?`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]task.Job, 0)
	for rows.Next() {
		j, err := readJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}
func (s *Store) Counts() (map[string]int, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT status,COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[string]int{"queued": 0, "running": 0, "succeeded": 0, "failed": 0, "cancelled": 0, "total": 0}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		totals[status] = n
		totals["total"] += n
	}
	return totals, rows.Err()
}

// sweepLost recovers tasks from a killed worker at most once every 5s. Every
// claim has a lease longer than the provider timeout; Complete uses attempt
// fencing, so an expired worker cannot overwrite a later attempt's result.
func (s *Store) sweepLost(ctx context.Context, now time.Time) error {
	stamp := now.UnixNano()
	last := s.lastSweep.Load()
	if last != 0 && stamp-last < int64(5*time.Second) {
		return nil
	}
	if !s.lastSweep.CompareAndSwap(last, stamp) {
		return nil
	}
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE jobs SET status='cancelled',updated_at=?,lease_until=NULL WHERE status='running' AND lease_until<? AND cancel_requested=TRUE`, []any{now, now}},
		{`UPDATE jobs SET status='failed',error='worker lease expired; attempt limit reached',updated_at=?,lease_until=NULL WHERE status='running' AND lease_until<? AND attempts>=?`, []any{now, now, s.maxAttempts}},
		{`UPDATE jobs SET status='queued',next_run_at=?,updated_at=?,lease_until=NULL WHERE status='running' AND lease_until<? AND attempts<? AND cancel_requested=FALSE`, []any{now, now, now, s.maxAttempts}},
	} {
		if _, err := s.db.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReserveDue(now time.Time) (task.Job, bool, error) {
	ctx, cancel := timeout()
	defer cancel()
	if err := s.sweepLost(ctx, now); err != nil {
		return task.Job{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Job{}, false, err
	}
	defer tx.Rollback()
	j, err := readJob(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM jobs WHERE status='queued' AND next_run_at<=? ORDER BY next_run_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Job{}, false, nil
	}
	if err != nil {
		return task.Job{}, false, err
	}
	j.Status = task.Running
	j.Attempts++
	j.UpdatedAt = now
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET status='running',attempts=?,updated_at=?,lease_until=? WHERE id=?`, j.Attempts, now, now.Add(s.lease), j.ID)
	if err != nil {
		return task.Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return task.Job{}, false, err
	}
	return j, true, nil
}

func safeMessage(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	// Do not allow huge upstream response text in database; provider errors
	// contain only sanitized fixed messages.
	if len(s) > 1000 {
		s = s[:1000]
	}
	return s
}

func (s *Store) Complete(id string, attempt int, result string, runErr error, retry bool, maxAttempts int, delay time.Duration) (task.Job, error) {
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Job{}, err
	}
	defer tx.Rollback()
	j, err := readJob(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM jobs WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Job{}, task.ErrNotFound
	}
	if err != nil {
		return task.Job{}, err
	}
	if j.Status != task.Running || j.Attempts != attempt {
		return task.Job{}, task.ErrStaleAttempt
	}
	now := time.Now().UTC()
	j.UpdatedAt = now
	switch {
	case j.CancelRequested:
		j.Status = task.Cancelled
		j.Error = ""
		j.Result = ""
	case runErr == nil:
		j.Status = task.Succeeded
		j.Result = result
		j.Error = ""
	case retry && j.Attempts < maxAttempts:
		j.Status = task.Queued
		j.Error = safeMessage(runErr)
		j.NextRunAt = now.Add(delay)
	default:
		j.Status = task.Failed
		j.Error = safeMessage(runErr)
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET status=?,result=?,error=?,next_run_at=?,updated_at=?,lease_until=NULL WHERE id=?`, string(j.Status), j.Result, j.Error, j.NextRunAt, now, id)
	if err != nil {
		return task.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Job{}, err
	}
	return j, nil
}

func (s *Store) Cancel(id string) (task.Job, error) {
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Job{}, err
	}
	defer tx.Rollback()
	j, err := readJob(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM jobs WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Job{}, task.ErrNotFound
	}
	if err != nil {
		return task.Job{}, err
	}
	switch j.Status {
	case task.Queued:
		j.Status = task.Cancelled
	case task.Running:
		j.CancelRequested = true
	default:
		return task.Job{}, task.ErrTerminal
	}
	j.UpdatedAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET status=?,cancel_requested=?,updated_at=? WHERE id=?`, string(j.Status), j.CancelRequested, j.UpdatedAt, j.ID)
	if err != nil {
		return task.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Job{}, err
	}
	return j, nil
}
