package runner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"gotaskforge/internal/provider"
	"gotaskforge/internal/task"
)

type Runner struct {
	Store       task.Repository
	Provider    provider.Provider
	Workers     int
	Timeout     time.Duration
	MaxAttempts int
	Logger      *slog.Logger

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	wake    chan struct{}
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

func New(s task.Repository, p provider.Provider, workers, maxAttempts int, timeout time.Duration, log *slog.Logger) *Runner {
	return &Runner{Store: s, Provider: p, Workers: workers, MaxAttempts: maxAttempts, Timeout: timeout,
		Logger: log, cancels: make(map[string]context.CancelFunc), wake: make(chan struct{}, 1)}
}

func (r *Runner) Start(parent context.Context) {
	if r.Workers < 1 || r.MaxAttempts < 1 || r.Timeout <= 0 {
		panic("invalid runner configuration")
	}
	ctx, stop := context.WithCancel(parent)
	r.mu.Lock()
	if r.stop != nil {
		r.mu.Unlock()
		panic("runner already started")
	}
	r.stop = stop
	r.mu.Unlock()
	for i := 0; i < r.Workers; i++ {
		r.wg.Add(1)
		go r.worker(ctx)
	}
}

func (r *Runner) Shutdown() {
	r.mu.Lock()
	if r.stop != nil {
		r.stop()
	}
	for _, cancel := range r.cancels {
		cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Runner) Notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) Cancel(id string) (task.Job, error) {
	job, err := r.Store.Cancel(id)
	if err != nil {
		return job, err
	}
	r.mu.Lock()
	cancel := r.cancels[id]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return job, nil
}

func (r *Runner) worker(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job, found, err := r.Store.ReserveDue(time.Now().UTC())
		if err != nil {
			r.Logger.Error("reserve job failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		if !found {
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
			case <-ticker.C:
			}
			continue
		}
		r.execute(ctx, job)
	}
}

func (r *Runner) execute(parent context.Context, job task.Job) {
	ctx, cancel := context.WithTimeout(parent, r.Timeout)
	r.mu.Lock()
	r.cancels[job.ID] = cancel
	r.mu.Unlock()
	// Cross-instance cancellations must reach workers too, not only this
	// process's local cancel map. Polling keeps file mode equally simple.
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			fresh, err := r.Store.Lookup(job.ID)
			if err == nil && fresh.CancelRequested {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	result, err := r.Provider.Generate(ctx, job.Prompt)
	cancel()
	<-monitorDone
	r.mu.Lock()
	delete(r.cancels, job.ID)
	r.mu.Unlock()

	retry := err != nil && (provider.IsRetryable(err) || errors.Is(err, context.DeadlineExceeded) || (errors.Is(err, context.Canceled) && parent.Err() != nil))
	if errors.Is(err, context.Canceled) && parent.Err() == nil {
		retry = false
	}
	delay := time.Second << min(job.Attempts-1, 5)
	finished, finishErr := r.Store.Complete(job.ID, job.Attempts, result, err, retry, r.MaxAttempts, delay)
	if errors.Is(finishErr, task.ErrStaleAttempt) {
		r.Logger.Warn("obsolete worker result discarded", "job_id", job.ID)
	} else if finishErr != nil {
		// Recoverable: on process restart or lease expiration, the job is requeued.
		r.Logger.Error("commit job failed", "job_id", job.ID, "error", finishErr)
	} else {
		r.Logger.Info("job updated", "job_id", job.ID, "status", finished.Status, "attempts", finished.Attempts)
	}
}
