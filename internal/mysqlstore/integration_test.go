//go:build integration && mysql

package mysqlstore

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"gotaskforge/internal/task"
)

func testDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Fatal("MYSQL_DSN required for integration tests")
	}
	s, err := Open(dsn, 100000, 3, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func TestMySQLIdempotencyAndLifecycle(t *testing.T) {
	s := testDB(t)
	key := "mysql-it-" + time.Now().Format("20060102150405.000000000")
	j, created, err := s.Create("a test prompt", key)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	t.Cleanup(func() { _, _ = s.db.Exec("DELETE FROM jobs WHERE id=?", j.ID) })
	again, created, err := s.Create("a test prompt", key)
	if err != nil || created || again.ID != j.ID {
		t.Fatalf("idempotency: %+v %v %v", again, created, err)
	}
	if _, _, err := s.Create("other prompt", key); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("want conflict: %v", err)
	}
	reserved, found, err := s.ReserveDue(time.Now().UTC().Add(time.Second))
	if err != nil || !found || reserved.ID != j.ID || reserved.Attempts != 1 {
		t.Fatalf("reserve: %+v %v %v", reserved, found, err)
	}
	if _, err := s.Complete(j.ID, 99, "bad", nil, false, 3, 0); !errors.Is(err, task.ErrStaleAttempt) {
		t.Fatalf("stale result accepted: %v", err)
	}
	complete, err := s.Complete(j.ID, reserved.Attempts, "answer", nil, false, 3, 0)
	if err != nil || complete.Status != task.Succeeded {
		t.Fatalf("complete: %+v %v", complete, err)
	}
	got, err := s.Lookup(j.ID)
	if err != nil || got.Result != "answer" {
		t.Fatalf("get: %+v %v", got, err)
	}
}
func TestMySQLConcurrentClaim(t *testing.T) {
	s := testDB(t)
	j, _, err := s.Create("concurrent claim prompt", "claim-it-"+time.Now().Format("150405.000000000"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.db.Exec("DELETE FROM jobs WHERE id=?", j.ID) })
	var wg sync.WaitGroup
	results := make(chan task.Job, 16)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, ok, err := s.ReserveDue(time.Now().UTC().Add(time.Second))
			if err != nil {
				t.Errorf("reserve: %v", err)
			}
			if ok && claimed.ID == j.ID {
				results <- claimed
			}
		}()
	}
	wg.Wait()
	close(results)
	n := 0
	for range results {
		n++
	}
	if n != 1 {
		t.Fatalf("same task claimed %d times", n)
	}
}
