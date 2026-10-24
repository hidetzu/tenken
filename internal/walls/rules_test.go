package walls_test

// ⚠ この照合は docs/cli-reference.md（Owner 専用）を守る壁。弱めない。
// ⚠ 両方向に照らす: 数が同じでも、片方の重複がもう片方の欠落を隠す。

import (
	"go/ast"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func cliSection(t *testing.T, from, to string) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/cli-reference.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	start, end := strings.Index(doc, from), strings.Index(doc, to)
	if start < 0 || end < start {
		t.Fatalf("docs/cli-reference.md has no section %q — the cross-check would pass vacuously", from)
	}
	return doc[start:end]
}

// diffSets は two の食い違いを返す（重複、片方にしか無いもの）。
func diffSets(spec, code []string) []string {
	var out []string
	count := func(xs []string, name string) map[string]bool {
		seen := map[string]bool{}
		for _, x := range xs {
			if seen[x] {
				out = append(out, x+" appears twice in "+name)
			}
			seen[x] = true
		}
		return seen
	}
	s, c := count(spec, "docs/cli-reference.md"), count(code, "the code")
	for x := range s {
		if !c[x] {
			out = append(out, x+" is in docs/cli-reference.md but not in the code")
		}
	}
	for x := range c {
		if !s[x] {
			out = append(out, x+" is in the code but not in docs/cli-reference.md")
		}
	}
	sort.Strings(out)
	return out
}

func TestRulesAndDefaultsMatchCLISpec(t *testing.T) {
	sec := cliSection(t, "\n### 2.2 Rules", "\n### 2.3 ")
	rows := regexp.MustCompile("(?m)^\\| `([a-z]+)\\.([a-z-]+)` \\| (fail|warn|info) \\|").FindAllStringSubmatch(sec, -1)
	if len(rows) == 0 {
		t.Fatal("found no rule rows in docs/cli-reference.md § 2.2")
	}
	var spec, code []string
	for _, r := range rows {
		spec = append(spec, r[1]+"."+r[2]+" "+r[1]+" "+r[3]) // ID、チェック、既定の重さを 1 つの文字列で照らす
	}
	for _, r := range rulesFromSource(t) {
		code = append(code, r)
	}
	for _, e := range diffSets(spec, code) {
		t.Error(e)
	}
}

func TestChecksMatchCLISpec(t *testing.T) {
	sec := cliSection(t, "\n### 2.1 Check names", "\n### 2.2 ")
	rows := regexp.MustCompile("(?m)^\\| `([a-z]+)` \\|").FindAllStringSubmatch(sec, -1)
	if len(rows) == 0 {
		t.Fatal("found no check rows in docs/cli-reference.md § 2.1")
	}
	var spec []string
	for _, r := range rows {
		spec = append(spec, r[1])
	}
	for _, e := range diffSets(spec, checksFromSource(t)) {
		t.Error(e)
	}
}

func TestDiffSetsSeesDuplicatesAndBothDirections(t *testing.T) {
	e := strings.Join(diffSets([]string{"a", "b"}, []string{"a", "a"}), "\n")
	for _, want := range []string{"a appears twice in the code", "b is in docs/cli-reference.md but not in the code"} {
		if !strings.Contains(e, want) {
			t.Errorf("want %q in:\n%s", want, e)
		}
	}
	if e := diffSets([]string{"a"}, []string{"a", "z"}); len(e) != 1 || !strings.Contains(e[0], "z is in the code but not") {
		t.Errorf("an extra entry in the code: %v", e)
	}
}

// rulesFromSource は internal/finding の `var Rules = []Rule{{"id", "check", Severity}, ...}` を読み、
// "id check severity" の並びを返す（import しない。source_test.go）。
// ⚠ リテラルとして読めない形に変わったら落ちる: 壁が読めない値は、壁が守れない値。
func rulesFromSource(t *testing.T) []string {
	t.Helper()
	files := parseDir(t, "../finding")
	consts := constValues(files)
	cl := varLiteral(files, "Rules")
	if cl == nil || len(cl.Elts) == 0 {
		t.Fatal("internal/finding has no literal `var Rules = []Rule{...}` the wall can read")
	}
	var out []string
	for _, e := range cl.Elts {
		r, ok := e.(*ast.CompositeLit)
		if !ok || len(r.Elts) != 3 {
			t.Fatalf("a Rules entry is not {\"id\", \"check\", Severity}: %T", e)
		}
		id, ok1 := literal(r.Elts[0])
		check, ok2 := literal(r.Elts[1])
		sev, ok3 := r.Elts[2].(*ast.Ident)
		if !ok1 || !ok2 || !ok3 || consts[sev.Name] == "" {
			t.Fatalf("a Rules entry is not {\"id\", \"check\", Severity constant}")
		}
		out = append(out, id+" "+check+" "+consts[sev.Name])
	}
	return out
}

// checksFromSource は internal/finding の `var Checks = []string{...}` を読む。
func checksFromSource(t *testing.T) []string {
	t.Helper()
	cl := varLiteral(parseDir(t, "../finding"), "Checks")
	if cl == nil || len(cl.Elts) == 0 {
		t.Fatal("internal/finding has no literal `var Checks = []string{...}` the wall can read")
	}
	var out []string
	for _, e := range cl.Elts {
		v, ok := literal(e)
		if !ok {
			t.Fatal("a Checks entry is not a string literal")
		}
		out = append(out, v)
	}
	return out
}
