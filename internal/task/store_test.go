package task

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// removeAllRetry deletes a directory, retrying briefly.
//
// persist() replaces the snapshot with CreateTemp + Rename on every mutation.
// On Windows the replaced directory entry stays in a delete-pending state for
// a moment (Defender/indexers hold a short-lived handle), so a single
// os.RemoveAll can report "The directory is not empty" for a directory that is
// already empty. This is an OS artefact, reproducible with CreateTemp/Rename
// alone; the retry keeps the suite deterministic. The cleanup registered here
// runs before testing's own t.TempDir cleanup, which then finds nothing to do.
func removeAllRetry(t *testing.T, dir string) {
	t.Helper()
	var err error
	for i := 0; i < 100; i++ {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("cleanup %s: %v", dir, err)
}

func testStore(t *testing.T, capacity int) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { removeAllRetry(t, dir) })
	path := filepath.Join(dir, "jobs.json")
	s, err := Open(path, capacity)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestIdempotencyCapacityAndPersistence(t *testing.T) {
	s, path := testStore(t, 1)
	first, created, err := s.Create("hello", "same-request")
	if err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	repeated, created, err := s.Create("hello", "same-request")
	if err != nil || created || repeated.ID != first.ID {
		t.Fatalf("idempotency: %+v %v %v", repeated, created, err)
	}
	if _, _, err = s.Create("different", "same-request"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if _, _, err = s.Create("another", "new-request"); !errors.Is(err, ErrFull) {
		t.Fatalf("want capacity error, got %v", err)
	}
	reopened, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	restored, ok := reopened.Get(first.ID)
	if !ok || restored.Prompt != "hello" || restored.IdempotencyKey != "same-request" {
		t.Fatalf("restored: %+v", restored)
	}
	if _, err := reopened.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.Create("another", "new-request"); err != nil {
		t.Fatalf("cancel should free capacity: %v", err)
	}
}

func TestConcurrentReserveAndRetry(t *testing.T) {
	s, _ := testStore(t, 50)
	for i := 0; i < 20; i++ {
		if _, _, err := s.Create("prompt", ""); err != nil {
			t.Fatal(err)
		}
	}
	ids := make(chan string, 20)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, ok, err := s.ReserveDue(time.Now().Add(time.Second))
				if err != nil {
					t.Errorf("reserve: %v", err)
					return
				}
				if !ok {
					return
				}
				ids <- job.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("double-reserved job %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 20 {
		t.Fatalf("reserved %d, want 20", len(seen))
	}
	for id := range seen {
		job, err := s.Finish(id, "", errors.New("rate limited"), true, 3, time.Second)
		if err != nil || job.Status != Queued || job.Attempts != 1 {
			t.Fatalf("retry: %+v %v", job, err)
		}
		break
	}
}

func TestRecoverInterruptedRunningJob(t *testing.T) {
	s, path := testStore(t, 3)
	job, _, err := s.Create("hi", "")
	if err != nil {
		t.Fatal(err)
	}
	running, ok, err := s.ReserveDue(time.Now().Add(time.Second))
	if err != nil || !ok || running.Attempts != 1 {
		t.Fatalf("reserve failed: %+v %v", running, err)
	}
	recovered, err := Open(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := recovered.Get(job.ID)
	if restored.Status != Queued || restored.Attempts != 1 {
		t.Fatalf("recovery: %+v", restored)
	}
	second, ok, err := recovered.ReserveDue(time.Now().Add(time.Second))
	if err != nil || !ok || second.Attempts != 2 {
		t.Fatalf("retry after restart: %+v %v", second, err)
	}
}

func TestCancelRunningJobWinsOverSuccess(t *testing.T) {
	s, _ := testStore(t, 1)
	job, _, err := s.Create("hi", "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ReserveDue(time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := s.Finish(job.ID, "late success", nil, false, 3, 0)
	if err != nil || finished.Status != Cancelled || finished.Result != "" {
		t.Fatalf("cancel did not win: %+v %v", finished, err)
	}
	if _, err := s.Cancel(job.ID); !errors.Is(err, ErrTerminal) {
		t.Fatalf("want terminal, got %v", err)
	}
}
