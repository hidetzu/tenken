package sitemap

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/finding"
)

func ids(fs []finding.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule+"@"+f.Subject)
	}
	return strings.Join(out, " ")
}

func byRule(fs []finding.Finding, rule string) *finding.Finding {
	for i := range fs {
		if fs[i].Rule == rule {
			return &fs[i]
		}
	}
	return nil
}

var in10 = Input{Timeout: 10 * time.Second}

// docs/test-perspectives.md §3.5: robots.txt の 4 つの状態と、見る/見ないの決まり。
func TestRobotsStates(t *testing.T) {
	ra := 3
	cases := []struct {
		name string
		r    *Robots
		want string
		sev  finding.Severity
	}{
		{"ok", &Robots{State: "ok", URL: "https://a.test/robots.txt"}, "", ""},
		{"404", &Robots{State: "missing", URL: "https://a.test/robots.txt", Status: 404}, "sitemap.robots-missing@https://a.test/robots.txt", finding.Info},
		{"403", &Robots{State: "missing", URL: "https://a.test/robots.txt", Status: 403}, "sitemap.robots-missing@https://a.test/robots.txt", finding.Info},
		{"503", &Robots{State: "broken", URL: "https://a.test/robots.txt", Status: 503}, "sitemap.robots-broken@https://a.test/robots.txt", finding.Warn},
		{"timeout", &Robots{State: "unreachable", URL: "https://a.test/robots.txt", Err: &fetch.UnreachableError{URL: "https://a.test/robots.txt", Reason: fetch.ReasonTimeout, Err: errors.New("x")}}, "sitemap.robots-unreachable@https://a.test/robots.txt", finding.Warn},
		{"429", &Robots{State: "rate-limited", URL: "https://a.test/robots.txt", RetryAfter: &ra}, "sitemap.robots-rate-limited@https://a.test/robots.txt", finding.Warn},
		{"bad redirect: unreachable with reason redirect", &Robots{State: "bad-redirect", URL: "https://a.test/robots.txtx", FirstURL: "https://a.test/robots.txt", BadStop: "too-many"}, "sitemap.robots-unreachable@https://a.test/robots.txtx", finding.Warn},
		{"not asked (sitemap skipped)", nil, "", ""},
	}
	for _, c := range cases {
		in := in10
		in.Robots = c.r
		fs := Check(in)
		if ids(fs) != c.want {
			t.Errorf("%s: got %s, want %s", c.name, ids(fs), c.want)
			continue
		}
		if c.want != "" && (fs[0].Severity != c.sev || len(fs[0].FoundOn) != 0) {
			t.Errorf("%s: severity %s found_on %v", c.name, fs[0].Severity, fs[0].FoundOn)
		}
	}
	in := in10
	in.Robots = &Robots{State: "broken", URL: "https://a.test/robots.txt", Status: 503}
	if f := Check(in)[0]; f.Detail["status"] != 503 {
		t.Fatalf("broken detail %v", f.Detail)
	}
	in.Robots = &Robots{State: "unreachable", URL: "https://a.test/robots.txt", Err: &fetch.UnreachableError{URL: "https://a.test/robots.txt", Reason: fetch.ReasonTimeout, Err: errors.New("x")}}
	if f := Check(in)[0]; f.Detail["reason"] != "timeout" || !strings.Contains(f.Message, "no answer within 10s") {
		t.Fatalf("unreachable %v %q", f.Detail, f.Message)
	}
	in.Robots = &Robots{State: "bad-redirect", URL: "https://a.test/robots.txt", FirstURL: "https://a.test/robots.txt", BadStop: "loop"}
	if f := Check(in)[0]; f.Detail["reason"] != "redirect" || !strings.Contains(f.Message, "redirected in a loop") {
		t.Fatalf("bad-redirect %v %q", f.Detail, f.Message)
	}
	in.Robots = &Robots{State: "rate-limited", URL: "https://a.test/robots.txt"}
	if f := Check(in)[0]; f.Detail["retry_after_seconds"] != nil {
		t.Fatalf("rate-limited without Retry-After: %v", f.Detail)
	}
	in.Robots = &Robots{State: "rate-limited", URL: "https://a.test/robots.txt", RetryAfter: &ra}
	if f := Check(in)[0]; f.Detail["retry_after_seconds"] != 3 {
		t.Fatalf("rate-limited with Retry-After: %v", f.Detail)
	}
}

// sitemap が無い（info）、壊れている（fail、problem）、宣言された sitemap が 4xx / 5xx・届かない（invalid）。
func TestSitemapFiles(t *testing.T) {
	in := in10
	in.Missing = true
	in.Files = []File{{URL: "https://a.test/sitemap.xml", Status: 404}}
	fs := Check(in)
	if ids(fs) != "sitemap.missing@https://a.test/sitemap.xml" || fs[0].Severity != finding.Info || len(fs[0].FoundOn) != 0 {
		t.Fatalf("missing: %s %+v", ids(fs), fs)
	}
	in = in10
	in.Declared = true
	in.Files = []File{
		{URL: "https://a.test/one.xml", Status: 200, Problem: "the XML is not well-formed at line 3: unexpected EOF"},
		{URL: "https://a.test/two.xml", Status: 500},
		{URL: "https://a.test/three.xml", Err: &fetch.UnreachableError{URL: "https://a.test/three.xml", Reason: fetch.ReasonConnect, Err: errors.New("x")}},
		{URL: "https://a.test/four.xml", Status: 200},
		{URL: "https://a.test/five.xml", RateLimited: true},
	}
	fs = Check(in)
	if ids(fs) != "sitemap.invalid@https://a.test/one.xml sitemap.invalid@https://a.test/three.xml sitemap.invalid@https://a.test/two.xml sitemap.rate-limited@https://a.test/five.xml" {
		t.Fatalf("invalid: %s", ids(fs))
	}
	if rl := fs[3]; rl.Severity != finding.Warn || rl.Detail["retry_after_seconds"] != nil || len(rl.FoundOn) != 0 || !strings.Contains(rl.Message, "not a broken sitemap") {
		t.Fatalf("rate-limited: %+v", rl)
	}
	ra := 9
	in.Files = []File{{URL: "https://a.test/five.xml", RateLimited: true, RetryAfter: &ra}}
	if f := Check(in)[0]; f.Detail["retry_after_seconds"] != 9 {
		t.Fatalf("retry_after: %v", f.Detail)
	}
	fs = fs[:3]
	for _, f := range fs {
		if f.Severity != finding.Fail || f.Detail["problem"] == "" || len(f.FoundOn) != 0 {
			t.Fatalf("%+v", f)
		}
		if strings.Contains(f.Message, "dial tcp") {
			t.Fatalf("library text: %q", f.Message)
		}
	}
}

// docs/test-perspectives.md §3.5: 200 は問題なし、404 は失敗、リダイレクトは警告、別オリジンと禁止は警告（取りに行かない）、届かないは失敗。
func TestSitemapEntries(t *testing.T) {
	sm := "https://a.test/sitemap.xml"
	in := in10
	in.Files = []File{{URL: sm, Status: 200}}
	in.Entries = []Entry{
		{Loc: "https://a.test/ok", URL: "https://a.test/ok", Sitemap: sm, Final: true, Status: 200, Chain: []map[string]any{{"url": "https://a.test/ok", "status": 200}}},
		{Loc: "https://a.test/gone", URL: "https://a.test/gone", Sitemap: sm, Final: true, Status: 404, Chain: []map[string]any{{"url": "https://a.test/gone", "status": 404}}},
		{Loc: "https://a.test/moved", URL: "https://a.test/moved", Sitemap: sm, Final: true, Status: 200, Redirected: true, Chain: []map[string]any{{"url": "https://a.test/moved", "status": 301}, {"url": "https://a.test/new", "status": 200}}},
		{Loc: "https://a.test/moved-gone", URL: "https://a.test/moved-gone", Sitemap: sm, Final: true, Status: 404, Redirected: true, Chain: []map[string]any{{"url": "https://a.test/moved-gone", "status": 301}, {"url": "https://a.test/x", "status": 404}}},
		{Loc: "https://b.test/", URL: "https://b.test/", Sitemap: sm, External: true},
		{Loc: "https://a.test/private/x", URL: "https://a.test/private/x", Sitemap: sm, Disallowed: true},
		{Loc: "https://a.test/slow", URL: "https://a.test/slow", Sitemap: sm, Err: &fetch.UnreachableError{URL: "https://a.test/slow", Reason: fetch.ReasonTimeout, Err: errors.New("x")}},
		{Loc: "not a url", URL: "", Sitemap: sm},
		{Loc: "https://a.test/secret", URL: "https://a.test/secret", Sitemap: sm, Excluded: true},
		{Loc: "https://a.test/busy", URL: "https://a.test/busy", Sitemap: sm, RateLimit: &RateLimit{Retried: true, RetryAfterReadable: true, RetryAfterAsked: 7 * time.Second}},
		{Loc: "https://a.test/busy2", URL: "https://a.test/busy2", Sitemap: sm, RateLimit: &RateLimit{}},
		{Loc: "https://a.test/gone", URL: "https://a.test/gone", Sitemap: "https://a.test/other.xml", Final: true, Status: 404, Chain: []map[string]any{{"url": "https://a.test/gone", "status": 404}}},
	}
	fs := Check(in)
	want := "sitemap.url-broken@https://a.test/gone sitemap.url-broken@https://a.test/moved-gone sitemap.url-unreachable@https://a.test/slow sitemap.url-disallowed@https://a.test/private/x sitemap.url-external@https://b.test/ sitemap.url-rate-limited@https://a.test/busy sitemap.url-rate-limited@https://a.test/busy2 sitemap.url-redirects@https://a.test/moved"
	if ids(fs) != want {
		t.Fatalf("got  %s\nwant %s", ids(fs), want)
	}
	gone := byRule(fs, "sitemap.url-broken")
	if gone.Detail["status"] != 404 || len(gone.Detail["chain"].([]map[string]any)) != 0 || len(gone.FoundOn) != 2 || gone.FoundOn[0].Kind != finding.KindSitemap || gone.FoundOn[0].Loc != "https://a.test/gone" {
		t.Fatalf("gone: %+v", gone)
	}
	if mg := fs[1]; len(mg.Detail["chain"].([]map[string]any)) != 2 || !strings.Contains(mg.Message, "redirects to https://a.test/x, which returned 404") {
		t.Fatalf("moved-gone: %+v", mg)
	}
	if u := byRule(fs, "sitemap.url-unreachable"); u.Detail["reason"] != "timeout" || u.Severity != finding.Fail {
		t.Fatalf("unreachable: %+v", u)
	}
	if rl := byRule(fs, "sitemap.url-rate-limited"); rl.Detail["retry_after_seconds"] != 7 || rl.Severity != finding.Warn || fs[6].Detail["retry_after_seconds"] != nil {
		t.Fatalf("rate-limited: %+v / %+v", rl, fs[6])
	}
	for _, rule := range []string{"sitemap.url-redirects", "sitemap.url-disallowed", "sitemap.url-external"} {
		if f := byRule(fs, rule); f.Severity != finding.Warn || len(f.Detail) != 0 || f.FoundOn[0].Sitemap != sm {
			t.Fatalf("%s: %+v", rule, f)
		}
	}
}

// robots.txt が使えないときの url-disallowed は「robots.txt で禁止」とは言わない（Disallow の行があるのではない）。--exclude の sitemap は指摘にならない。
func TestDisallowedUnderUnusableRobotsAndExcludedFile(t *testing.T) {
	sm := "https://a.test/sitemap.xml"
	e := Entry{Loc: "https://a.test/p", URL: "https://a.test/p", Sitemap: sm, Disallowed: true}
	in := in10
	in.Robots = &Robots{State: "broken", URL: "https://a.test/robots.txt", Status: 503}
	in.Files = []File{{URL: sm, Status: 200}, {URL: "https://a.test/other.xml", Excluded: true}}
	in.Entries = []Entry{e}
	fs := Check(in)
	if ids(fs) != "sitemap.robots-broken@https://a.test/robots.txt sitemap.url-disallowed@https://a.test/p" {
		t.Fatalf("%s", ids(fs))
	}
	if m := fs[1].Message; strings.Contains(m, "disallowed by robots.txt") || !strings.Contains(m, "could not be used") {
		t.Fatalf("%q", m)
	}
	in.Robots = &Robots{State: "ok", URL: "https://a.test/robots.txt"}
	if m := Check(in)[0].Message; !strings.Contains(m, "disallowed by robots.txt") {
		t.Fatalf("%q", m)
	}
}
