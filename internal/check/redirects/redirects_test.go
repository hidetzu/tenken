package redirects

import (
	"strings"
	"testing"

	"github.com/hidetzu/tenken/internal/finding"
)

func hop(u string, status int, next string) Hop {
	loc := next
	return Hop{URL: u, Status: status, Location: loc, Next: next}
}

func ids(fs []finding.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule+"@"+f.Subject)
	}
	return strings.Join(out, " ")
}

var start = finding.Start("https://a.test/")

func TestOneRedirectIsNotAFinding(t *testing.T) {
	c := Chain{Hops: []Hop{hop("https://a.test/", 301, "https://a.test/b"), hop("https://a.test/b", 200, "")}, Stop: StopFinal}
	if fs := Check(c, 3, start); len(fs) != 0 {
		t.Fatalf("got %s", ids(fs))
	}
}

func TestTemporaryRedirectsAreInfoPerRedirectingURL(t *testing.T) {
	c := Chain{Hops: []Hop{hop("https://a.test/", 302, "https://a.test/b"), hop("https://a.test/b", 307, "https://a.test/c"), hop("https://a.test/c", 301, "https://a.test/d"), hop("https://a.test/d", 200, "")}, Stop: StopFinal}
	fs := Check(c, 3, start)
	if ids(fs) != "redirects.temporary@https://a.test/ redirects.temporary@https://a.test/b" {
		t.Fatalf("got %s", ids(fs))
	}
	if fs[0].Severity != finding.Info || len(fs[0].FoundOn) != 1 || fs[0].FoundOn[0].Kind != finding.KindStart {
		t.Fatalf("finding = %+v", fs[0])
	}
	chain := fs[0].Detail["chain"].([]map[string]any)
	if len(chain) != 4 || chain[0]["status"] != 302 || chain[3]["url"] != "https://a.test/d" {
		t.Fatalf("detail.chain = %v (first request to last response)", chain)
	}
}

func TestDowngradeIsFail(t *testing.T) {
	c := Chain{Hops: []Hop{hop("https://a.test/", 301, "http://a.test/x"), hop("http://a.test/x", 200, "")}, Stop: StopFinal}
	fs := Check(c, 3, start)
	if ids(fs) != "redirects.downgrade@https://a.test/" || fs[0].Severity != finding.Fail {
		t.Fatalf("got %s", ids(fs))
	}
	// http → https → http: 2 段目が downgrade
	c = Chain{Hops: []Hop{hop("http://a.test/", 301, "https://a.test/"), hop("https://a.test/", 301, "http://a.test/y"), hop("http://a.test/y", 200, "")}, Stop: StopFinal}
	if fs := Check(c, 3, finding.Probe("http://a.test/")); ids(fs) != "redirects.downgrade@https://a.test/" {
		t.Fatalf("got %s", ids(fs))
	}
}

func TestLoopTooManyAndBadLocation(t *testing.T) {
	cases := []struct {
		name string
		c    Chain
		want string
		sev  finding.Severity
	}{
		{"loop A → B → A", Chain{Hops: []Hop{hop("https://a.test/", 301, "https://a.test/b"), hop("https://a.test/b", 301, "https://a.test/")}, Stop: StopLoop}, "redirects.loop@https://a.test/", finding.Fail},
		{"loop A → A", Chain{Hops: []Hop{hop("https://a.test/", 301, "https://a.test/")}, Stop: StopLoop}, "redirects.loop@https://a.test/", finding.Fail},
		{"too many", Chain{Hops: []Hop{hop("https://a.test/0", 301, "https://a.test/1"), hop("https://a.test/1", 301, "https://a.test/2"), hop("https://a.test/2", 301, "https://a.test/3"), hop("https://a.test/3", 301, "https://a.test/4")}, Stop: StopTooMany}, "redirects.too-many@https://a.test/0", finding.Warn},
		{"no Location", Chain{Hops: []Hop{{URL: "https://a.test/", Status: 302}}, Stop: StopBadLocation}, "redirects.bad-location@https://a.test/", finding.Fail},
		{"broken Location", Chain{Hops: []Hop{{URL: "https://a.test/", Status: 301, Location: "http://[::1"}}, Stop: StopBadLocation}, "redirects.bad-location@https://a.test/", finding.Fail},
	}
	for _, c := range cases {
		fs := Check(c.c, 3, start)
		if ids(fs) != c.want || fs[0].Severity != c.sev {
			t.Errorf("%s: got %s (%v), want %s (%s)", c.name, ids(fs), sevs(fs), c.want, c.sev)
		}
	}
	// 無い Location と壊れた Location は文が違う（CLAUDE.md §4: 何が起きたか）
	none := Check(Chain{Hops: []Hop{{URL: "https://a.test/", Status: 302}}, Stop: StopBadLocation}, 3, start)[0].Message
	broken := Check(Chain{Hops: []Hop{{URL: "https://a.test/", Status: 302, Location: "::"}}, Stop: StopBadLocation}, 3, start)[0].Message
	if !strings.Contains(none, "without a Location") || !strings.Contains(broken, "not a usable URL") {
		t.Fatalf("messages: %q / %q", none, broken)
	}
}

func sevs(fs []finding.Finding) []finding.Severity {
	var out []finding.Severity
	for _, f := range fs {
		out = append(out, f.Severity)
	}
	return out
}

func TestTemporaryAndLoopTogether(t *testing.T) {
	c := Chain{Hops: []Hop{hop("https://a.test/", 302, "https://a.test/b"), hop("https://a.test/b", 302, "https://a.test/")}, Stop: StopLoop}
	fs := Check(c, 3, start)
	if ids(fs) != "redirects.loop@https://a.test/ redirects.temporary@https://a.test/ redirects.temporary@https://a.test/b" {
		t.Fatalf("got %s (fail first, then by rule, then subject)", ids(fs))
	}
}

// 開始 URL 自身が http なら、found_on は start。
func TestNoHTTPSOnAnHTTPStartNamesTheStart(t *testing.T) {
	c := Chain{Hops: []Hop{hop("http://a.test/", 200, "")}, Stop: StopFinal}
	fs := NoHTTPS(c, "http://a.test/", finding.Start("http://a.test/"))
	if len(fs) != 1 || fs[0].FoundOn[0].Kind != finding.KindStart || fs[0].Subject != "http://a.test/" {
		t.Fatalf("%+v", fs)
	}
}

func TestEmptyChainGivesNothing(t *testing.T) {
	if fs := Check(Chain{}, 3, start); len(fs) != 0 {
		t.Fatal(fs)
	}
	if fs := NoHTTPS(Chain{}, "http://a.test/", finding.Probe("http://a.test/")); len(fs) != 0 {
		t.Fatal(fs)
	}
}

// docs/test-perspectives.md §3.2: http が https に飛ばない → 警告。鎖のどこかで https に着けばよい。
func TestNoHTTPS(t *testing.T) {
	probe := "http://a.test/"
	cases := []struct {
		name string
		c    Chain
		want bool
	}{
		{"answers 200 on http", Chain{Hops: []Hop{hop(probe, 200, "")}, Stop: StopFinal}, true},
		{"redirects within http", Chain{Hops: []Hop{hop(probe, 301, "http://www.a.test/"), hop("http://www.a.test/", 200, "")}, Stop: StopFinal}, true},
		{"redirects to https", Chain{Hops: []Hop{hop(probe, 301, "https://a.test/"), hop("https://a.test/", 200, "")}, Stop: StopFinal}, false},
		{"reaches https after one http hop", Chain{Hops: []Hop{hop(probe, 301, "http://www.a.test/"), hop("http://www.a.test/", 301, "https://www.a.test/"), hop("https://www.a.test/", 200, "")}, Stop: StopFinal}, false},
		{"too many, all http", Chain{Hops: []Hop{hop(probe, 301, "http://a.test/1"), hop("http://a.test/1", 301, "http://a.test/2")}, Stop: StopTooMany}, true},
		{"404 on http", Chain{Hops: []Hop{hop(probe, 404, "")}, Stop: StopFinal}, true},
	}
	for _, c := range cases {
		fs := NoHTTPS(c.c, probe, finding.Probe(probe))
		if (len(fs) == 1) != c.want {
			t.Errorf("%s: got %s, want finding=%v", c.name, ids(fs), c.want)
			continue
		}
		if c.want && (fs[0].Rule != "redirects.no-https" || fs[0].Subject != probe || fs[0].FoundOn[0].Kind != finding.KindProbe || fs[0].Severity != finding.Warn) {
			t.Errorf("%s: finding = %+v", c.name, fs[0])
		}
	}
}
