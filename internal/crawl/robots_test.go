package crawl

import (
	"strings"
	"testing"
	"time"
)

// RFC 9309 §2.2.2: いちばん長く一致する規則が勝ち、同じ長さなら allow が勝つ。§2.2.3: * と $。
func TestRobotsRules(t *testing.T) {
	r := ParseRobots([]byte(`
User-agent: *
Disallow: /private/
Allow: /private/public
Disallow: /*.pdf$
Disallow: /tmp*
Allow: /tmp/keep
Disallow: /exact$
Disallow:
`), "tenken")
	for path, want := range map[string]bool{
		"/":                    true,
		"/private/":            false,
		"/private/x":           false,
		"/private/public":      true,
		"/private/publication": true, // Allow の方が長く一致
		"/doc.pdf":             false,
		"/doc.pdf?x=1":         true, // $ は終わり
		"/doc.pdfx":            true,
		"/tmp":                 false,
		"/tmp/anything":        false,
		"/tmp/keep":            true,
		"/exact":               false,
		"/exactly":             true,
	} {
		if got := r.Allowed(path); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", path, got, want)
		}
	}
}

// RFC 9309 §2.2.1: 製品名にいちばん具体的に一致するグループ。無ければ *。それも無ければ制限なし。
func TestRobotsGroupSelection(t *testing.T) {
	body := []byte(`
User-agent: *
Disallow: /all/

User-agent: ten
Disallow: /ten/

User-agent: TENKEN
Disallow: /tenken/

User-agent: other
Disallow: /
`)
	r := ParseRobots(body, "tenken/0.1")
	if r.Allowed("/tenken/x") || !r.Allowed("/ten/x") || !r.Allowed("/all/x") || !r.matched {
		t.Fatalf("the most specific group (tenken) must be chosen: %+v", r)
	}
	r = ParseRobots(body, "someone-else")
	if r.Allowed("/all/x") || !r.Allowed("/tenken/x") || r.matched {
		t.Fatalf("the * group must be chosen: %+v", r)
	}
	r = ParseRobots([]byte("User-agent: other\nDisallow: /\n"), "tenken")
	if !r.Allowed("/anything") {
		t.Fatal("no matching group and no *: everything is allowed")
	}
	if !ParseRobots(nil, "tenken").Allowed("/") || !(*Robots)(nil).Allowed("/") {
		t.Fatal("empty or missing robots.txt allows everything")
	}
}

// 連続する User-agent 行は 1 つのグループ（RFC 9309 §2.1）。
func TestRobotsConsecutiveAgentsShareAGroup(t *testing.T) {
	r := ParseRobots([]byte("User-agent: a\nUser-agent: tenken\nDisallow: /x\n\nUser-agent: *\nDisallow: /y\n"), "tenken")
	if r.Allowed("/x") || !r.Allowed("/y") {
		t.Fatalf("%+v", r)
	}
}

func TestRobotsCrawlDelayAndSitemaps(t *testing.T) {
	r := ParseRobots([]byte("Sitemap: https://a.test/s1.xml\nUser-agent: *\nCrawl-delay: 2.5\nSitemap: https://a.test/s2.xml\nUser-agent: tenken\nCrawl-delay: 1\n"), "tenken")
	if r.CrawlDelay() != time.Second {
		t.Fatalf("delay %v (the chosen group's)", r.CrawlDelay())
	}
	if got := strings.Join(r.Sitemaps(), ","); got != "https://a.test/s1.xml,https://a.test/s2.xml" {
		t.Fatalf("sitemaps %s", got)
	}
	// 0 以下・1 時間以上（MaxCrawlDelay）・ごみの Crawl-delay は無視。1 時間の手前は使う
	for _, v := range []string{"-1", "0", "3600", "99999", "soon"} {
		if ParseRobots([]byte("User-agent: *\nCrawl-delay: "+v+"\n"), "tenken").CrawlDelay() != 0 {
			t.Errorf("Crawl-delay %s must be ignored", v)
		}
	}
	if got := ParseRobots([]byte("User-agent: *\nCrawl-delay: 3599\n"), "tenken").CrawlDelay(); got != 3599*time.Second {
		t.Errorf("Crawl-delay 3599 gave %v, want 59m59s (below MaxCrawlDelay)", got)
	}
}

// 壊れた行、コメント、空白、大文字小文字、CRLF（RFC 9309 §2.2.4: 解釈できない行は無視）。
func TestRobotsTolerantParsing(t *testing.T) {
	r := ParseRobots([]byte("# comment\r\nUSER-AGENT : *\r\n  disallow :  /a   # trailing\r\nnonsense line\r\nDisallow /nocolon\r\n:\r\nAllow: \r\n"), "tenken")
	if r.Allowed("/a") || !r.Allowed("/nocolon") || !r.Allowed("/b") {
		t.Fatalf("%+v", r)
	}
}

// 敵対的なパターン: * が何百もあっても、終わる。
func TestRobotsPatternWithManyStarsTerminates(t *testing.T) {
	pattern := "/" + strings.Repeat("a*", 500) + "b$"
	path := "/" + strings.Repeat("a", 5000)
	done := make(chan bool, 1)
	go func() { done <- patternMatch(pattern, path) }()
	select {
	case got := <-done:
		if got {
			t.Fatal("must not match (no b at the end)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pattern matching did not finish")
	}
	if !patternMatch("/*.php$", "/x/y.php") || patternMatch("/*.php$", "/x/y.php?x") || !patternMatch("/a*b*c", "/a1b2c3") || !patternMatch("/a*c$", "/abc") || patternMatch("/a*c$", "/acb") {
		t.Fatal("pattern semantics")
	}
}

// RFC 9309 §2.2.1: 同じ名前のグループが複数あれば規則を合わせる。* のグループも複数なら合わせる。§2.5: 500 KiB より後ろは読まない。
func TestRobotsGroupsAreCombinedAndTheSizeIsBounded(t *testing.T) {
	r := ParseRobots([]byte("User-agent: tenken\nDisallow: /a\n\nUser-agent: tenken\nDisallow: /b\nCrawl-delay: 3\n\nUser-agent: *\nDisallow: /c\n\nUser-agent: *\nDisallow: /d\n"), "tenken")
	if r.Allowed("/a") || r.Allowed("/b") || !r.Allowed("/c") || r.CrawlDelay() != 3*time.Second {
		t.Fatalf("%+v", r)
	}
	if r := ParseRobots([]byte("User-agent: *\nDisallow: /c\n\nUser-agent: *\nDisallow: /d\n"), "tenken"); r.Allowed("/c") || r.Allowed("/d") {
		t.Fatalf("* groups combined: %+v", r)
	}
	big := []byte("User-agent: *\n" + strings.Repeat("Disallow: /aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", MaxRobotsBytes/40) + "Disallow: /late\n")
	if r := ParseRobots(big, "tenken"); !r.Allowed("/late") {
		t.Fatal("a rule after the 500 KiB limit must not be read")
	}
}
