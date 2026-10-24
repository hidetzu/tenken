package testsupport

import "time"

// Now はテストの基準の時刻。今日の日付に依存しないように、固定の値を使う（docs/test-perspectives.md §1）。
var Now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// Clock は固定の時刻を返す関数を作る。production のコードは now func() time.Time を受け取る（docs/design.md §2）。
func Clock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
