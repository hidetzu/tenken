package walls_test

// docs/design.md §2 の境界を、文ではなくテストで守る。規則は rules の表と violations() だけにあり、
// TestRepositoryKeepsTheBoundaries がリポジトリのすべての Go のソース（_test.go、testdata/、vendor/、隠しディレクトリは除く）に当てる。
// ⚠ cmd/ と internal/ だけではない: 表に無いディレクトリ（pkg/ など）に置いたファイルは、それだけで違反になる。
// TestBoundaryRules は、すり抜け方の 1 つ 1 つ（別名、ドット import、log、組み込みの print、time.Since、…）を
// 小さなソースで再現して、violations() がそれを捕まえることを確かめる。
//
// ⚠ 読んだファイルが 0 件なら失敗する: 「何も読まなかった」と「違反が無かった」は同じに見える（.claude/rules/verification.md）。
// ⚠ import のパスで判断する。識別子の名前では判断しない（別名とローカル変数を取り違えないため）。

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/hidetzu/tenken/"

// mayImport は、パッケージ（リポジトリのルートからのディレクトリ）が import してよい、このモジュールの中のパッケージ。
// design.md §2 の依存の向き。⚠ ここに無いディレクトリは、それだけで違反（層を足すなら docs/design.md §2 の表が先）。
var mayImport = map[string][]string{
	"cmd/tenken":           {"internal/cli"},
	"internal/cli":         {"internal/crawl", "internal/check/", "internal/finding", "internal/fetch"},
	"internal/crawl":       {"internal/fetch", "internal/finding"},
	"internal/check/":      {"internal/fetch", "internal/finding"},
	"internal/fetch":       {},
	"internal/finding":     {},
	"internal/testsupport": {},
}

// 接続を開ける標準ライブラリと x/net のパッケージ。⚠ import するだけで違反（design.md §2）。
// net/url、net/netip、net/textproto、net/mail は接続を開けないので含まない。golang.org/x/net/html は HTML の解析だけなので含まない。
func opensConnections(path string) bool {
	switch {
	case path == "net", path == "crypto/tls", path == "net/http", strings.HasPrefix(path, "net/http/"),
		path == "net/smtp", path == "net/rpc", strings.HasPrefix(path, "net/rpc/"):
		return true
	case path == "log/syslog": // syslog.Dial
		return true
	case strings.HasPrefix(path, "golang.org/x/net/"):
		return !(path == "golang.org/x/net/html" || strings.HasPrefix(path, "golang.org/x/net/html/"))
	}
	return false
}

// 表示と終了。internal/cli と cmd/ だけが使ってよい。
var printsOrExits = map[string]map[string]bool{
	"fmt":        {"Print": true, "Printf": true, "Println": true},
	"os":         {"Exit": true, "Stdout": true, "Stderr": true},
	"log":        nil, // log のすべて（Print*、Fatal*、Panic*、SetOutput、…）
	"log/slog":   nil, // 既定で標準エラーに書く
	"log/syslog": nil,
}

// どこでも import しない。import の走査ではその先の振る舞い（標準出力への書き込み、接続、時計）が見えなくなるので、入口ごと閉じる。
// cgo（"C"）、unsafe（//go:linkname は unsafe の import が要る）、syscall、os/exec（外のコマンドで通信できる）。
var neverImport = map[string]bool{"C": true, "unsafe": true, "syscall": true, "os/exec": true}

// 時計を読む。internal/cli だけが使ってよい（ほかは now func() time.Time を受け取る）。
var readsClock = map[string]bool{"Now": true, "Since": true, "Until": true}

type src struct {
	pkg  string // リポジトリのルートからのディレクトリ（例: "internal/check/cert"）
	path string
	body string
}

func under(pkg, dir string) bool {
	if strings.HasSuffix(dir, "/") {
		return strings.HasPrefix(pkg, dir)
	}
	return pkg == dir || strings.HasPrefix(pkg, dir+"/")
}

func allowedDeps(pkg string) ([]string, bool) {
	for dir, deps := range mayImport {
		if under(pkg, dir) {
			return deps, true
		}
	}
	return nil, false
}

// violations は 1 つのファイルの違反を返す。
func violations(s src) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), s.path, s.body, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var out []string
	bad := func(format string, a ...any) { out = append(out, s.path+": "+fmt.Sprintf(format, a...)) }

	deps, known := allowedDeps(s.pkg)
	if !known {
		bad("package %s is not in design.md §2's table — a new layer needs docs/design.md §2 first", s.pkg)
	}

	names := map[string]string{} // ローカル名 → import のパス
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		local := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == "." {
			bad("dot-imports %s: never dot-import (the boundary is checked by import path)", p)
		}
		names[local] = p

		// internal/testsupport はテストの道具で、本番のコードからは import できない（下の規則）ので、testing を使ってよい
		if p == "testing" && !under(s.pkg, "internal/testsupport") {
			bad("imports testing outside a _test.go file: production code must not behave differently under test")
		}
		if neverImport[p] {
			bad("imports %s: never (it reaches past what this test can see)", p)
		}
		if opensConnections(p) && !under(s.pkg, "internal/fetch") && !under(s.pkg, "internal/testsupport") {
			bad("imports %s: only internal/fetch may open connections", p)
		}
		if dep, ok := strings.CutPrefix(p, module); ok {
			if dep == "internal/testsupport" && !under(s.pkg, "internal/testsupport") {
				bad("imports internal/testsupport: it is for tests only")
			}
			ok := false
			for _, d := range deps {
				if under(dep, d) {
					ok = true
				}
			}
			if known && !ok {
				bad("imports %s: design.md §2 does not let %s depend on it", dep, s.pkg)
			}
		}
	}

	// init() は import されるだけで動く。壁のテストが別のパッケージを import しなくても、本番のバイナリの中で値をすり替えられる。
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "init" {
			bad("declares init(): set state explicitly from the caller instead (init runs on import, unseen)")
		}
	}
	// //go:debug は crypto/x509 と crypto/tls の振る舞いを変える（go.mod の godebug と同じ）。
	// ⚠ ビルドの制約はここでは見ない: 規則を自分で書き直すと go/build とずれる。
	//   TestEveryTargetBuildsEveryFile が go/build そのものに、ターゲットごとに聞く。
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:debug") {
				bad("has a //go:debug directive: it changes what crypto/x509 and crypto/tls observe — a reason in docs/design.md first")
			}
		}
	}

	mayPrint := under(s.pkg, "internal/cli") || under(s.pkg, "cmd")
	mayReadClock := under(s.pkg, "internal/cli")
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			id, ok := n.X.(*ast.Ident)
			// ⚠ id.Obj != nil はローカルの変数（例: time := …）。import の名前は解決されずに nil のまま
			if !ok || id.Obj != nil {
				return true
			}
			p, imported := names[id.Name]
			if !imported {
				return true
			}
			if fns, banned := printsOrExits[p]; banned && (fns == nil || fns[n.Sel.Name]) && !mayPrint {
				bad("uses %s.%s: only internal/cli prints and exits", p, n.Sel.Name)
			}
			if p == "time" && readsClock[n.Sel.Name] && !mayReadClock {
				bad("uses time.%s: take now func() time.Time instead", n.Sel.Name)
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Obj == nil && (id.Name == "print" || id.Name == "println") && !mayPrint {
				bad("calls builtin %s: only internal/cli prints", id.Name)
			}
		}
		return true
	})
	return out, nil
}

func repoSources(t *testing.T) []src {
	t.Helper()
	root := "../.."
	var out []src
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel, _ := filepath.Rel(root, p); rel != "." && skippedByWalk(filepath.ToSlash(rel)+"/x") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, src{pkg: filepath.ToSlash(filepath.Dir(rel)), path: filepath.ToSlash(rel), body: string(b)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// skippedByWalk は repoSources が読まないパス（testdata、vendor、node_modules、隠しディレクトリ）。走査と照合で同じ規則を使う。
func skippedByWalk(p string) bool {
	parts := strings.Split(p, "/")
	for _, d := range parts[:len(parts)-1] {
		if d == "testdata" || d == "vendor" || d == "node_modules" || strings.HasPrefix(d, ".") {
			return true
		}
	}
	return false
}

// go.work は go.mod の replace と同じことができる（どのモジュールでも差し替えられる）。リポジトリに置かない。
func TestNoGoWork(t *testing.T) {
	for _, f := range []string{"../../go.work", "../../go.work.sum"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s exists: a workspace file can replace any module (.claude/rules/go.md)", strings.TrimPrefix(f, "../../"))
		}
	}
}

func TestRepositoryKeepsTheBoundaries(t *testing.T) {
	files := repoSources(t)
	if len(files) == 0 {
		t.Fatal("read no Go source in the repository — the boundary checks would pass vacuously")
	}
	for _, f := range files {
		v, err := violations(f)
		if err != nil {
			t.Fatalf("%s: %v", f.path, err)
		}
		for _, s := range v {
			t.Error(s)
		}
	}
}

// 各行: どのパッケージに、どんなソースを置くと、どの違反が出るか（want が空なら違反なし）。
func TestBoundaryRules(t *testing.T) {
	cases := []struct {
		name, pkg, body, want string
	}{
		{"net/http in a check", "internal/check/x", `package x; import _ "net/http"`, "only internal/fetch may open connections"},
		{"httputil in a check", "internal/check/x", `package x; import _ "net/http/httputil"`, "only internal/fetch may open connections"},
		{"crypto/tls in crawl", "internal/crawl", `package crawl; import _ "crypto/tls"`, "only internal/fetch may open connections"},
		{"x/net/http2 in a check", "internal/check/x", `package x; import _ "golang.org/x/net/http2"`, "only internal/fetch may open connections"},
		{"x/net/html in a check is fine", "internal/check/x", `package x; import _ "golang.org/x/net/html"`, ""},
		{"net/url and net/netip are fine", "internal/crawl", `package crawl; import (_ "net/url"; _ "net/netip")`, ""},
		{"net/http in fetch is fine", "internal/fetch", `package fetch; import _ "net/http"`, ""},
		{"aliased fmt", "internal/check/x", `package x; import f "fmt"; func F() { f.Println() }`, "uses fmt.Println"},
		{"dot-imported fmt", "internal/check/x", `package x; import . "fmt"; func F() { Println() }`, "never dot-import"},
		{"log.Println", "internal/check/x", `package x; import "log"; func F() { log.Println() }`, "uses log.Println"},
		{"log.Panicf", "internal/crawl", `package crawl; import "log"; func F() { log.Panicf("") }`, "uses log.Panicf"},
		{"builtin println", "internal/check/x", `package x; func F() { println() }`, "calls builtin println"},
		{"print in crawl", "internal/crawl", `package crawl; import "fmt"; func F() { fmt.Print() }`, "uses fmt.Print"},
		{"os.Exit in crawl", "internal/crawl", `package crawl; import "os"; func F() { os.Exit(3) }`, "uses os.Exit"},
		{"fmt.Sprintf is fine", "internal/check/x", `package x; import "fmt"; var _ = fmt.Sprintf("")`, ""},
		{"cli may print", "internal/cli", `package cli; import "fmt"; func F() { fmt.Println() }`, ""},
		{"cmd may exit", "cmd/tenken", `package main; import "os"; func main() { os.Exit(0) }`, ""},
		{"aliased time.Now", "internal/check/x", `package x; import clk "time"; var _ = clk.Now()`, "uses time.Now"},
		{"time.Since", "internal/fetch", `package fetch; import "time"; var _ = time.Since(time.Time{})`, "uses time.Since"},
		{"time.Until", "internal/check/x", `package x; import "time"; var _ = time.Until(time.Time{})`, "uses time.Until"},
		{"time.Now in cmd", "cmd/tenken", `package main; import "time"; var _ = time.Now()`, "uses time.Now"},
		{"a local variable that shadows time is not the package", "internal/check/x", `package x; import "time"; var _ time.Duration; type c struct{ Now int }; func F() { time := c{}; _ = time.Now }`, ""},
		{"time.Duration is fine", "internal/fetch", `package fetch; import "time"; var _ time.Duration`, ""},
		{"production code importing testsupport", "internal/check/x", `package x; import _ "github.com/hidetzu/tenken/internal/testsupport"`, "it is for tests only"},
		{"check importing cli", "internal/check/x", `package x; import _ "github.com/hidetzu/tenken/internal/cli"`, "does not let internal/check/x depend on it"},
		{"check importing another check", "internal/check/x", `package x; import _ "github.com/hidetzu/tenken/internal/check/y"`, "does not let internal/check/x depend on it"},
		{"fetch importing finding", "internal/fetch", `package fetch; import _ "github.com/hidetzu/tenken/internal/finding"`, "does not let internal/fetch depend on it"},
		{"check importing finding is fine", "internal/check/x", `package x; import _ "github.com/hidetzu/tenken/internal/finding"`, ""},
		{"an unknown layer", "internal/report", `package report`, "is not in design.md §2's table"},
		{"a package outside cmd/ and internal/", "pkg/foo", `package foo`, "is not in design.md §2's table"},
		{"log/slog in a check", "internal/check/x", `package x; import "log/slog"; func F() { slog.Info("x") }`, "uses log/slog.Info"},
		{"log/syslog in a check", "internal/check/x", `package x; import _ "log/syslog"`, "only internal/fetch may open connections"},
		{"cgo", "internal/fetch", `package fetch; import "C"`, "imports C: never"},
		{"unsafe (go:linkname)", "internal/check/x", `package x; import _ "unsafe"`, "imports unsafe: never"},
		{"syscall", "internal/crawl", `package crawl; import _ "syscall"`, "imports syscall: never"},
		{"os/exec", "internal/check/x", `package x; import _ "os/exec"`, "imports os/exec: never"},
		{"testing in production code", "internal/cli", `package cli; import "testing"; var _ = testing.Testing`, "imports testing outside a _test.go file"},
		{"testing in testsupport is fine", "internal/testsupport", `package testsupport; import "testing"; var _ testing.TB`, ""},
		{"init in cli", "internal/cli", `package cli; func init() {}`, "declares init()"},
		{"init in finding", "internal/finding", `package finding; func init() {}`, "declares init()"},
		{"a method named init is fine", "internal/finding", `package finding; type T struct{}; func (T) init() {}`, ""},
		{"go:debug", "cmd/tenken", "//go:debug x509negativeserial=1\npackage main", "has a //go:debug directive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := violations(src{pkg: c.pkg, path: c.pkg + "/f.go", body: c.body})
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(v, "\n")
			if c.want == "" && got != "" {
				t.Fatalf("want no violation, got:\n%s", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("want a violation containing %q, got:\n%s", c.want, got)
			}
		})
	}
}

// go.md: 依存は標準ライブラリと golang.org/x/net だけ。go.mod の require を読み、replace を置かない。
func goModViolations(text string) []string {
	var out []string
	block := "" // いま中にいる "require (" などのブロックの種類
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		// "require(" も "require (" と同じ（go mod edit はどちらも読む）
		f := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ").Replace(line))
		if len(f) == 0 {
			continue
		}
		if block != "" && f[0] == ")" {
			block = ""
			continue
		}
		directive, args := block, f
		if block == "" {
			directive, args = f[0], f[1:]
			if len(args) == 1 && args[0] == "(" {
				block = directive
				continue
			}
		}
		switch directive {
		case "replace":
			out = append(out, "go.mod has a replace directive: it can swap golang.org/x/net for anything (.claude/rules/go.md)")
		case "godebug":
			out = append(out, "go.mod has a godebug directive: it changes what crypto/x509 and crypto/tls observe — a reason in docs/design.md first")
		case "require":
			if len(args) > 0 && args[0] != "golang.org/x/net" {
				out = append(out, "go.mod requires "+args[0]+": only the standard library and golang.org/x/net (.claude/rules/go.md; anything else is a reason in docs/design.md first)")
			}
		}
	}
	return out
}

func TestGoModRequiresOnlyXNet(t *testing.T) {
	b, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "module "+strings.TrimSuffix(module, "/")) {
		t.Fatal("go.mod does not declare this module — the check would read the wrong file")
	}
	for _, v := range goModViolations(string(b)) {
		t.Error(v)
	}
}

func TestGoModRules(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"x/net in a block", "module m\nrequire (\n\tgolang.org/x/net v0.53.0 // html\n)\n", ""},
		{"x/net on one line", "module m\nrequire golang.org/x/net v0.53.0\n", ""},
		{"a comment line in a block", "module m\nrequire (\n\t// pinned for html\n\tgolang.org/x/net v0.53.0\n)\n", ""},
		{"another module in a block", "module m\nrequire (\n\tgithub.com/x/y v1.0.0\n)\n", "requires github.com/x/y"},
		{"another module on one line", "module m\nrequire github.com/x/y v1.0.0\n", "requires github.com/x/y"},
		{"replace on one line", "module m\nreplace golang.org/x/net => github.com/x/y v1.0.0\n", "replace directive"},
		{"replace in a block", "module m\nreplace (\n\tgolang.org/x/net => ../net\n)\n", "replace directive"},
		{"require( without a space", "module m\nrequire(\n\tgithub.com/x/y v1.0.0\n)\n", "requires github.com/x/y"},
		{"replace( without a space", "module m\nreplace(\n\tgolang.org/x/net => ../net\n)\n", "replace directive"},
		{"godebug", "module m\ngodebug x509sha1=1\n", "godebug directive"},
		{"toolchain is fine", "module m\ntoolchain go1.27.2\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(goModViolations(c.text), "\n")
			if c.want == "" && got != "" {
				t.Fatalf("want none, got %q", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("want %q, got %q", c.want, got)
			}
		})
	}
}

// design.md §2 の依存の表と mayImport は同じ決定の二つの写し。食い違ったら落ちる（CLAUDE.md §3）。
func TestADRTableMatchesMayImport(t *testing.T) {
	b, err := os.ReadFile("../../docs/design.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	start := strings.Index(doc, "| Package | Packages in this module it may import")
	if start < 0 {
		t.Fatal("docs/design.md §2 has no dependency table — the cross-check would pass vacuously")
	}
	tick := regexp.MustCompile("`([^`]+)`")
	adr := map[string][]string{}
	for _, line := range strings.Split(doc[start:], "\n")[2:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		cols := strings.Split(line, "|")
		var deps []string
		if !strings.Contains(cols[2], "none") {
			for _, m := range tick.FindAllStringSubmatch(cols[2], -1) {
				deps = append(deps, strings.TrimSuffix(m[1], "*"))
			}
		}
		for _, m := range tick.FindAllStringSubmatch(cols[1], -1) {
			adr[strings.TrimSuffix(m[1], "*")] = deps
		}
	}
	for _, e := range compareTables(adr, mayImport) {
		t.Error(e)
	}
}

// compareTables は design.md §2 の表と mayImport の食い違いを返す。⚠ 両方向に照らす: 片方にしか無い名前は、依存が空の名前と同じ "" に見えてしまう。
func compareTables(adr, may map[string][]string) []string {
	norm := func(m map[string][]string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			v = append([]string(nil), v...)
			sort.Strings(v)
			out[k] = strings.Join(v, ",")
		}
		return out
	}
	got, want := norm(adr), norm(may)
	var out []string
	for k, v := range want {
		if g, ok := got[k]; !ok {
			out = append(out, k+" is in mayImport but not in design.md §2's table")
		} else if g != v {
			out = append(out, k+": design.md §2 says ["+g+"], mayImport says ["+v+"]")
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			out = append(out, k+" is in design.md §2's table but not in mayImport")
		}
	}
	sort.Strings(out)
	return out
}

func TestCompareTables(t *testing.T) {
	may := map[string][]string{"a": {}, "b": {"a"}}
	if e := compareTables(map[string][]string{"a": nil, "b": {"a"}}, may); len(e) != 0 {
		t.Errorf("equal tables: %v", e)
	}
	e := strings.Join(compareTables(map[string][]string{"b": {"a"}, "c": nil}, may), "\n")
	for _, want := range []string{"a is in mayImport but not in design.md", "c is in design.md §2's table but not in mayImport"} {
		if !strings.Contains(e, want) {
			t.Errorf("want %q in:\n%s", want, e)
		}
	}
	if e := compareTables(map[string][]string{"a": nil, "b": nil}, may); len(e) != 1 {
		t.Errorf("a changed dependency list: %v", e)
	}
}

// 走査そのものを守る: git が追っている Go のソースは、すべて repoSources に入る（cmd/ と internal/ に狭めたら落ちる）。
func TestWalkSeesEveryTrackedGoFile(t *testing.T) {
	out, err := exec.Command("git", "-C", "../..", "ls-files", "-z", "*.go").Output()
	if err != nil {
		// ⚠ CI では飛ばさない: 飛ばしたことは -v なしでは見えず、「走っていない」が「通った」に見える
		if os.Getenv("CI") != "" {
			t.Fatalf("git ls-files failed in CI: %v", err)
		}
		t.Skipf("git ls-files failed (not a git checkout, or git is missing): %v", err)
	}
	seen := map[string]bool{}
	for _, f := range repoSources(t) {
		seen[f.path] = true
	}
	n := 0
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || strings.HasSuffix(p, "_test.go") || skippedByWalk(p) {
			continue
		}
		n++
		if !seen[p] {
			t.Errorf("%s is tracked by git but the boundary walk does not read it", p)
		}
	}
	if n == 0 {
		t.Fatal("git lists no Go source — the check would pass vacuously")
	}
}

// ciTargets は CI が組み立てるターゲット（.github/workflows/ci.yml の cross-compile の for の並び）。
// ⚠ 写しを持たず、ci.yml から読む。
func ciTargets(t *testing.T) [][2]string {
	t.Helper()
	b, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`for t in ([a-z0-9/ ]+); do`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("ci.yml has no cross-compile target list — the check would pass vacuously")
	}
	var out [][2]string
	for _, tgt := range strings.Fields(m[1]) {
		goos, goarch, ok := strings.Cut(tgt, "/")
		if !ok {
			t.Fatalf("ci.yml target %q is not GOOS/GOARCH", tgt)
		}
		out = append(out, [2]string{goos, goarch})
	}
	return out
}

// どのターゲットでも、すべてのファイルがビルドされる。⚠ go/build そのものに聞く（ビルドの制約、ファイル名の接尾辞、
// //+build の書き方の違いを、規則を書き直さずに見る）。ずれがあれば、壁が読むファイルとあるバイナリに入るファイルが違う。
func TestEveryTargetBuildsEveryFile(t *testing.T) {
	byDir := map[string]map[string]bool{}
	for _, f := range repoSources(t) {
		dir := filepath.Join("../..", filepath.FromSlash(f.pkg))
		if byDir[dir] == nil {
			byDir[dir] = map[string]bool{}
		}
		byDir[dir][filepath.Base(f.path)] = true
	}
	targets := ciTargets(t)
	for dir, all := range byDir {
		for _, tg := range targets {
			ctx := build.Default
			ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = tg[0], tg[1], false
			built := map[string]bool{}
			if pkg, err := ctx.ImportDir(dir, 0); err == nil {
				for _, n := range pkg.GoFiles {
					built[n] = true
				}
			}
			for n := range all {
				if !built[n] {
					t.Errorf("%s/%s is not built for %s/%s: tenken builds the same files everywhere, so a wall reads what ships", dir, n, tg[0], tg[1])
				}
			}
		}
	}
}

// shippedPackages は、target で cmd/tenken から辿れるこのモジュールのパッケージ（ディレクトリ → go/build の結果）。
// ⚠ ディレクトリを走査するのではなく、バイナリに入る依存の木を go/build に聞いて辿る。
//
//	走査が飛ばす場所（testdata/、隠しディレクトリ）に置いて import しても、ここには入る。
func shippedPackages(t *testing.T, goos, goarch string) map[string]*build.Package {
	t.Helper()
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = goos, goarch, false
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*build.Package{}
	var visit func(rel string)
	visit = func(rel string) {
		if _, seen := out[rel]; seen {
			return
		}
		pkg, err := ctx.ImportDir(filepath.Join(root, filepath.FromSlash(rel)), 0)
		if err != nil {
			t.Fatalf("%s/%s: %s: %v", goos, goarch, rel, err)
		}
		out[rel] = pkg
		for _, imp := range pkg.Imports {
			if dep, ok := strings.CutPrefix(imp, module); ok {
				visit(dep)
			}
		}
	}
	visit("cmd/tenken")
	return out
}

// バイナリに入るものは、すべて壁が読むものである。ターゲットごとに、cmd/tenken から辿れるパッケージが
//   - 壁が走査するディレクトリにあり（testdata/ や隠しディレクトリに隠していない）
//   - その Go のファイルをすべて壁が読んでいて
//   - Go 以外のビルドの入力（アセンブリ、C、.syso、…）を持たない
//
// ことを確かめる。⚠ Go 以外の入力は、import を経ずにシステムコールや接続ができ、壁はその中身を読めない。
func TestEverythingThatShipsIsReadByTheWalls(t *testing.T) {
	read := map[string]bool{}
	for _, f := range repoSources(t) {
		read[f.path] = true
	}
	for _, tg := range ciTargets(t) {
		for rel, pkg := range shippedPackages(t, tg[0], tg[1]) {
			where := tg[0] + "/" + tg[1] + ": " + rel
			if skippedByWalk(rel + "/x.go") {
				t.Errorf("%s ships but sits where the walls do not look (testdata, vendor, node_modules, or a dot-directory)", where)
			}
			for _, n := range pkg.GoFiles {
				if !read[rel+"/"+n] {
					t.Errorf("%s/%s ships but the walls do not read it", where, n)
				}
			}
			other := map[string][]string{
				"cgo": pkg.CgoFiles, "C": pkg.CFiles, "C++": pkg.CXXFiles, "Objective-C": pkg.MFiles, "header": pkg.HFiles,
				"Fortran": pkg.FFiles, "assembly": pkg.SFiles, "SWIG": pkg.SwigFiles, "SWIG C++": pkg.SwigCXXFiles, "syso": pkg.SysoFiles,
			}
			kinds := make([]string, 0, len(other))
			for k := range other {
				kinds = append(kinds, k)
			}
			sort.Strings(kinds)
			for _, k := range kinds {
				if len(other[k]) > 0 {
					t.Errorf("%s ships %s files %v: the walls cannot read what they do", where, k, other[k])
				}
			}
		}
	}
}
