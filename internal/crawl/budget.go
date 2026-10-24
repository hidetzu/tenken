package crawl

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Budget は実行全体の上限（docs/cli-reference.md §3.2 の --max-duration と --max-requests）。
// nil なら上限は無い。⚠ 時計は引数で受け取る（docs/design.md §2 § Walls）。
type Budget struct {
	mu       sync.Mutex
	deadline time.Time // ゼロなら時間の上限は無い
	max      int       // 0 なら要求の数の上限は無い
	now      func() time.Time
	sent     int
	hit      map[string]bool
}

// NewBudget は、start から maxDuration（0 なら無制限）、要求 maxRequests 回（0 なら無制限）の上限を作る。
func NewBudget(start time.Time, maxDuration time.Duration, maxRequests int, now func() time.Time) *Budget {
	b := &Budget{max: maxRequests, now: now, hit: map[string]bool{}}
	if maxDuration > 0 {
		b.deadline = start.Add(maxDuration)
	}
	return b
}

// errOverLimits は、Budget が要求を出させなかったこと（Follow は StopOverLimits にする）。
var errOverLimits = errors.New("the run's --max-duration or --max-requests was reached")

// take は要求を 1 つ出してよいかを答え、出すなら数える。
// exempt（開始ページ・probe・robots.txt）は止めずに数える: 止めれば点検そのものができない（docs/cli-reference.md §3.2）。
// ⚠ 要求の直前に呼ぶ（番を取ったあと）: --delay や 429 の待ちの間に期限を過ぎていれば、待ったあとの要求は出さない。
func (b *Budget) take(exempt bool) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !exempt && b.overLocked() {
		return false
	}
	b.sent++
	return true
}

// over は、もう上限に当たっているか（数えない）。番を取る前に見る: 出さない要求のために --delay を寝ない
// （寝てから断ると、残りの候補ごとに間隔を 1 つずつ寝て、実行が上限のあとも続く）。
func (b *Budget) over(exempt bool) bool {
	if b == nil || exempt {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overLocked()
}

func (b *Budget) overLocked() bool {
	if !b.deadline.IsZero() && !b.now().Before(b.deadline) {
		b.hit["max-duration"] = true
		return true
	}
	if b.max > 0 && b.sent >= b.max {
		b.hit["max-requests"] = true
		return true
	}
	return false
}

// countsRequests は、要求の数に上限があるか（--max-requests が 0 でない）。
func (b *Budget) countsRequests() bool { return b != nil && b.max > 0 }

// Hit は当たった上限の名前（docs/cli-reference.md §5.2 の limits_hit。名前の順）。
func (b *Budget) Hit() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for name := range b.hit {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
