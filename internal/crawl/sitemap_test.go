package crawl

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/testsupport"
)

func sopt(robots *RobotsResult) SitemapOptions {
	return SitemapOptions{MaxURLs: 50000, MaxRedirects: 3, Robots: robots, Now: testsupport.Clock(testsupport.Now), Sleep: func(time.Duration) {}}
}

func robotsOf(t *testing.T, f *fetch.Client, origin string) *RobotsResult {
	t.Helper()
	return ReadRobots(context.Background(), f, origin, Options{Product: "tenken", Now: testsupport.Clock(testsupport.Now), Sleep: func(time.Duration) {}})
}

func urlset(locs ...string) string {
	b := `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`
	for _, l := range locs {
		b += "<url><loc>" + l + "</loc><lastmod>2026-01-01</lastmod></url>"
	}
	return b + "</urlset>"
}

func sitemapindex(locs ...string) string {
	b := `<?xml version="1.0" encoding="UTF-8"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`
	for _, l := range locs {
		b += "<sitemap><loc>" + l + "</loc></sitemap>"
	}
	return b + "</sitemapindex>"
}

func xmlHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(body))
	}
}

func entryList(res SitemapResult) string {
	var out []string
	for _, e := range res.Entries {
		st := "?"
		switch {
		case e.External:
			st = "external"
		case e.Disallowed:
			st = "disallowed"
		case e.Err != nil:
			st = "unreachable"
		case e.Chain.Stop == StopFinal:
			st = fmt.Sprint(e.Chain.Final.Status)
		default:
			st = string(e.Chain.Stop)
		}
		out = append(out, strings.TrimPrefix(e.Loc, "http://")[strings.Index(strings.TrimPrefix(e.Loc, "http://"), "/"):]+"="+st)
	}
	sort.Strings(out) // Loc の順はホスト（テストのポート）に依るので、並べ直して比べる
	return strings.Join(out, " ")
}

// docs/test-perspectives.md §3.5: Sitemap: の行が無ければ /sitemap.xml。URL は 200 なら問題なし、404 は失敗、リダイレクトは警告。
// 別オリジンと robots の禁止は取りに行かない。
func TestSitemapEntriesAreCheckedByStatusOnly(t *testing.T) {
	s := newSite(t, map[string]string{"/ok": `<a href="/deeper">d</a>`, "/deeper": "x"})
	ext := newSite(t, map[string]string{"/": "x"})
	s.robots = "User-agent: *\nDisallow: /private/\n"
	s.handler["/sitemap.xml"] = xmlHandler(urlset(s.url("/ok"), s.url("/missing"), s.url("/moved"), s.url("/private/x"), ext.url("/"), s.url("/ok")))
	s.handler["/moved"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", 301) }
	f := newClient(t)
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(robotsOf(t, f, Origin(Normalize(s.url("/"))))))
	if res.Missing || res.Declared || len(res.Files) != 1 || res.Files[0].Problem != "" || res.Files[0].URLs != 6 {
		t.Fatalf("%+v", res)
	}
	if got := entryList(res); got != "/=external /missing=404 /moved=200 /ok=200 /ok=200 /private/x=disallowed" {
		t.Fatalf("entries %s", got)
	}
	if res.External != 1 || res.Disallowed != 1 || s.hitCount("/private/x") != 0 || ext.hitCount("/") != 0 {
		t.Fatalf("external %d disallowed %d hits %d %d", res.External, res.Disallowed, s.hitCount("/private/x"), ext.hitCount("/"))
	}
	// URL はステータスを見るだけで、ページとして解析・巡回しない。同じ URL が 2 度載っていても 1 回（/moved の行き先としての 1 回は別）
	if s.hitCount("/deeper") != 0 || s.hitCount("/ok") > 2 {
		t.Fatalf("/deeper %d /ok %d", s.hitCount("/deeper"), s.hitCount("/ok"))
	}
	for _, e := range res.Entries {
		if strings.HasSuffix(e.Loc, "/moved") && len(e.Chain.Hops) != 2 {
			t.Fatalf("moved chain %+v", e.Chain)
		}
	}
}

// robots.txt の Sitemap: の行があればそれを使う（複数可）。/sitemap.xml は見ない。
func TestSitemapsDeclaredInRobots(t *testing.T) {
	s := newSite(t, map[string]string{"/a": "x", "/b": "x"})
	s.robots = fmt.Sprintf("Sitemap: %s\nSitemap: %s\nUser-agent: *\nDisallow:\n", s.url("/maps/one.xml"), s.url("/maps/two.xml"))
	s.handler["/maps/one.xml"] = xmlHandler(urlset(s.url("/a")))
	s.handler["/maps/two.xml"] = xmlHandler(urlset(s.url("/b")))
	s.handler["/sitemap.xml"] = xmlHandler(urlset(s.url("/never")))
	f := newClient(t)
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(robotsOf(t, f, Origin(Normalize(s.url("/"))))))
	if !res.Declared || len(res.Files) != 2 || len(res.Entries) != 2 || s.hitCount("/sitemap.xml") != 0 {
		t.Fatalf("%+v hits %d", res, s.hitCount("/sitemap.xml"))
	}
}

// sitemap.xml が無い（404）→ Missing。Sitemap: で宣言された sitemap が 404 なら Missing ではない（宣言が壊れている）。
func TestSitemapMissing(t *testing.T) {
	s := newSite(t, nil)
	f := newClient(t)
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(robotsOf(t, f, Origin(Normalize(s.url("/"))))))
	if !res.Missing || len(res.Files) != 1 || res.Files[0].Status != 404 {
		t.Fatalf("%+v", res)
	}
	s.robots = fmt.Sprintf("Sitemap: %s\n", s.url("/declared.xml"))
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(robotsOf(t, f, Origin(Normalize(s.url("/"))))))
	if res.Missing || !res.Declared || res.Files[0].Status != 404 {
		t.Fatalf("%+v", res)
	}
}

// sitemap index（入れ子）を辿る。ループと深すぎる入れ子で止まらなくならない。gzip を読める。
func TestSitemapIndexNestingLoopsAndGzip(t *testing.T) {
	s := newSite(t, map[string]string{"/p": "x"})
	s.handler["/sitemap.xml"] = xmlHandler(sitemapindex(s.url("/a.xml"), s.url("/b.xml"), s.url("/sitemap.xml"), s.url("/loop.xml"), s.url("/deep0.xml")))
	s.handler["/a.xml"] = xmlHandler(urlset(s.url("/p")))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(urlset(s.url("/p"), s.url("/q"))))
	zw.Close()
	s.handler["/b.xml"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Write(gz.Bytes())
	}
	s.handler["/loop.xml"] = xmlHandler(sitemapindex(s.url("/sitemap.xml"), s.url("/loop.xml")))
	for i := 0; i < 20; i++ {
		s.handler[fmt.Sprintf("/deep%d.xml", i)] = xmlHandler(sitemapindex(s.url(fmt.Sprintf("/deep%d.xml", i+1))))
	}
	f := newClient(t)
	done := make(chan SitemapResult, 1)
	go func() {
		done <- ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(robotsOf(t, f, Origin(Normalize(s.url("/"))))))
	}()
	var res SitemapResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("nested sitemaps did not finish")
	}
	if got := entryList(res); got != "/p=200 /p=200 /q=404" {
		t.Fatalf("entries %s", got)
	}
	if !res.NestingHit {
		t.Fatalf("children deeper than MaxSitemapNesting were not read, and nothing says so: %+v", res)
	}
	if s.hitCount("/sitemap.xml") != 1 || s.hitCount("/loop.xml") != 1 || s.hitCount("/deep15.xml") != 0 || s.hitCount("/deep9.xml") != 1 {
		t.Fatalf("hits sitemap %d loop %d deep9 %d deep15 %d", s.hitCount("/sitemap.xml"), s.hitCount("/loop.xml"), s.hitCount("/deep9.xml"), s.hitCount("/deep15.xml"))
	}
	// 入れ子が浅ければ NestingHit は立たない
	s.handler["/sitemap.xml"] = xmlHandler(sitemapindex(s.url("/a.xml")))
	if shallow := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil)); shallow.NestingHit {
		t.Fatalf("shallow nesting flagged: %+v", shallow)
	}
}

// 壊れた XML は問題として報告し、読めたところまでの URL は確かめる。全体は止まらない。
func TestBrokenSitemapXML(t *testing.T) {
	cases := map[string]string{
		"not xml":        "hello",
		"wrong root":     `<?xml version="1.0"?><feed><loc>x</loc></feed>`,
		"unclosed":       `<urlset><url><loc>http://a.test/x</loc></url><url><loc>http://a.test/y`,
		"empty":          "",
		"binary garbage": "\x00\x01\x02\xff",
	}
	for name, body := range cases {
		s := newSite(t, map[string]string{"/x": "x"})
		s.handler["/sitemap.xml"] = xmlHandler(body)
		f := newClient(t)
		res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
		if len(res.Files) != 1 || res.Files[0].Problem == "" || res.Missing {
			t.Errorf("%s: %+v", name, res.Files)
		}
		if strings.Contains(res.Files[0].Problem, "panic") {
			t.Errorf("%s: %s", name, res.Files[0].Problem)
		}
	}
	// 途中まで読めた URL は確かめる（unclosed の場合、a.test は別オリジンなので外部として数える）
	s := newSite(t, map[string]string{"/x": "x"})
	s.handler["/sitemap.xml"] = xmlHandler(`<urlset><url><loc>` + s.url("/x") + `</loc></url><url><loc>` + s.url("/y"))
	f := newClient(t)
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	if res.Files[0].Problem == "" || entryList(res) != "/x=200" {
		t.Fatalf("%+v %s", res.Files[0], entryList(res))
	}
}

// 巨大な sitemap: --max-sitemap-urls まで読み、上限に当たったことを返す。
func TestSitemapURLLimit(t *testing.T) {
	var locs []string
	s := newSite(t, map[string]string{})
	for i := 0; i < 30; i++ {
		locs = append(locs, s.url(fmt.Sprintf("/u%02d", i)))
	}
	s.handler["/sitemap.xml"] = xmlHandler(urlset(locs...))
	f := newClient(t)
	for limit, want := range map[int]struct {
		n   int
		hit bool
	}{10: {10, true}, 30: {30, false}, 31: {30, false}} {
		o := sopt(nil)
		o.MaxURLs = limit
		res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
		if len(res.Entries) != want.n || res.LimitHit != want.hit {
			t.Errorf("limit %d: %d entries, hit %v", limit, len(res.Entries), res.LimitHit)
		}
	}
}

// 既に取りに行った URL は Known から取る（二度取りに行かない）。
func TestKnownURLsAreNotFetchedAgain(t *testing.T) {
	s := newSite(t, map[string]string{"/k": "x"})
	s.handler["/sitemap.xml"] = xmlHandler(urlset(s.url("/k")))
	f := newClient(t)
	o := sopt(nil)
	o.Known = func(u string) (Chain, error, bool) {
		if strings.HasSuffix(u, "/k") {
			return Chain{Hops: []Hop{{URL: u, Status: 200}}, Stop: StopFinal, Final: &fetch.Response{Status: 200}}, nil, true
		}
		return Chain{}, nil, false
	}
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
	if s.hitCount("/k") != 0 || len(res.Entries) != 1 || res.Entries[0].Chain.Final.Status != 200 {
		t.Fatalf("hits %d %+v", s.hitCount("/k"), res.Entries)
	}
}

// robots.txt が使えない（5xx）とき: sitemap の URL の確認は、同じオリジンでは行わない（全面禁止）。外部は数えるだけ。
func TestSitemapUnderUnavailableRobots(t *testing.T) {
	s := newSite(t, map[string]string{"/a": "x"})
	s.handler["/robots.txt"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }
	s.handler["/sitemap.xml"] = xmlHandler(urlset(s.url("/a")))
	f := newClient(t)
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(robotsOf(t, f, Origin(Normalize(s.url("/"))))))
	if entryList(res) != "/a=disallowed" || s.hitCount("/a") != 0 {
		t.Fatalf("%s hits %d", entryList(res), s.hitCount("/a"))
	}
}

// gzip の上限は展開後の大きさで、圧縮率では区切らない（XML の sitemap は 20 倍ほどに縮む）。超えたら tenken の上限だと言う。
func TestGzipBoundIsAbsoluteNotARatio(t *testing.T) {
	s := newSite(t, map[string]string{"/p": "x"})
	var locs []string
	for i := 0; i < 2000; i++ {
		locs = append(locs, s.url(fmt.Sprintf("/page/%04d", i)))
	}
	raw := urlset(locs...)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(raw))
	zw.Close()
	if ratio := len(raw) / gz.Len(); ratio < 12 {
		t.Fatalf("the fixture must compress more than 10x to exercise the bound; got %dx", ratio)
	}
	s.handler["/sitemap.xml"] = func(w http.ResponseWriter, r *http.Request) { w.Write(gz.Bytes()) }
	f := newClient(t)
	o := sopt(nil)
	o.Exclude = []*regexp.Regexp{regexp.MustCompile(`/page/`)} // 2000 回取りに行かない
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
	if len(res.Files) != 1 || res.Files[0].Problem != "" || res.Files[0].URLs != 2000 {
		t.Fatalf("%+v", res.Files)
	}
	// 展開後が上限を超える: zero の塊（圧縮率は 1000 倍を超える）
	var bomb bytes.Buffer
	zw = gzip.NewWriter(&bomb)
	zw.Write(bytes.Repeat([]byte{0}, MaxSitemapDecompressed+1))
	zw.Close()
	s.handler["/sitemap.xml"] = func(w http.ResponseWriter, r *http.Request) { w.Write(bomb.Bytes()) }
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
	if p := res.Files[0].Problem; !strings.Contains(p, "larger than 64 MiB when decompressed") || strings.Contains(p, "could not be decompressed") {
		t.Fatalf("problem %q", p)
	}
}

// --max-sitemap-urls にファイルの間で当たっても limits_hit。not_fetched は URL ごとに 1。--exclude は sitemap の URL にも効く。
func TestSitemapLimitAcrossFilesCountsAndExclude(t *testing.T) {
	s := newSite(t, map[string]string{})
	ext := newSite(t, map[string]string{"/": "x"})
	s.handler["/sitemap.xml"] = xmlHandler(sitemapindex(s.url("/a.xml"), s.url("/b.xml")))
	s.handler["/a.xml"] = xmlHandler(urlset(s.url("/1"), s.url("/2"), s.url("/3")))
	s.handler["/b.xml"] = xmlHandler(urlset(s.url("/4"), s.url("/5"), s.url("/6")))
	f := newClient(t)
	o := sopt(nil)
	o.MaxURLs = 3
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
	if !res.LimitHit || len(res.Entries) != 3 || s.hitCount("/b.xml") != 0 {
		t.Fatalf("hit %v entries %d b.xml hits %d", res.LimitHit, len(res.Entries), s.hitCount("/b.xml"))
	}
	s.robots = "User-agent: *\nDisallow: /private/\n"
	s.handler["/sitemap.xml"] = xmlHandler(urlset(ext.url("/"), ext.url("/"), s.url("/private/x"), s.url("/private/x"), s.url("/secret/1"), s.url("/secret/1"), s.url("/secret/2")))
	o = sopt(robotsOf(t, f, Origin(Normalize(s.url("/")))))
	o.Exclude = []*regexp.Regexp{regexp.MustCompile(`secret`)}
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
	if res.External != 1 || res.Disallowed != 1 || res.Excluded != 2 || s.hitCount("/secret/1") != 0 || s.hitCount("/secret/2") != 0 {
		t.Fatalf("external %d disallowed %d excluded %d hits %d %d", res.External, res.Disallowed, res.Excluded, s.hitCount("/secret/1"), s.hitCount("/secret/2"))
	}
	// sitemap 自身が --exclude に一致: 取りに行かず数える
	o.Exclude = []*regexp.Regexp{regexp.MustCompile(`sitemap\.xml`)}
	before := s.hitCount("/sitemap.xml")
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
	if res.Excluded != 1 || len(res.Entries) != 0 || !res.Files[0].Excluded || s.hitCount("/sitemap.xml") != before {
		t.Fatalf("%+v", res)
	}
}

// 文は tenken がしたこと・できなかったことを言い、サイトのせいにしない（CLAUDE.md §4-1）: 429 の sitemap、UTF-8 でない encoding。
func TestSitemapFileSentences(t *testing.T) {
	s := newSite(t, map[string]string{})
	s.handler["/sitemap.xml"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }
	f := newClient(t)
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	if fl := res.Files[0]; !fl.RateLimited || fl.Problem != "" || fl.RetryAfter != nil || s.hitCount("/sitemap.xml") != 2 {
		t.Fatalf("%+v hits %d", fl, s.hitCount("/sitemap.xml"))
	}
	s.handler["/sitemap.xml"] = func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "2"); w.WriteHeader(429) }
	if res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil)); res.Files[0].RetryAfter == nil || *res.Files[0].RetryAfter != 2 {
		t.Fatalf("%+v", res.Files[0])
	}
	// URL にならない <loc>: その sitemap の problem に数と例。ほかの URL は確かめる
	s.handler["/sitemap.xml"] = xmlHandler(urlset("not a url", s.url("/x"), "also::bad"))
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	if fl := res.Files[0]; fl.BadLocs != 2 || !strings.Contains(fl.Problem, `2 of 3 <loc> values are not URLs (the first is "not a url")`) || len(res.Entries) != 1 {
		t.Fatalf("%+v entries %d", fl, len(res.Entries))
	}
	// 上限で途中で止まっても、XML が壊れていても、数えた <loc> は言う
	s.handler["/sitemap.xml"] = xmlHandler(urlset("not a url", s.url("/1"), s.url("/2"), s.url("/3")))
	o := sopt(nil)
	o.MaxURLs = 2
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), o)
	if fl := res.Files[0]; !res.LimitHit || !strings.Contains(fl.Problem, "1 of 4 <loc> values are not URLs") {
		t.Fatalf("limit + bad loc: %+v", fl)
	}
	s.handler["/sitemap.xml"] = xmlHandler(`<urlset><url><loc>not a url</loc></url><url><loc>` + s.url("/x"))
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	if fl := res.Files[0]; !strings.Contains(fl.Problem, "not well-formed") || !strings.Contains(fl.Problem, "also 1 of 1 <loc> values are not URLs") {
		t.Fatalf("broken xml + bad loc: %+v", fl)
	}
	s.handler["/sitemap.xml"] = xmlHandler(`<?xml version="1.0" encoding="ISO-8859-1"?><urlset><url><loc>` + s.url("/x") + `</loc></url></urlset>`)
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	if p := res.Files[0].Problem; !strings.Contains(p, `declares encoding "ISO-8859-1"`) || strings.Contains(p, "CharsetReader") {
		t.Fatalf("problem %q", p)
	}
	s.handler["/sitemap.xml"] = xmlHandler(`<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>` + s.url("/x") + `</loc></url></urlset>`)
	if res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil)); res.Files[0].Problem != "" || len(res.Entries) != 1 {
		t.Fatalf("utf-8 declared: %+v", res.Files[0])
	}
}

// 相手が本文の途中で接続を切った sitemap: 読めたところまでの URL を確かめ、文は --max-body のせいにしない。index の bad loc の文。
func TestIncompleteSitemapAndIndexBadLocSentences(t *testing.T) {
	s := newSite(t, map[string]string{"/a": "x"})
	full := []byte(urlset(s.url("/a"), s.url("/b"), s.url("/c")))
	s.handler["/sitemap.xml"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(full)))
		w.Header().Set("Content-Type", "application/xml")
		w.Write(full[:bytes.LastIndex(full, []byte("/c"))+1]) // /c の URL の途中で切れる
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		panic(http.ErrAbortHandler) // 接続を切る
	}
	f := newClient(t)
	res := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	fl := res.Files[0]
	if !fl.Incomplete || fl.Truncated || !strings.Contains(fl.Problem, "the connection ended before the whole sitemap arrived (the server closed the connection)") || strings.Contains(fl.Problem, "max-body") || len(res.Entries) != 2 {
		t.Fatalf("%+v entries %d", fl, len(res.Entries))
	}
	s.handler["/sitemap.xml"] = xmlHandler(sitemapindex("not a url", s.url("/child.xml")))
	s.handler["/child.xml"] = xmlHandler(urlset(s.url("/a")))
	res = ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	var p string
	for _, fl := range res.Files { // Files は URL の順（/child.xml が先）
		if strings.HasSuffix(fl.URL, "/sitemap.xml") {
			p = fl.Problem
		}
	}
	if !strings.HasSuffix(p, "the others were read as sitemaps") || len(res.Entries) != 1 {
		t.Fatalf("%q entries %d", p, len(res.Entries))
	}
}

// sitemap の URL の確認でも本文は読まない（HEAD が 405 で GET に落ちても、ステータスが届いたら切る）。
func TestSitemapURLCheckDoesNotReadBodies(t *testing.T) {
	released := make(chan struct{})
	defer close(released)
	s := newSite(t, map[string]string{})
	s.handler["/big"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-released
	}
	s.handler["/sitemap.xml"] = xmlHandler(urlset(s.url("/big")))
	f := newClient(t)
	done := make(chan SitemapResult, 1)
	go func() { done <- ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil)) }()
	select {
	case res := <-done:
		if e := res.Entries[0]; e.Err != nil || e.Chain.Final.Status != 200 || e.Chain.Final.Body != nil || s.hitCount("/big") != 2 {
			t.Fatalf("%+v hits %d", e, s.hitCount("/big"))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sitemap check waited for a body it does not need")
	}
}

// docs/test-perspectives.md §3.5: sitemap の URL が HEAD にだけ 4xx を返すなら GET でやり直し、GET の結果で判断する（巡回のリンクと同じ）。
func TestSitemapHeadOnly4xxIsRetriedWithGet(t *testing.T) {
	released := make(chan struct{})
	defer close(released)
	s := newSite(t, map[string]string{})
	s.handler["/profile"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-released // やり直しの GET の本文は送らない
	}
	s.handler["/gone"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }
	s.handler["/sitemap.xml"] = xmlHandler(urlset(s.url("/profile"), s.url("/gone")))
	f := newClient(t)
	done := make(chan SitemapResult, 1)
	go func() { done <- ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil)) }()
	select {
	case res := <-done:
		if got := entryList(res); got != "/gone=404 /profile=200" {
			t.Fatalf("entries %s", got)
		}
		if s.hitCount("/profile") != 2 || s.hitCount("/gone") != 2 {
			t.Fatalf("hits profile %d gone %d (HEAD then GET for each)", s.hitCount("/profile"), s.hitCount("/gone"))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the GET retry waited for a body")
	}
}

// docs/test-perspectives.md §2.2: sitemap のファイル自体が loopback / プライベートのアドレスか localhost にあるなら、
// 取りに行かず private に数える（robots.txt の Sitemap: の行、index の子、/sitemap.xml からのリダイレクトの先）。
// localhost はテストのサーバ（127.0.0.1）に解決されるので、取りに行けば hitCount に出る。
func TestSitemapFilesAtPrivateAddressesAreNotFetched(t *testing.T) {
	other := newSite(t, map[string]string{})
	other.handler["/s.xml"] = xmlHandler(urlset("http://x.test/"))
	priv := "http://localhost:" + strings.TrimPrefix(other.url(""), "http://127.0.0.1:") + "/s.xml"
	for _, c := range []struct {
		name string
		set  func(s *site)
	}{
		{"robots.txt の Sitemap: の行", func(s *site) { s.robots = fmt.Sprintf("Sitemap: %s\nUser-agent: *\nDisallow:\n", priv) }},
		{"index の子", func(s *site) {
			s.handler["/sitemap.xml"] = xmlHandler(`<?xml version="1.0"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>` + priv + `</loc></sitemap></sitemapindex>`)
		}},
		{"/sitemap.xml からのリダイレクトの先", func(s *site) {
			s.handler["/sitemap.xml"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, priv, 302) }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := other.hitCount("/s.xml")
			s := newSite(t, map[string]string{})
			c.set(s)
			f := newClient(t)
			o := Origin(Normalize(s.url("/")))
			res := ReadSitemaps(context.Background(), f, o, sopt(robotsOf(t, f, o)))
			if got := other.hitCount("/s.xml") - before; got != 0 {
				t.Fatalf("the sitemap at localhost was fetched %d time(s)", got)
			}
			if res.Private != 1 || len(res.Entries) != 0 || res.Missing {
				t.Fatalf("private %d entries %d missing %v", res.Private, len(res.Entries), res.Missing)
			}
			var p *SitemapFile
			for i := range res.Files {
				if res.Files[i].Private {
					p = &res.Files[i]
				}
			}
			if p == nil || p.Err != nil || p.Problem != "" || p.Status != 0 {
				t.Fatalf("files %+v", res.Files)
			}
		})
	}
}
