package headers

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hidetzu/tenken/internal/finding"
)

func ids(fs []finding.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return strings.Join(out, ",")
}

var o = Options{HSTSMinAge: 15552000}

func page(u string, h map[string][]string) Page { return Page{URL: u, Header: h} }

func good() map[string][]string {
	return map[string][]string{
		"Strict-Transport-Security": {"max-age=31536000; includeSubDomains"},
		"X-Content-Type-Options":    {"nosniff"},
		"X-Frame-Options":           {"DENY"},
		"Content-Security-Policy":   {"default-src 'self'"},
		"Referrer-Policy":           {"strict-origin-when-cross-origin"},
	}
}

func TestAllHeadersPresentIsClean(t *testing.T) {
	if fs := Check([]Page{page("https://a.test/", good())}, o); len(fs) != 0 {
		t.Fatalf("got %s", ids(fs))
	}
}

// docs/test-perspectives.md §3.4: 何も無ければ、それぞれの指摘。subject はオリジン。detail は空。found_on は header で value は null。
func TestNothingSentGivesEveryRule(t *testing.T) {
	fs := Check([]Page{page("https://a.test/x", nil)}, o)
	if got := ids(fs); got != "headers.framing-missing,headers.hsts-missing,headers.nosniff-missing,headers.csp-missing,headers.referrer-policy-missing" {
		t.Fatalf("got %s (warn first, then info; by rule ID)", got)
	}
	for _, f := range fs {
		if f.Subject != "https://a.test" || len(f.Detail) != 0 || len(f.FoundOn) != 1 || f.FoundOn[0].Kind != finding.KindHeader || f.FoundOn[0].Value != nil || f.FoundOn[0].Page != "https://a.test/x" {
			t.Fatalf("finding = %+v", f)
		}
	}
	if fs[0].Severity != finding.Warn || fs[3].Severity != finding.Info {
		t.Fatalf("severities %v %v", fs[0].Severity, fs[3].Severity)
	}
}

// HSTS は https の応答でだけ見る。
func TestHSTSIsNotLookedAtOnHTTP(t *testing.T) {
	fs := Check([]Page{page("http://a.test/", nil)}, o)
	if strings.Contains(ids(fs), "hsts") {
		t.Fatalf("got %s", ids(fs))
	}
}

// HSTS の max-age がしきい値未満、または壊れている → hsts-weak。ちょうどは弱くない。
func TestHSTSWeak(t *testing.T) {
	for v, want := range map[string]string{
		"max-age=15552000":                   "",
		"max-age=15551999":                   "headers.hsts-weak",
		"max-age=0":                          "headers.hsts-weak",
		"MAX-AGE=\"31536000\"":               "",
		"includeSubDomains; max-age=99999":   "headers.hsts-weak",
		"includeSubDomains":                  "headers.hsts-weak",
		"max-age=abc":                        "headers.hsts-weak",
		"max-age=-1":                         "headers.hsts-weak",
		"max-age=+100":                       "headers.hsts-weak", // + は DIGIT ではない
		"max-age=99999999999999999999":       "",                  // 上限は無い（RFC 6797 §6.1.1）: 十分に長い
		"max-age=1099511627777":              "",
		"max-age=":                           "headers.hsts-weak",
		"max-age=31536000; max-age=0":        "headers.hsts-weak", // 同じ指示が 2 回: ブラウザが無視する（§6.1、§8.1）
		"max-age=31536000; max-age=31536000": "headers.hsts-weak", // 2 回で値が同じでも（値を見て弱いのではない）
		"max-age=31536000; includeSubDomains; includeSubDomains": "headers.hsts-weak",
		"max-age=\"31536000":                           "headers.hsts-weak", // 引用符が閉じていない
		"max-age=31536000; preload; includeSubDomains": "",
	} {
		h := good()
		h["Strict-Transport-Security"] = []string{v}
		if got := ids(Check([]Page{page("https://a.test/", h)}, o)); got != want {
			t.Errorf("STS %q: %q, want %q", v, got, want)
		}
	}
	// しきい値はフラグから
	h := good()
	h["Strict-Transport-Security"] = []string{"max-age=100"}
	if got := ids(Check([]Page{page("https://a.test/", h)}, Options{HSTSMinAge: 100})); got != "" {
		t.Fatalf("--hsts-min-age 100 with max-age=100: %s", got)
	}
}

// Fetch の "determine nosniff": , で分けた最初の要素が nosniff なら有効。複数のヘッダは , でつないだ並び。
func TestNosniff(t *testing.T) {
	for v, want := range map[string]string{"nosniff": "", "NoSniff": "", " nosniff ": "", "sniff": "headers.nosniff-invalid", "": "headers.nosniff-invalid", "nosniff, nosniff": "", "nosniff, foo": "", "foo, nosniff": "headers.nosniff-invalid"} {
		h := good()
		h["X-Content-Type-Options"] = []string{v}
		if got := ids(Check([]Page{page("https://a.test/", h)}, o)); got != want {
			t.Errorf("XCTO %q: %q, want %q", v, got, want)
		}
	}
	h := good()
	h["X-Content-Type-Options"] = []string{"nosniff", "garbage"}
	if got := ids(Check([]Page{page("https://a.test/", h)}, o)); got != "" {
		t.Errorf("two headers, first nosniff: %q", got)
	}
	h["X-Content-Type-Options"] = []string{"garbage", "nosniff"}
	if got := ids(Check([]Page{page("https://a.test/", h)}, o)); got != "headers.nosniff-invalid" {
		t.Errorf("two headers, first garbage: %q", got)
	}
	// 巨大な値は文に入れない（found_on の value にはそのまま）。切るのは文字の境目
	h["X-Content-Type-Options"] = []string{strings.Repeat("x", 100000)}
	if fs := Check([]Page{page("https://a.test/", h)}, o); len(fs[0].Message) > 300 || len(*fs[0].FoundOn[0].Value) != 100000 {
		t.Errorf("message length %d, value length %d", len(fs[0].Message), len(*fs[0].FoundOn[0].Value))
	}
	h["X-Content-Type-Options"] = []string{strings.Repeat("é", 61)}
	if fs := Check([]Page{page("https://a.test/", h)}, o); !utf8.ValidString(fs[0].Message) || !strings.Contains(fs[0].Message, strings.Repeat("é", 60)+"…") {
		t.Errorf("message %q", fs[0].Message)
	}
}

// X-Frame-Options か CSP の frame-ancestors のどちらかがあればよい。Report-Only は数えない。
func TestFraming(t *testing.T) {
	cases := []struct {
		name string
		h    map[string][]string
		want string
	}{
		{"only XFO", withOnly("X-Frame-Options", "SAMEORIGIN"), "headers.csp-missing"},
		{"only CSP frame-ancestors", withOnly("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'"), ""},
		{"CSP without frame-ancestors", withOnly("Content-Security-Policy", "default-src 'self'"), "headers.framing-missing"},
		{"CSP frame-ancestors uppercase", withOnly("Content-Security-Policy", "FRAME-ANCESTORS 'self'"), ""},
		{"frame-ancestors in a second policy of one header (CSP3 §4.1.1)", withOnly("Content-Security-Policy", "default-src 'self', frame-ancestors 'none'"), ""},
		{"frame-ancestors in a second CSP header", withTwoCSP("default-src 'self'", "frame-ancestors 'none'"), ""},
		{"report-only does not count", map[string][]string{"Content-Security-Policy-Report-Only": {"frame-ancestors 'none'"}, "Strict-Transport-Security": good()["Strict-Transport-Security"], "X-Content-Type-Options": {"nosniff"}, "Referrer-Policy": {"no-referrer"}}, "headers.framing-missing,headers.csp-missing"},
	}
	for _, c := range cases {
		if got := ids(Check([]Page{page("https://a.test/", c.h)}, o)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func withTwoCSP(a, b string) map[string][]string {
	h := good()
	delete(h, "X-Frame-Options")
	h["Content-Security-Policy"] = []string{a, b}
	return h
}

func withOnly(name, value string) map[string][]string {
	h := good()
	delete(h, "X-Frame-Options")
	delete(h, "Content-Security-Policy")
	h[name] = []string{value}
	return h
}

// ヘッダ名の大文字小文字、同じヘッダが複数ある（最初のものを使う）。
func TestHeaderNameCaseAndRepeats(t *testing.T) {
	h := map[string][]string{
		"strict-transport-security": {"max-age=31536000", "max-age=1"},
		"x-content-type-options":    {"nosniff", "garbage"},
		"X-FRAME-OPTIONS":           {"DENY"},
		"content-security-policy":   {"default-src 'none'"},
		"referrer-policy":           {"no-referrer"},
	}
	if got := ids(Check([]Page{page("https://a.test/", h)}, o)); got != "" {
		t.Fatalf("got %s", got)
	}
}

// 巡回した全ページで見て、同じ指摘は 1 件にまとめ、見つかった場所を全部持つ（値はページごと）。別のオリジンは別の件。
func TestFindingsMergeAcrossPagesPerOrigin(t *testing.T) {
	h1 := good()
	delete(h1, "Referrer-Policy")
	h2 := good()
	delete(h2, "Referrer-Policy")
	h2["Strict-Transport-Security"] = []string{"max-age=5"}
	fs := Check([]Page{page("https://a.test/", h1), page("https://a.test/p2", h2), page("https://b.test/", nil)}, o)
	var refA *finding.Finding
	for i := range fs {
		if fs[i].Rule == "headers.referrer-policy-missing" && fs[i].Subject == "https://a.test" {
			refA = &fs[i]
		}
	}
	if refA == nil || len(refA.FoundOn) != 2 || refA.FoundOn[0].Page != "https://a.test/" || refA.FoundOn[1].Page != "https://a.test/p2" {
		t.Fatalf("referrer-policy on a.test: %+v", refA)
	}
	var weak *finding.Finding
	for i := range fs {
		if fs[i].Rule == "headers.hsts-weak" {
			weak = &fs[i]
		}
	}
	if weak == nil || len(weak.FoundOn) != 1 || *weak.FoundOn[0].Value != "max-age=5" || weak.FoundOn[0].Header != "Strict-Transport-Security" {
		t.Fatalf("hsts-weak: %+v", weak)
	}
	subjects := map[string]bool{}
	for _, f := range fs {
		subjects[f.Subject] = true
	}
	if !subjects["https://a.test"] || !subjects["https://b.test"] || len(subjects) != 2 {
		t.Fatalf("subjects %v", subjects)
	}
}

func TestMessagesNameTheOriginAndNoLibraryText(t *testing.T) {
	for _, f := range Check([]Page{page("https://a.test/", map[string][]string{"Strict-Transport-Security": {"max-age=x"}})}, o) {
		if !strings.HasPrefix(f.Message, "https://a.test ") || strings.Contains(f.Message, "strconv") {
			t.Fatalf("%q", f.Message)
		}
	}
}
