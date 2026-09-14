package store

import (
	"sync"
	"testing"
)

func TestDurabilityDedupeAndRecovery(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := Job{Repo: "owner/repo", Number: 1, Installation: 3}
	a, new, err := s.Enqueue("delivery", j)
	if err != nil || !new {
		t.Fatal(err)
	}
	b, new, err := s.Enqueue("delivery", j)
	if err != nil || new || a.ID != b.ID {
		t.Fatal("redelivery duplicated")
	}
	claimed, ok, err := s.Claim()
	if err != nil || !ok {
		t.Fatal(err)
	}
	_, _, err = s.Enqueue("second", j)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ = s.Claim(); ok {
		t.Fatal("same PR ran concurrently")
	}
	reopened, _ := Open(s.Dir)
	if err = reopened.Recover(); err != nil {
		t.Fatal(err)
	}
	c, ok, err := reopened.Claim()
	if err != nil || !ok || c.ID != claimed.ID {
		t.Fatal("job not recovered")
	}
	if err = s.Retry(c.ID); err == nil {
		t.Fatal("running job retried")
	}
	c.Status = "failed"
	if err = s.Save(&c); err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(c.ID); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentClaimOnlyOnce(t *testing.T) {
	s, _ := Open(t.TempDir())
	_, _, err := s.Enqueue("one", Job{Repo: "o/r", Number: 1, Installation: 1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.Claim()
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if n != 1 {
		t.Fatal(n)
	}
}
func TestDaemonLockAndTraversal(t *testing.T) {
	s, _ := Open(t.TempDir())
	release, err := s.DaemonLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = s.DaemonLock(); err == nil {
		t.Fatal("second daemon admitted")
	}
	if _, err = s.Get("../../outside"); err == nil {
		t.Fatal("invalid ID accepted")
	}
}
