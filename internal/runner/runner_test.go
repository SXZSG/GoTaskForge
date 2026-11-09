package runner

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gotaskforge/internal/provider"
	"gotaskforge/internal/task"
)

type failOnce struct{ calls atomic.Int32 }

func (f *failOnce) Generate(_ context.Context, _ string) (string, error) {
	if f.calls.Add(1) == 1 {
		return "", &provider.ProcessError{Message: "temporary 429", Retryable: true}
	}
	return "done", nil
}

type blockUntilCancelled struct{ started chan struct{} }

func (p blockUntilCancelled) Generate(ctx context.Context, _ string) (string, error) {
	close(p.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func waitStatus(t *testing.T, store *task.Store, id string, want task.Status) task.Job {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := store.Get(id)
		if ok && job.Status == want {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, _ := store.Get(id)
	t.Fatalf("timeout waiting for %s; job=%+v", want, job)
	return task.Job{}
}

func TestRetryThenSucceed(t *testing.T) {
	store, err := task.Open(filepath.Join(t.TempDir(), "jobs.json"), 20)
	if err != nil {
		t.Fatal(err)
	}
	p := &failOnce{}
	r := New(store, p, 2, 3, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Start(context.Background())
	defer r.Shutdown()
	job, _, err := store.Create("hello", "")
	if err != nil {
		t.Fatal(err)
	}
	r.Notify()
	complete := waitStatus(t, store, job.ID, task.Succeeded)
	if complete.Attempts != 2 || complete.Result != "done" {
		t.Fatalf("retry result: %+v", complete)
	}
}

func TestCancelInFlight(t *testing.T) {
	store, err := task.Open(filepath.Join(t.TempDir(), "jobs.json"), 20)
	if err != nil {
		t.Fatal(err)
	}
	p := blockUntilCancelled{started: make(chan struct{})}
	r := New(store, p, 1, 3, 3*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Start(context.Background())
	defer r.Shutdown()
	job, _, err := store.Create("hello", "")
	if err != nil {
		t.Fatal(err)
	}
	r.Notify()
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	if _, err := r.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitStatus(t, store, job.ID, task.Cancelled)
	if !cancelled.CancelRequested {
		t.Fatalf("expected flag: %+v", cancelled)
	}
}
