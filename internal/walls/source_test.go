package walls_test

// 壁はこのモジュールのパッケージを import しない。値はソースを構文解析して読む。
// ⚠ import すると、そのパッケージの init() が壁のテストの中で動く。AI が merge できるパッケージの init() で
//   壁を止めたり、壁が読む値をすり替えたりできてしまう。
//   TestWallsImportNothingFromThisModule がこの規則を守る。

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parseDir は dir の Go のソースのうち、コンパイラが実際にビルドするもの（_test.go を除く）を構文解析する。
// ⚠ ファイルを glob で拾わない: //go:build や _linux のような接尾辞でビルドから外れたファイルのリテラルを読むと、
//
//	壁はバイナリに無い値を照らすことになる。どのファイルを
//	ビルドするかは go/build に決めさせる。加えて、本番のコードにビルドの制約を置くこと自体を境界の規則で禁じている。
func parseDir(t *testing.T, dir string) []*ast.File {
	t.Helper()
	pkg, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	var out []*ast.File
	for _, name := range pkg.GoFiles {
		p := filepath.Join(dir, name)
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		t.Fatalf("no Go source in %s — the wall would read nothing", dir)
	}
	return out
}

// constValues は、パッケージの const 宣言のうち、値がリテラルのものを 名前 → 値 で返す。
// 文字列は引用符を外し、Severity("fail") のような型変換は中のリテラルを使う。
func constValues(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, s := range g.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := literal(vs.Values[i]); ok {
							out[n.Name] = v
						}
					}
				}
			}
		}
	}
	return out
}

func literal(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		}
		return e.Value, true
	case *ast.CallExpr: // Severity("fail")
		if len(e.Args) == 1 {
			return literal(e.Args[0])
		}
	}
	return "", false
}

// varLiteral は `var name = []T{...}` の複合リテラルを返す。無ければ nil。
func varLiteral(files []*ast.File, name string) *ast.CompositeLit {
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if n.Name == name && i < len(vs.Values) {
						if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
							return cl
						}
					}
				}
			}
		}
	}
	return nil
}

func TestWallsImportNothingFromThisModule(t *testing.T) {
	paths, _ := filepath.Glob("*.go")
	if len(paths) == 0 {
		t.Fatal("no Go source in internal/walls")
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, b, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, module) {
				t.Errorf("%s imports %s: a wall must read source, not import it (its init() would run inside the wall)", p, path)
			}
		}
	}
}
