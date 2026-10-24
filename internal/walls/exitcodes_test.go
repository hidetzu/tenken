package walls_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 終了コードの値の正本は docs/cli-reference.md §4。internal/cli の定数はその写しなので、表と食い違ったら落ちる
// （CLAUDE.md §3: 同じ決定を二か所に書くなら、機械で照らし合わせる）。
func TestExitCodesMatchCLISpec(t *testing.T) {
	b, err := os.ReadFile("../../docs/cli-reference.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	start := strings.Index(doc, "\n## 4. Exit codes")
	end := strings.Index(doc, "\n## 5. ")
	if start < 0 || end < start {
		t.Fatal("docs/cli-reference.md has no § 4 Exit codes section — the cross-check would pass vacuously")
	}
	rows := regexp.MustCompile("(?m)^\\| `(\\d)` \\| ([^|]+)\\|").FindAllStringSubmatch(doc[start:end], -1)
	// internal/cli の定数はソースから読む（import しない。source_test.go）
	consts := constValues(parseDir(t, "../cli"))
	want := map[string]int{} // §4 の Meaning の列にある言葉 → 定数の値
	for phrase, name := range map[string]string{
		"no finding reaches": "ExitOK",
		"findings reach":     "ExitFindings",
		"Usage error":        "ExitUsage",
		"Could not check":    "ExitCouldNotCheck",
	} {
		v, ok := consts[name]
		if !ok {
			t.Fatalf("internal/cli has no constant %s with a literal value", name)
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("internal/cli: %s = %s is not an integer", name, v)
		}
		want[phrase] = n
	}
	if len(rows) != len(want) {
		t.Fatalf("docs/cli-reference.md § 4 has %d exit-code rows, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		code, _ := strconv.Atoi(r[1])
		matched := false
		for phrase, c := range want {
			if strings.Contains(r[2], phrase) {
				matched = true
				if c != code {
					t.Errorf("docs/cli-reference.md § 4 says %d for %q, and internal/cli says %d", code, phrase, c)
				}
			}
		}
		if !matched {
			t.Errorf("docs/cli-reference.md § 4 row %d (%q) matches no constant in internal/cli", code, strings.TrimSpace(r[2]))
		}
	}
}
