package task

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

var (
	ErrNotFound = errors.New("job not found")
	ErrConflict = errors.New("idempotency key already used with different prompt")
	ErrFull     = errors.New("pending job capacity reached")
	ErrTerminal = errors.New("job is already finished")
)

type Job struct {
	ID              string    `json:"id"`
	Prompt          string    `json:"prompt"`
	IdempotencyKey  string    `json:"-"`
	Status          Status    `json:"status"`
	Result          string    `json:"result,omitempty"`
	Error           string    `json:"error,omitempty"`
	Attempts        int       `json:"attempts"`
	NextRunAt       time.Time `json:"next_run_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	CancelRequested bool      `json:"cancel_requested,omitempty"`
}

type storedJob struct {
	Job
	Key string `json:"idempotency_key,omitempty"`
}

type snapshot struct {
	Version int         `json:"version"`
	Jobs    []storedJob `json:"jobs"`
}

// Store is a small, crash-recoverable single-process store. Each mutation is
// atomically snapshotted to a local file. It is NOT a distributed queue.
type Store struct {
	mu         sync.Mutex
	jobs       map[string]Job
	path       string
	maxPending int
}

func Open(path string, maxPending int) (*Store, error) {
	if maxPending < 1 {
		return nil, errors.New("maxPending must be positive")
	}
	s := &Store{jobs: map[string]Job{}, path: path, maxPending: maxPending}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		var snap snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, fmt.Errorf("read snapshot: %w", err)
		}
		if snap.Version != 1 {
			return nil, fmt.Errorf("unsupported snapshot version: %d", snap.Version)
		}
		for _, j := range snap.Jobs {
			if j.ID == "" {
				return nil, errors.New("snapshot contains empty job ID")
			}
			if _, exists := s.jobs[j.ID]; exists {
				return nil, errors.New("duplicate job ID")
			}
			j.IdempotencyKey = j.Key
			s.jobs[j.ID] = j.Job
		}
	}
	// After crash/restart, in-flight jobs must be eligible for retry. This
	// yields at-least-once, not exactly-once, external provider execution.
	changed := false
	for id, job := range s.jobs {
		if job.Status == Running {
			if job.CancelRequested {
				job.Status = Cancelled
			} else {
				job.Status = Queued
				job.NextRunAt = time.Now().UTC()
			}
			job.UpdatedAt = time.Now().UTC()
			s.jobs[id] = job
			changed = true
		}
	}
	if changed {
		if err := s.persist(s.jobs); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// edit clones the map, writes the new snapshot, then swaps it in memory.
// A failed filesystem write never changes the committed in-memory state.
func (s *Store) edit(fn func(map[string]Job) error) error {
	next := make(map[string]Job, len(s.jobs)+1)
	for k, v := range s.jobs {
		next[k] = v
	}
	if err := fn(next); err != nil {
		return err
	}
	if err := s.persist(next); err != nil {
		return err
	}
	s.jobs = next
	return nil
}

func (s *Store) persist(jobs map[string]Job) error {
	snap := snapshot{Version: 1, Jobs: make([]storedJob, 0, len(jobs))}
	for _, job := range jobs {
		snap.Jobs = append(snap.Jobs, storedJob{Job: job, Key: job.IdempotencyKey})
	}
	sort.Slice(snap.Jobs, func(i, j int) bool { return snap.Jobs[i].ID < snap.Jobs[j].ID })
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".tasks-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Create(prompt, key string) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		for _, job := range s.jobs {
			if job.IdempotencyKey == key {
				if job.Prompt != prompt {
					return Job{}, false, ErrConflict
				}
				return job, false, nil
			}
		}
	}
	pending := 0
	for _, job := range s.jobs {
		if job.Status == Queued || job.Status == Running {
			pending++
		}
	}
	if pending >= s.maxPending {
		return Job{}, false, ErrFull
	}
	id, err := NewID()
	if err != nil {
		return Job{}, false, err
	}
	now := time.Now().UTC()
	job := Job{ID: id, Prompt: prompt, IdempotencyKey: key, Status: Queued, CreatedAt: now, UpdatedAt: now, NextRunAt: now}
	if err := s.edit(func(m map[string]Job) error { m[id] = job; return nil }); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (s *Store) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *Store) List(status Status, limit int) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		if status == "" || job.Status == status {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ReserveDue atomically changes exactly one due job from queued to running.
func (s *Store) ReserveDue(now time.Time) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var candidate Job
	found := false
	for _, j := range s.jobs {
		if j.Status != Queued || j.NextRunAt.After(now) {
			continue
		}
		if !found || j.NextRunAt.Before(candidate.NextRunAt) ||
			(j.NextRunAt.Equal(candidate.NextRunAt) && j.ID < candidate.ID) {
			candidate, found = j, true
		}
	}
	if !found {
		return Job{}, false, nil
	}
	candidate.Status = Running
	candidate.Attempts++
	candidate.UpdatedAt = now
	if err := s.edit(func(m map[string]Job) error { m[candidate.ID] = candidate; return nil }); err != nil {
		return Job{}, false, err
	}
	return candidate, true, nil
}

func (s *Store) Finish(id, result string, runErr error, retry bool, maxAttempts int, delay time.Duration) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishLocked(id, result, runErr, retry, maxAttempts, delay)
}

func (s *Store) finishLocked(id, result string, runErr error, retry bool, maxAttempts int, delay time.Duration) (Job, error) {
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	if job.Status != Running {
		return Job{}, ErrTerminal
	}
	now := time.Now().UTC()
	job.UpdatedAt = now
	switch {
	case job.CancelRequested:
		job.Status = Cancelled
		job.Error, job.Result = "", ""
	case runErr == nil:
		job.Status = Succeeded
		job.Result, job.Error = result, ""
	case retry && job.Attempts < maxAttempts:
		job.Status = Queued
		job.Error = runErr.Error()
		job.NextRunAt = now.Add(delay)
	default:
		job.Status = Failed
		job.Error = runErr.Error()
	}
	if err := s.edit(func(m map[string]Job) error { m[id] = job; return nil }); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Store) Cancel(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	switch job.Status {
	case Queued:
		job.Status = Cancelled
	case Running:
		job.CancelRequested = true
	default:
		return Job{}, ErrTerminal
	}
	job.UpdatedAt = time.Now().UTC()
	if err := s.edit(func(m map[string]Job) error { m[id] = job; return nil }); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Store) Stats() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{"queued": 0, "running": 0, "succeeded": 0, "failed": 0, "cancelled": 0, "total": 0}
	for _, job := range s.jobs {
		out[string(job.Status)]++
		out["total"]++
	}
	return out
}
