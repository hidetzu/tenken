package crawl

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// docs/test-perspectives.md §2.2: --max-requests は並行しても N を超えない。exempt は止めずに数える。0 は無制限。
func TestBudgetNeverLetsMoreThanNThrough(t *testing.T) {
	b := NewBudget(now, 0, 10, func() time.Time { return now })
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.take(false) {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 10 || b.sent != 10 || len(b.Hit()) != 1 || b.Hit()[0] != "max-requests" {
		t.Fatalf("let through %d, sent %d, hit %v", ok.Load(), b.sent, b.Hit())
	}
	if !b.take(true) || b.sent != 11 {
		t.Fatalf("an exempt request was stopped or not counted: sent %d", b.sent)
	}
	var none *Budget
	if !none.take(false) || none.Hit() != nil {
		t.Fatal("a nil Budget must not limit")
	}
	unlimited := NewBudget(now, 0, 0, func() time.Time { return now.Add(1000 * time.Hour) })
	for i := 0; i < 1000; i++ {
		if !unlimited.take(false) {
			t.Fatalf("0 and 0 must mean no limit (stopped at %d)", i)
		}
	}
}

// 期限は start + maxDuration。その瞬間からは出さない（Before でない）。
func TestBudgetDeadlineIsInclusive(t *testing.T) {
	clock := now
	b := NewBudget(now, time.Second, 0, func() time.Time { return clock })
	clock = now.Add(999 * time.Millisecond)
	if !b.take(false) {
		t.Fatal("refused before the deadline")
	}
	clock = now.Add(time.Second)
	if b.take(false) || len(b.Hit()) != 1 || b.Hit()[0] != "max-duration" {
		t.Fatalf("let a request through at the deadline: hit %v", b.Hit())
	}
}
