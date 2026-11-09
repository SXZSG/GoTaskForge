package task

import (
	"errors"
	"time"
)

// Repository abstracts the durable job state machine. ReserveDue is an atomic
// claim, and Complete fences stale workers using the attempt number.
type Repository interface {
	Create(prompt, key string) (Job, bool, error)
	Lookup(id string) (Job, error)
	Query(status Status, limit int) ([]Job, error)
	Counts() (map[string]int, error)
	ReserveDue(now time.Time) (Job, bool, error)
	Complete(id string, attempt int, result string, runErr error, retry bool, maxAttempts int, delay time.Duration) (Job, error)
	Cancel(id string) (Job, error)
	Ping() error
	Close() error
}

var ErrStaleAttempt = errors.New("stale worker attempt")

func (s *Store) Lookup(id string) (Job, error) {
	j, ok := s.Get(id)
	if !ok {
		return Job{}, ErrNotFound
	}
	return j, nil
}

func (s *Store) Query(status Status, limit int) ([]Job, error) { return s.List(status, limit), nil }
func (s *Store) Counts() (map[string]int, error)               { return s.Stats(), nil }
func (s *Store) Ping() error                                   { return nil }
func (s *Store) Close() error                                  { return nil }

func (s *Store) Complete(id string, attempt int, result string, runErr error, retry bool, maxAttempts int, delay time.Duration) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	if job.Status != Running || job.Attempts != attempt {
		return Job{}, ErrStaleAttempt
	}
	return s.finishLocked(id, result, runErr, retry, maxAttempts, delay)
}
