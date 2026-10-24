package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/testsupport"
)

// site はテスト用のサイト: パス → 本文（HTML）かハンドラ。
type site struct {
	pages   map[string]string
	handler map[string]http.HandlerFunc
	robots  string
	hits    sync.Map // パス → 回数
	srv     *httptest.Server
}

func newSite(t *testing.T, pages map[string]string) *site {
	t.Helper()
	s := &site{pages: pages, handler: map[string]http.HandlerFunc{}}
	s.srv = testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := s.hits.LoadOrStore(r.URL.Path, new(int32))
		atomic.AddInt32(n.(*int32), 1)
		if h, ok := s.handler[r.URL.Path]; ok {
			h(w, r)
			return
		}
		if r.URL.Path == "/robots.txt" {
			if s.robots == "" {
				w.WriteHeader(404)
				return
			}
			w.Write([]byte(s.robots))
			return
		}
		body, ok := s.pages[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == "HEAD" {
			return
		}
		w.Write([]byte(body))
	}))
	return s
}

func (s *site) url(p string) string { return s.srv.URL + p }

func (s *site) hitCount(p string) int {
	n, ok := s.hits.Load(p)
	if !ok {
		return 0
	}
	return int(atomic.LoadInt32(n.(*int32)))
}

func newClient(t *testing.T) *fetch.Client {
	t.Helper()
	c := fetch.New(fetch.Options{Timeout: 3 * time.Second, MaxBody: 1 << 20, Now: testsupport.Clock(testsupport.Now), UserAgent: "tenken/test"})
	t.Cleanup(c.Close)
	return c
}

func opt() Options {
	return Options{MaxDepth: 3, MaxPages: 200, MaxRedirects: 3, Concurrency: 4, Product: "tenken", FetchReferences: true, Now: testsupport.Clock(testsupport.Now), Sleep: func(time.Duration) {}}
}

// startPage は開始ページを取って Page にする（cli がすることの代わり）。
func startPage(t *testing.T, f fetch.Fetcher, u string) Page {
	t.Helper()
	// 開始 URL 自身の鎖は、名前を解決した結果がプライベートアドレスでも取る（cli と同じ。docs/cli-reference.md §5.2）
	ch, err := Follow(context.Background(), f, u, FollowOptions{MaxRedirects: 3, Now: testsupport.Clock(testsupport.Now), Sleep: func(time.Duration) {}, StartHost: NewStartHost(u)})
	if err != nil || ch.Stop != StopFinal {
		t.Fatalf("start: %v %s", err, ch.Stop)
	}
	return Page{URL: Normalize(ch.Hops[len(ch.Hops)-1].URL), Resp: *ch.Final}
}

func pageURLs(res Result) string {
	var out []string
	for _, p := range res.Pages {
		out = append(out, fmt.Sprintf("%d:%s", p.Depth, strings.TrimPrefix(p.URL, "http://")[strings.Index(strings.TrimPrefix(p.URL, "http://"), "/"):]))
	}
	return strings.Join(out, " ")
}

func linkOf(res Result, suffix string) *Link {
	for i := range res.Links {
		if strings.HasSuffix(res.Links[i].URL, suffix) {
			return &res.Links[i]
		}
	}
	return nil
}

// docs/test-perspectives.md §2.2: 同じオリジンだけを巡回し、深さの順に、同じ URL を二度取らない。
func TestCrawlFollowsSameOriginLinksBreadthFirst(t *testing.T) {
	s := newSite(t, map[string]string{
		"/":          `<a href="/a">a</a><a href="/b">b</a><a href="/a#x">a again</a><a href="/missing">m</a><img src="/pic.png">`,
		"/a":         `<a href="/a/deep">d</a><a href="/">home</a>`,
		"/b":         `<a href="/a">a</a>`,
		"/a/deep":    `<a href="/a/deeper">dd</a>`,
		"/a/deeper":  `<a href="/a/deepest">ddd</a>`,
		"/a/deepest": `fin`,
	})
	s.handler["/pic.png"] = func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Type", "image/png") }
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if got := pageURLs(res); got != "0:/ 1:/a 1:/b 2:/a/deep 3:/a/deeper" {
		t.Fatalf("pages %s (max-depth 3: /a/deepest is depth 4)", got)
	}
	if s.hitCount("/a") != 1 || s.hitCount("/") != 1 {
		t.Fatalf("/ fetched %d times, /a %d times; each URL once", s.hitCount("/"), s.hitCount("/a"))
	}
	if l := linkOf(res, "/missing"); l == nil || l.Chain.Final.Status != 404 || len(l.Locations) != 1 || l.Locations[0].Page != s.url("/") {
		t.Fatalf("missing link: %+v", l)
	}
	// 同じ壊れたリンクが複数のページにあれば、1 件で場所を全部持つ（TEST-PERSPECTIVES §3.1）: /a と /b は深さが違っても集まる
	s.pages["/b"] += `<a href="/missing">m</a>`
	s.pages["/a/deep"] += `<a href="/missing">m</a>`
	res = Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if l := linkOf(res, "/missing"); l == nil || len(l.Locations) != 3 {
		t.Fatalf("missing link locations: %+v", l)
	}
	if s.hitCount("/missing") != 2 { // 2 回の巡回でそれぞれ 1 回
		t.Fatalf("/missing fetched %d times across 2 crawls", s.hitCount("/missing"))
	}
	if l := linkOf(res, "/pic.png"); l == nil || !l.Subresource || l.Chain.Final.Status != 200 {
		t.Fatalf("subresource: %+v", l)
	}
	if res.NotFetched.OverLimits != 1 || strings.Join(res.LimitsHit, ",") != "max-depth" {
		t.Fatalf("not fetched %+v, limits %v", res.NotFetched, res.LimitsHit)
	}
	// 上限で辿らなかったページ候補も、リンクとしての生存は確かめる（HEAD）
	if l := linkOf(res, "/a/deepest"); l == nil || l.Chain.Final.Status != 200 {
		t.Fatalf("beyond max-depth, the link itself is still checked: %+v", l)
	}
	// ページでない a[href] は GET、サブリソースは HEAD
	if res.Robots != RobotsMissing {
		t.Fatalf("robots %s", res.Robots)
	}
}

// 上限ちょうどで止まる（上限 - 1、上限、上限 + 1）。超えた分は数え、limits_hit に出る。
func TestMaxPagesBoundary(t *testing.T) {
	s := newSite(t, map[string]string{
		"/":   `<a href="/p1">1</a><a href="/p2">2</a><a href="/p3">3</a>`,
		"/p1": `x`, "/p2": `x`, "/p3": `x`,
	})
	f := newClient(t)
	for maxPages, want := range map[int]struct {
		pages, over int
		hit         string
	}{3: {3, 1, "max-pages"}, 4: {4, 0, ""}, 5: {4, 0, ""}} {
		o := opt()
		o.MaxPages = maxPages
		res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
		if len(res.Pages) != want.pages || res.NotFetched.OverLimits != want.over || strings.Join(res.LimitsHit, ",") != want.hit {
			t.Errorf("max-pages %d: %d pages, over %d, hit %v", maxPages, len(res.Pages), res.NotFetched.OverLimits, res.LimitsHit)
		}
	}
}

// 深さ 0 は開始ページだけ。深さ 1 は開始ページのリンク先まで。
func TestMaxDepthBoundary(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<a href="/a">a</a>`, "/a": `<a href="/b">b</a>`, "/b": `<a href="/c">c</a>`, "/c": `x`})
	f := newClient(t)
	for depth, want := range map[int]string{0: "0:/", 1: "0:/ 1:/a", 2: "0:/ 1:/a 2:/b"} {
		o := opt()
		o.MaxDepth = depth
		if got := pageURLs(Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)); got != want {
			t.Errorf("max-depth %d: %s, want %s", depth, got, want)
		}
	}
}

// 外部（別のオリジン）は生存だけを確かめ、その先を巡回しない。--no-external なら数えるだけ。
func TestExternalLinksAreCheckedNotCrawled(t *testing.T) {
	ext := newSite(t, map[string]string{"/": `<a href="/inside">i</a>`, "/inside": `x`})
	s := newSite(t, map[string]string{"/": fmt.Sprintf(`<a href="%s">ext</a><a href="%s">gone</a>`, ext.url("/"), ext.url("/gone"))})
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if len(res.Pages) != 1 || ext.hitCount("/inside") != 0 {
		t.Fatalf("external site must not be crawled: pages %d, /inside hits %d", len(res.Pages), ext.hitCount("/inside"))
	}
	if l := linkOf(res, "/gone"); l == nil || !l.External || l.Chain.Final.Status != 404 {
		t.Fatalf("external 404: %+v", l)
	}
	if ext.hitCount("/robots.txt") != 0 {
		t.Fatal("external liveness checks do not read the other side's robots.txt (TEST-PERSPECTIVES §3.1)")
	}
	o := opt()
	o.NoExternal = true
	res = Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if len(res.Links) != 0 || res.NotFetched.External != 2 {
		t.Fatalf("--no-external: links %d, external %d", len(res.Links), res.NotFetched.External)
	}
	// HEAD を断る外部の HTML ページ（GET でやり直して HTML が返る）も巡回しない
	picky := newSite(t, map[string]string{"/": `<a href="/inside">i</a>`, "/inside": `x`})
	picky.handler["/"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<a href="/inside">i</a>`))
	}
	s2 := newSite(t, map[string]string{"/": fmt.Sprintf(`<a href="%s">p</a>`, picky.url("/"))})
	res = Crawl(context.Background(), f, startPage(t, f, s2.url("/")), opt())
	if len(res.Pages) != 1 || picky.hitCount("/inside") != 0 {
		t.Fatalf("an external HTML page fetched by GET must not be crawled: pages %d, /inside hits %d", len(res.Pages), picky.hitCount("/inside"))
	}
}

// robots.txt: 禁止された URL は巡回しない（数える）。5xx / 届かない / 429 なら開始ページより先へ行かない。4xx は制限なし。
func TestRobotsGovernsTheCrawl(t *testing.T) {
	pages := map[string]string{"/": `<a href="/ok">ok</a><a href="/private/x">p</a>`, "/ok": `x`, "/private/x": `x`}
	f := newClient(t)
	s := newSite(t, pages)
	s.robots = "User-agent: *\nDisallow: /private/\nCrawl-delay: 2\n"
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if res.Robots != RobotsOK || s.hitCount("/private/x") != 0 || res.NotFetched.RobotsDisallowed != 1 || len(res.Pages) != 2 {
		t.Fatalf("disallow: robots %s, private hits %d, not fetched %+v, pages %d", res.Robots, s.hitCount("/private/x"), res.NotFetched, len(res.Pages))
	}
	for name, h := range map[string]http.HandlerFunc{
		"5xx": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) },
		"429": func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "0"); w.WriteHeader(429) },
	} {
		s := newSite(t, pages)
		s.handler["/robots.txt"] = h
		res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
		if len(res.Pages) != 1 || res.NotFetched.RobotsUnavailable != 2 || s.hitCount("/ok") != 0 {
			t.Errorf("robots %s: pages %d, not fetched %+v, /ok hits %d (crawl must stop at the start page)", name, len(res.Pages), res.NotFetched, s.hitCount("/ok"))
		}
		if (name == "5xx" && res.Robots != RobotsBroken) || (name == "429" && res.Robots != RobotsRateLimited) {
			t.Errorf("robots %s: state %s", name, res.Robots)
		}
	}
	s = newSite(t, pages)
	s.handler["/robots.txt"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }
	if res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt()); res.Robots != RobotsMissing || len(res.Pages) != 3 {
		t.Fatalf("403 robots: %s, %d pages (unavailable = no restrictions)", res.Robots, len(res.Pages))
	}
}

// --exclude に一致する URL は取りに行かず、数える。
func TestExcludeIsNotFetchedAndCounted(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<a href="/keep">k</a><a href="/skip-me">s</a><img src="/skip-img.png">`, "/keep": `x`})
	f := newClient(t)
	o := opt()
	o.Exclude = []*regexp.Regexp{regexp.MustCompile(`skip`)}
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if res.NotFetched.Excluded != 2 || s.hitCount("/skip-me") != 0 || s.hitCount("/skip-img.png") != 0 || len(res.Links) != 1 {
		t.Fatalf("%+v links %d", res.NotFetched, len(res.Links))
	}
}

// 本文が --max-body を超えたページからはリンクを拾わず、数える。HTML でない応答からも拾わない。
func TestTruncatedAndNonHTMLPagesYieldNoLinks(t *testing.T) {
	big := strings.Repeat(`<a href="/x">x</a>`, 1000)
	s := newSite(t, map[string]string{"/": `<a href="/big">b</a><a href="/doc.pdf">p</a>`, "/big": big})
	s.handler["/doc.pdf"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte(`<a href="/from-pdf">no</a>`))
	}
	c := fetch.New(fetch.Options{Timeout: 3 * time.Second, MaxBody: 2048, Now: testsupport.Clock(testsupport.Now)})
	t.Cleanup(c.Close)
	res := Crawl(context.Background(), c, startPage(t, c, s.url("/")), opt())
	if res.NotFetched.OverMaxBody != 1 || s.hitCount("/x") != 0 || s.hitCount("/from-pdf") != 0 {
		t.Fatalf("not fetched %+v, /x hits %d, /from-pdf hits %d", res.NotFetched, s.hitCount("/x"), s.hitCount("/from-pdf"))
	}
	if got := pageURLs(res); got != "0:/ 1:/big" {
		t.Fatalf("pages %s (the pdf is not a page)", got)
	}
}

// HEAD を受け付けないサーバー: 405 なら GET でやり直す。
func TestHeadFallsBackToGetOn405(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<img src="/nohead.png">`})
	methods := []string{}
	var mu sync.Mutex
	s.handler["/nohead.png"] = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		if r.Method == "HEAD" {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(200)
	}
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if l := linkOf(res, "/nohead.png"); l == nil || l.Chain.Final.Status != 200 || strings.Join(methods, ",") != "HEAD,GET" {
		t.Fatalf("link %+v methods %v", l, methods)
	}
}

// 同時接続数の上限を超えない。ホストごとの間隔を守る。
func TestConcurrencyAndDelay(t *testing.T) {
	var inflight, peak int32
	s := newSite(t, nil)
	body := ""
	for i := 0; i < 20; i++ {
		body += fmt.Sprintf(`<a href="/p%d">p</a>`, i)
	}
	s.pages = map[string]string{"/": body}
	for i := 0; i < 20; i++ {
		s.handler[fmt.Sprintf("/p%d", i)] = func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&inflight, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&inflight, -1)
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("x"))
		}
	}
	f := newClient(t)
	o := opt()
	o.Concurrency = 2
	Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if peak > 2 {
		t.Fatalf("peak in-flight %d, want at most 2", peak)
	}
	// 間隔: 偽の時計と sleep で、同じホストへの要求が delay ずつ空くこと
	var slept []time.Duration
	var mu sync.Mutex
	now := testsupport.Now
	o = opt()
	o.Concurrency = 1
	o.Delay = time.Second
	o.Now = func() time.Time { return now }
	o.Sleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); now = now.Add(d); mu.Unlock() }
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if len(res.Links) != 20 || len(slept) < 19 {
		t.Fatalf("links %d, slept %d times (every request after the first waits)", len(res.Links), len(slept))
	}
	for _, d := range slept {
		if d != time.Second {
			t.Fatalf("slept %v, want 1s between requests to the same host", d)
		}
	}
}

// ループするリンク（A → B → A）で止まらなくならない。相対 URL、../、プロトコル相対を解決する。
func TestLoopsAndRelativeURLs(t *testing.T) {
	s := newSite(t, map[string]string{
		"/x/a": `<a href="b">b</a><a href="../top">top</a>`,
		"/x/b": `<a href="a">a</a>`,
		"/top": `<a href="/x/a">a</a>`,
	})
	s.pages["/x/a"] += fmt.Sprintf(`<a href="//%s/top">pr</a>`, strings.TrimPrefix(s.srv.URL, "http://"))
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/x/a")), opt())
	if got := pageURLs(res); got != "0:/x/a 1:/top 1:/x/b" {
		t.Fatalf("pages %s", got)
	}
	if s.hitCount("/x/a") != 1 || s.hitCount("/top") != 1 {
		t.Fatal("a looping link must not refetch")
	}
}

// リダイレクトで外のオリジンへ出たページは巡回しない。同じオリジンの別のページへ飛べば、そのページを 1 回だけ見る。
func TestRedirectedPages(t *testing.T) {
	ext := newSite(t, map[string]string{"/": `<a href="/deeper">d</a>`, "/deeper": `x`})
	s := newSite(t, map[string]string{"/": `<a href="/out">o</a><a href="/old">old</a><a href="/new">new</a>`, "/new": `<a href="/leaf">l</a>`, "/leaf": `x`})
	s.handler["/out"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, ext.url("/"), 301) }
	s.handler["/old"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/new", 301) }
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if got := pageURLs(res); got != "0:/ 1:/new 2:/leaf" {
		t.Fatalf("pages %s", got)
	}
	// /new は、リンクとしての 1 回と /old のリダイレクト先としての 1 回で 2 回まで要求されうる（先に分からない）が、ページとしては 1 つ
	if ext.hitCount("/deeper") != 0 || s.hitCount("/new") > 2 || s.hitCount("/leaf") != 1 {
		t.Fatalf("ext /deeper %d, /new %d, /leaf %d", ext.hitCount("/deeper"), s.hitCount("/new"), s.hitCount("/leaf"))
	}
	if l := linkOf(res, "/old"); l == nil || len(l.Chain.Hops) != 2 {
		t.Fatalf("redirect chain of /old: %+v", l)
	}
}

// 同じ入力なら同じ出力（並びを含む）。
func TestCrawlIsDeterministic(t *testing.T) {
	pages := map[string]string{"/": ""}
	for i := 0; i < 30; i++ {
		pages["/"] += fmt.Sprintf(`<a href="/p%02d">p</a>`, i)
		pages[fmt.Sprintf("/p%02d", i)] = fmt.Sprintf(`<a href="/q%02d">q</a>`, i)
		pages[fmt.Sprintf("/q%02d", i)] = "x"
	}
	s := newSite(t, pages)
	f := newClient(t)
	o := opt()
	o.MaxPages = 40
	a := pageURLs(Crawl(context.Background(), f, startPage(t, f, s.url("/")), o))
	b := pageURLs(Crawl(context.Background(), f, startPage(t, f, s.url("/")), o))
	if a != b {
		t.Fatalf("differs:\n%s\n%s", a, b)
	}
	if strings.Count(a, " ") != 39 {
		t.Fatalf("pages %s", a)
	}
}

// ページ数の上限は HTML のページにだけ使う: 404 や HTML でない応答、外へのリダイレクトは消費しない。
func TestPageBudgetIsSpentOnlyOnHTMLPages(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<a href="/p1">1</a><a href="/p2">2</a><a href="/p3">3</a>`, "/p2": `x`, "/p3": `x`})
	f := newClient(t)
	o := opt()
	o.MaxPages = 3
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if got := pageURLs(res); got != "0:/ 1:/p2 1:/p3" || res.NotFetched.OverLimits != 0 || len(res.LimitsHit) != 0 {
		t.Fatalf("pages %s, not fetched %+v, limits %v (the 404 must not use the budget)", got, res.NotFetched, res.LimitsHit)
	}
	if l := linkOf(res, "/p1"); l == nil || l.Chain.Final.Status != 404 {
		t.Fatalf("p1: %+v", l)
	}
}

// robots.txt の Crawl-delay が --delay より長ければそちらを使う（docs/cli-reference.md §3.2）。
func TestCrawlDelayOverridesAShorterDelay(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<a href="/a">a</a><a href="/b">b</a>`, "/a": `x`, "/b": `x`})
	s.robots = "User-agent: *\nCrawl-delay: 2\n"
	f := newClient(t)
	var slept []time.Duration
	var mu sync.Mutex
	now := testsupport.Now
	o := opt()
	o.Concurrency, o.Delay = 1, time.Second
	o.Now = func() time.Time { return now }
	o.Sleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); now = now.Add(d); mu.Unlock() }
	Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if len(slept) == 0 {
		t.Fatal("no wait between requests")
	}
	for _, d := range slept {
		if d != 2*time.Second {
			t.Fatalf("slept %v, want 2s (Crawl-delay) over --delay 1s", d)
		}
	}
}

// robots.txt: 届かない（時間切れ）、リダイレクトは 5 回まで追う、ループは届かないと同じ（docs/cli-reference.md §2.1）。
func TestRobotsUnreachableAndRedirects(t *testing.T) {
	pages := map[string]string{"/": `<a href="/ok">ok</a><a href="/private/x">p</a>`, "/ok": `x`, "/private/x": `x`}
	s := newSite(t, pages)
	s.handler["/robots.txt"] = func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}
	c := fetch.New(fetch.Options{Timeout: 200 * time.Millisecond, MaxBody: 1 << 20, Now: testsupport.Clock(testsupport.Now)})
	t.Cleanup(c.Close)
	res := Crawl(context.Background(), c, startPage(t, c, s.url("/")), opt())
	if res.Robots != RobotsUnreachable || res.RobotsErr == nil || len(res.Pages) != 1 || res.NotFetched.RobotsUnavailable != 2 {
		t.Fatalf("timeout: %s %v pages %d %+v", res.Robots, res.RobotsErr, len(res.Pages), res.NotFetched)
	}
	f := newClient(t)
	// 1 回のリダイレクトの先の robots.txt に従う
	s = newSite(t, pages)
	s.handler["/robots.txt"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/real-robots.txt", 301) }
	s.handler["/real-robots.txt"] = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("User-agent: *\nDisallow: /private/\n")) }
	res = Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if res.Robots != RobotsOK || res.NotFetched.RobotsDisallowed != 1 || !strings.HasSuffix(res.RobotsURL, "/real-robots.txt") {
		t.Fatalf("redirected robots: %s %+v %s", res.Robots, res.NotFetched, res.RobotsURL)
	}
	// 5 回を超える、ループ → 届かないと同じ（すべて禁止）
	for name, h := range map[string]http.HandlerFunc{
		"loop":    func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/robots.txt", 302) },
		"too far": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.URL.Path+"x", 302) },
	} {
		s := newSite(t, pages)
		for i := 0; i <= MaxRobotsRedirects+1; i++ { // /robots.txt, /robots.txtx, … が同じ振る舞いをする
			s.handler["/robots.txt"+strings.Repeat("x", i)] = h
		}
		res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
		if res.Robots != RobotsBadRedirect || len(res.Pages) != 1 {
			t.Errorf("%s: %s, %d pages", name, res.Robots, len(res.Pages))
		}
	}
}

// 敵対的な入力: 同じリンクを何万も並べたページでも、まったく同じ参照は 1 つ（何も失わない）。異なる参照は全部拾う。
// 1 つの URL について巡回が覚える場所は MaxLocationsPerURL（10000）まで（超えた数は持つ。出力の 100 は finding.MaxLocations）。
func TestHostilePagesAreBounded(t *testing.T) {
	body := strings.Repeat(`<a href="/x">x</a>`, 50000)
	for i := 0; i < MaxLocationsPerURL+300; i++ {
		body += fmt.Sprintf(`<a href="/x#%d">x</a>`, i) // 同じ URL、違う値 → 違う場所
	}
	for i := 0; i < 12000; i++ {
		body += fmt.Sprintf(`<img src="/d/%d">`, i) // 全部違う URL
	}
	s := newSite(t, map[string]string{"/": body})
	c := fetch.New(fetch.Options{Timeout: 3 * time.Second, MaxBody: 1 << 24, Now: testsupport.Clock(testsupport.Now)})
	t.Cleanup(c.Close)
	o := opt()
	o.Exclude = []*regexp.Regexp{regexp.MustCompile(`/d/`)} // 12000 個を取りに行かずに数える
	res := Crawl(context.Background(), c, startPage(t, c, s.url("/")), o)
	if n := len(res.Pages[0].Refs); n != 1+MaxLocationsPerURL+300+12000 {
		t.Fatalf("refs %d, want %d (identical references collapse, distinct ones are all kept)", n, 1+MaxLocationsPerURL+300+12000)
	}
	if res.NotFetched.Excluded != 12000 {
		t.Fatalf("excluded %d (every distinct reference is counted)", res.NotFetched.Excluded)
	}
	l := linkOf(res, "/x")
	if l == nil || len(l.Locations) != MaxLocationsPerURL || l.MoreLocations != 301 {
		t.Fatalf("locations %d more %d", len(l.Locations), l.MoreLocations)
	}
}

// robots.txt の禁止と全面禁止は、サブリソースや上限の外のリンクの生存確認にも効く。
func TestRobotsAlsoGovernsLivenessChecks(t *testing.T) {
	pages := map[string]string{"/": `<img src="/private/pic.png"><a href="/private/x">p</a><a href="/ok">ok</a>`, "/ok": `<a href="/private/deep">d</a>`}
	f := newClient(t)
	s := newSite(t, pages)
	s.robots = "User-agent: *\nDisallow: /private/\n"
	o := opt()
	o.MaxDepth = 1
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if s.hitCount("/private/pic.png") != 0 || s.hitCount("/private/x") != 0 || s.hitCount("/private/deep") != 0 || res.NotFetched.RobotsDisallowed != 3 {
		t.Fatalf("hits %d %d %d, not fetched %+v", s.hitCount("/private/pic.png"), s.hitCount("/private/x"), s.hitCount("/private/deep"), res.NotFetched)
	}
	s = newSite(t, pages)
	s.handler["/robots.txt"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }
	res = Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if s.hitCount("/private/pic.png") != 0 || s.hitCount("/ok") != 0 || res.NotFetched.RobotsUnavailable != 3 {
		t.Fatalf("unavailable: hits %d %d, not fetched %+v", s.hitCount("/private/pic.png"), s.hitCount("/ok"), res.NotFetched)
	}
}

// 同じオリジンのリンクが外の https へ飛べば、そこからは証明書を検証する（自己署名なら unreachable の tls）。
func TestVerificationResumesWhenAChainLeavesTheOrigin(t *testing.T) {
	self := testsupport.SelfSigned(t, testsupport.Now, "127.0.0.1")
	ext := testsupport.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), self.TLS)
	s := newSite(t, map[string]string{"/": `<a href="/out">o</a>`})
	s.handler["/out"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, ext.URL+"/", 301) }
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	l := linkOf(res, "/out")
	if l == nil || l.Err == nil {
		t.Fatalf("the external hop must be verified: %+v", l)
	}
	if u, ok := l.Err.(*fetch.UnreachableError); !ok || u.Reason != fetch.ReasonTLS || !strings.HasPrefix(u.URL, ext.URL) {
		t.Fatalf("err = %v", l.Err)
	}
}

// docs/test-perspectives.md §2.2: links を選んでいない回（FetchReferences false）はサブリソース・外部を取りに行かず数えもしないが、
// ページの候補は同じに扱う（深さの外も取ってみて、limits_hit と over_limits は links のときと同じ）。
func TestWithoutFetchReferencesOnlyPagesAreFetched(t *testing.T) {
	ext := newSite(t, map[string]string{"/": "x"})
	// private への参照と、上限を超える外部の参照も置く: links を選んでいない回では数えない（参照先の分なので）
	s := newSite(t, map[string]string{"/": fmt.Sprintf(`<a href="/a">a</a><img src="/pic.png"><a href="%s">e</a><a href="%s/2">e2</a><a href="%s/3">e3</a><a href="http://localhost:1/x">l</a><a href="/missing">m</a>`, ext.url("/"), ext.url(""), ext.url("")), "/a": `<a href="/b">b</a>`, "/b": "x"})
	f := newClient(t)
	o := opt()
	o.FetchReferences = false
	o.MaxDepth = 1
	o.MaxLinksPerPage = 2
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if s.hitCount("/pic.png") != 0 || ext.hitCount("/") != 0 || s.hitCount("/b") != 1 || s.hitCount("/missing") != 1 || s.hitCount("/a") != 1 {
		t.Fatalf("hits pic %d ext %d b %d missing %d a %d", s.hitCount("/pic.png"), ext.hitCount("/"), s.hitCount("/b"), s.hitCount("/missing"), s.hitCount("/a"))
	}
	if res.NotFetched != (NotFetched{OverLimits: 1}) || strings.Join(res.LimitsHit, ",") != "max-depth" {
		t.Fatalf("the depth limit is reported as with links; references (private, capped) are not counted: %+v %v", res.NotFetched, res.LimitsHit)
	}
	o.FetchReferences = true
	o.MaxLinksPerPage = 0
	with := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	if with.NotFetched != (NotFetched{OverLimits: 1, Private: 1}) || strings.Join(with.LimitsHit, ",") != "max-depth" || len(with.Pages) != len(res.Pages) {
		t.Fatalf("with links: %+v %v %d pages; without: %+v %v %d pages", with.NotFetched, with.LimitsHit, len(with.Pages), res.NotFetched, res.LimitsHit, len(res.Pages))
	}
	if got := pageURLs(res); got != "0:/ 1:/a" {
		t.Fatalf("pages %s", got)
	}
}

// docs/test-perspectives.md §2.2: 生存確認では本文を読まない。同じオリジンのリンクが外の巨大なファイルへリダイレクトしても、
// ステータスが届いたら切る。読んだ本文（ページ）も巡回の結果には残らない。
// ⚠ 本文を読むと、リンク先が巨大なファイルのとき 1 本ごとに --max-body まで読み、巡回の最後まで持つことになる
func TestLivenessChecksDoNotReadBodies(t *testing.T) {
	released := make(chan struct{})
	defer close(released)
	var bodyStarted int32
	ext := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		atomic.AddInt32(&bodyStarted, 1) // ⚠ ヘッダを送る前に数える: 送ったあとでは、巡回がそれより先に終わることがある
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-released // 本文は送らない: 読みに来れば巡回が止まる
	}))
	var links strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&links, `<a href="/dl/go%d.tar.gz">d</a>`, i)
	}
	// 外の HTML へのリダイレクトも同じ: 外のページは解析しないので本文は読まない
	extHTML := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		atomic.AddInt32(&bodyStarted, 1) // ⚠ ヘッダを送る前に数える: 送ったあとでは、巡回がそれより先に終わることがある
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-released
	}))
	// HEAD を受け付けない（405）相手への GET のやり直しも本文を読まない: 外部のリンクと、同じオリジンのサブリソース
	noHead := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		atomic.AddInt32(&bodyStarted, 1) // ⚠ ヘッダを送る前に数える: 送ったあとでは、巡回がそれより先に終わることがある
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-released
	}
	ext405 := testsupport.NewHTTPServer(t, http.HandlerFunc(noHead))
	s := newSite(t, map[string]string{"/": links.String() + fmt.Sprintf(`<a href="/doc">doc</a><a href="/moved">m</a><a href="%s/nohead">n</a><img src="/img405.png">`, ext405.URL), "/doc": "x"})
	s.handler["/img405.png"] = noHead
	for i := 0; i < 20; i++ {
		s.handler[fmt.Sprintf("/dl/go%d.tar.gz", i)] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, ext.URL+r.URL.Path, 302) }
	}
	s.handler["/moved"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, extHTML.URL+"/page.html", 302) }
	f := newClient(t)
	done := make(chan Result, 1)
	go func() { done <- Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt()) }()
	var res Result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the crawl waited for bodies it does not need")
	}
	if len(res.Pages) != 2 || len(res.Links) != 24 {
		t.Fatalf("pages %d links %d", len(res.Pages), len(res.Links))
	}
	for _, l := range res.Links {
		// Incomplete が立っていれば、本文を読もうとして時間切れになった（読まないのなら立たない）
		if l.Err != nil || l.Chain.Final == nil || l.Chain.Final.Status != 200 || l.Chain.Final.Body != nil || l.Chain.Final.Incomplete {
			t.Fatalf("link %s: %+v err %v", l.URL, l.Chain.Final, l.Err)
		}
	}
	for _, p := range res.Pages {
		if p.Resp.Body != nil || len(p.Refs) == 0 && p.URL != s.url("/doc") {
			t.Fatalf("page %s keeps its body (%d bytes) or lost its refs", p.URL, len(p.Resp.Body))
		}
	}
	if n := atomic.LoadInt32(&bodyStarted); n != 23 {
		t.Fatalf("servers answered %d of 23 (20 tarballs, 1 external HTML, 2 HEAD-405 fallbacks)", n)
	}
}

// docs/test-perspectives.md §3.1: HEAD にだけ 4xx を返すサーバーは GET でやり直し、GET の結果で判断する。やり直しの GET も本文は読まない。
func TestHeadOnly4xxIsRetriedWithGet(t *testing.T) {
	released := make(chan struct{})
	defer close(released)
	var methods []string
	var mu sync.Mutex
	ext := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/profile" && r.Method == "HEAD":
			w.WriteHeader(404)
		case r.URL.Path == "/profile":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-released // GET の本文は送らない: 読みに来れば止まる
		case r.URL.Path == "/gone":
			w.WriteHeader(404) // HEAD も GET も 404: 壊れている
		case r.URL.Path == "/flaky" && r.Method == "HEAD":
			w.WriteHeader(503) // 5xx も GET でやり直す
		case r.URL.Path == "/flaky":
			w.WriteHeader(200)
		}
	}))
	s := newSite(t, map[string]string{"/": fmt.Sprintf(`<a href="%s/profile">p</a><a href="%s/gone">g</a><a href="%s/flaky">f</a>`, ext.URL, ext.URL, ext.URL)})
	f := newClient(t)
	done := make(chan Result, 1)
	go func() { done <- Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt()) }()
	var res Result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the GET retry waited for a body")
	}
	status := map[string]int{}
	for _, l := range res.Links {
		if l.Err != nil || l.Chain.Final == nil || l.Chain.Final.Incomplete {
			t.Fatalf("%s: %+v err %v", l.URL, l.Chain.Final, l.Err)
		}
		status[strings.TrimPrefix(l.URL, ext.URL)] = l.Chain.Final.Status
	}
	if status["/profile"] != 200 || status["/gone"] != 404 || status["/flaky"] != 200 {
		t.Fatalf("status %v", status)
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(methods)
	if got := strings.Join(methods, ","); got != "GET /flaky,GET /gone,GET /profile,HEAD /flaky,HEAD /gone,HEAD /profile" {
		t.Fatalf("methods %s", got)
	}
}

// docs/test-perspectives.md §2.2: 1 ページから確かめる参照先は --max-links-per-page まで（異なる URL で数える）。超えた分は取りに行かず、
// ほかのページからも確かめられなかった URL を over_limits に数え、limits_hit に max-links-per-page。ちょうど上限なら当たらない。
func TestMaxLinksPerPage(t *testing.T) {
	page := `<a href="/l1">1</a><a href="/l2">2</a><a href="/l1">dup</a><a href="/l3">3</a><a href="/l4">4</a><a href="/l5">5</a>`
	s := newSite(t, map[string]string{"/": page + `<a href="/p2">p</a>`, "/p2": `<a href="/l4">4</a>`, "/l1": "x", "/l2": "x", "/l3": "x", "/l4": "x", "/l5": "x"})
	f := newClient(t)
	o := opt()
	o.MaxLinksPerPage = 3
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	// / の異なる参照先は l1 l2 l3 l4 l5 p2 の 6: 最初の 3 つ（l1 l2 l3）だけ。l4 は p2 から確かめられる。l5 と p2 は取らない
	if s.hitCount("/l1") != 1 || s.hitCount("/l2") != 1 || s.hitCount("/l3") != 1 || s.hitCount("/l5") != 0 || s.hitCount("/p2") != 0 {
		t.Fatalf("hits l1 %d l2 %d l3 %d l5 %d p2 %d", s.hitCount("/l1"), s.hitCount("/l2"), s.hitCount("/l3"), s.hitCount("/l5"), s.hitCount("/p2"))
	}
	if strings.Join(res.LimitsHit, ",") != "max-links-per-page" || res.NotFetched.OverLimits != 3 {
		t.Fatalf("limits %v over_limits %d (l4 l5 p2 were never checked)", res.LimitsHit, res.NotFetched.OverLimits)
	}
	// ちょうど上限なら当たらない
	o.MaxLinksPerPage = 6
	s2 := newSite(t, map[string]string{"/": page + `<a href="/p2">p</a>`, "/p2": `<a href="/l4">4</a>`, "/l1": "x", "/l2": "x", "/l3": "x", "/l4": "x", "/l5": "x"})
	res = Crawl(context.Background(), f, startPage(t, f, s2.url("/")), o)
	if len(res.LimitsHit) != 0 || res.NotFetched.OverLimits != 0 || s2.hitCount("/l5") != 1 {
		t.Fatalf("exactly at the cap: %v %d", res.LimitsHit, res.NotFetched.OverLimits)
	}
}

// docs/test-perspectives.md §2.2: 同じホストへの要求は 1 つずつ。--concurrency が並行させるのは別のホスト。
func TestSameHostRequestsAreSerialized(t *testing.T) {
	g := &HostGate{now: time.Now, sleep: time.Sleep}
	var inA, maxA, inAny, maxAny int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		host := "a.test"
		if i%2 == 1 {
			host = "b.test"
		}
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			turn := g.acquire(host, false)
			defer turn.release()
			n := atomic.AddInt32(&inAny, 1)
			for {
				m := atomic.LoadInt32(&maxAny)
				if n <= m || atomic.CompareAndSwapInt32(&maxAny, m, n) {
					break
				}
			}
			if host == "a.test" {
				if atomic.AddInt32(&inA, 1) > 1 {
					atomic.StoreInt32(&maxA, 2)
				}
			}
			time.Sleep(20 * time.Millisecond)
			if host == "a.test" {
				atomic.AddInt32(&inA, -1)
			}
			atomic.AddInt32(&inAny, -1)
		}(host)
	}
	wg.Wait()
	if atomic.LoadInt32(&maxA) > 1 {
		t.Fatal("two requests to the same host overlapped")
	}
	if atomic.LoadInt32(&maxAny) < 2 {
		t.Fatal("requests to different hosts never overlapped")
	}
	// delay は前の応答が返ってから数える
	var slept []time.Duration
	clock := testsupport.Now
	g = &HostGate{delay: 100 * time.Millisecond, now: func() time.Time { return clock }, sleep: func(d time.Duration) { slept = append(slept, d); clock = clock.Add(d) }}
	turn := g.acquire("a.test", false)
	clock = clock.Add(30 * time.Millisecond) // 応答に 30ms
	turn.release()
	g.acquire("a.test", false).release()
	if len(slept) != 1 || slept[0] != 100*time.Millisecond {
		t.Fatalf("slept %v (want one 100ms wait after the previous response)", slept)
	}
}

// docs/test-perspectives.md §2.2: loopback / プライベート / リンクローカルのアドレスか localhost への参照は取りに行かず private に数える。
// 開始 URL のホスト（ここでは 127.0.0.1）と同じなら取る（ポートが違っても）。
func TestPrivateAddressesAreNotFetched(t *testing.T) {
	other := newSite(t, map[string]string{"/": "x"}) // 127.0.0.1 の別のポート: 開始 URL と同じホストなので取る
	s := newSite(t, map[string]string{"/": fmt.Sprintf(`<a href="%s/">o</a><a href="http://localhost:%s/x">l</a><a href="http://10.0.0.1/">p</a><a href="http://[::1]:1/">v6</a><a href="http://169.254.1.1/">ll</a><a href="http://192.168.1.1/a">p2</a><a href="http://192.168.1.1/a">dup</a><a href="http://0.0.0.0/">u</a><a href="http://foo.localhost/">fl</a>`, other.url(""), strings.TrimPrefix(other.url(""), "http://127.0.0.1:"))})
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if other.hitCount("/") != 1 {
		t.Fatalf("same host, other port: %d hits", other.hitCount("/"))
	}
	if other.hitCount("/x") != 0 {
		t.Fatalf("localhost must not be fetched even though it resolves to the test server: %d", other.hitCount("/x"))
	}
	if res.NotFetched.Private != 7 || len(res.Links) != 1 {
		t.Fatalf("private %d links %d", res.NotFetched.Private, len(res.Links))
	}
}

// docs/test-perspectives.md §2.2: 上限を超えた参照の場所は捨てない。同じ URL をほかのページから確かめていれば（先でも後でも）found_on に出る。
func TestCappedReferencesKeepTheirPlaces(t *testing.T) {
	// 上限 2。/ の 3 本目 /later は見送り（場所は覚える）。/p2 では /later が上限の中で取られ（場所は / と /p2）、
	// /a は上限超えだが / から確かめた URL なので場所だけ足す
	s := newSite(t, map[string]string{
		"/":   `<a href="/a">a</a><a href="/p2">p</a><a href="/later">l</a>`,
		"/p2": `<a href="/later">l</a><a href="/b">b</a><a href="/a">a</a>`,
		"/a":  "x", "/b": "x", "/later": "x",
	})
	f := newClient(t)
	o := opt()
	o.MaxLinksPerPage = 2
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), o)
	places := map[string]string{}
	for _, l := range res.Links {
		var ps []string
		for _, loc := range l.Locations {
			ps = append(ps, strings.TrimPrefix(loc.Page, s.url("")))
		}
		places[strings.TrimPrefix(l.URL, s.url(""))] = strings.Join(ps, ",")
	}
	if places["/later"] != "/,/p2" || places["/a"] != "/,/p2" {
		t.Fatalf("places %v (want /later and /a found on both pages)", places)
	}
	if s.hitCount("/later") != 1 || s.hitCount("/b") != 1 || res.NotFetched.OverLimits != 0 || strings.Join(res.LimitsHit, ",") != "max-links-per-page" {
		t.Fatalf("hits later %d b %d over_limits %d limits %v", s.hitCount("/later"), s.hitCount("/b"), res.NotFetched.OverLimits, res.LimitsHit)
	}
}

// docs/test-perspectives.md §2.2: リダイレクトの先が loopback / プライベートのアドレスなら、その前で止めて private に数える。
func TestRedirectIntoPrivateAddressIsNotFollowed(t *testing.T) {
	other := newSite(t, map[string]string{"/x": "x"})
	port := strings.TrimPrefix(other.url(""), "http://127.0.0.1:")
	// /r と /r2 は同じ行き先へ飛ぶ: 取りに行かなかった URL は 1 つなので private は 1
	s := newSite(t, map[string]string{"/": `<a href="/r">r</a><a href="/r2">r2</a>`})
	s.handler["/r"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://localhost:"+port+"/x", 302) }
	s.handler["/r2"] = s.handler["/r"]
	f := newClient(t)
	res := Crawl(context.Background(), f, startPage(t, f, s.url("/")), opt())
	if other.hitCount("/x") != 0 || res.NotFetched.Private != 1 || len(res.Links) != 2 {
		t.Fatalf("hits %d private %d links %d", other.hitCount("/x"), res.NotFetched.Private, len(res.Links))
	}
	if l := res.Links[0]; l.Err != nil || l.Chain.Stop != StopPrivate || len(l.Chain.Hops) != 1 || l.Chain.Hops[0].Next != "http://localhost:"+port+"/x" {
		t.Fatalf("chain %+v err %v", l.Chain, l.Err)
	}
	// sitemap の URL でも同じ: 2 本が同じ行き先へ飛んでも private は 1（行き先の URL で数える）
	s.handler["/sitemap.xml"] = xmlHandler(urlset(s.url("/r"), s.url("/r2")))
	sres := ReadSitemaps(context.Background(), f, Origin(Normalize(s.url("/"))), sopt(nil))
	if other.hitCount("/x") != 0 || sres.Private != 1 || len(sres.Entries) != 2 || sres.Entries[0].Chain.Stop != StopPrivate || sres.Entries[1].Chain.Stop != StopPrivate {
		t.Fatalf("sitemap: hits %d private %d entries %+v", other.hitCount("/x"), sres.Private, sres.Entries)
	}
}

// docs/test-perspectives.md §2: 429 を受けたホストへの間隔の値（上限 10 秒、下限 1 秒）。
func TestBackoffValuesAreTheDocumentedValues(t *testing.T) {
	if MaxBackoffGap != 10*time.Second || DefaultRetryWait != time.Second {
		t.Fatalf("max %v, min %v; docs/test-perspectives.md §2 says 10s and 1s", MaxBackoffGap, DefaultRetryWait)
	}
}

// widenHost は host の番を持たずに間隔を広げる（値の進み方だけを見るテスト用）。
func widenHost(g *HostGate, host string, asked time.Duration) {
	s := g.slot(host)
	s.mu.Lock()
	g.widen(s, asked)
	s.mu.Unlock()
}

// fakeGate は偽の時計の HostGate と、寝た時間の記録。
func fakeGate(delay time.Duration) (*HostGate, *[]time.Duration) {
	clock := now
	var slept []time.Duration
	g := &HostGate{delay: delay, now: func() time.Time { return clock }, sleep: func(d time.Duration) { slept = append(slept, d); clock = clock.Add(d) }}
	return g, &slept
}

// docs/test-perspectives.md §2: 429 を受けたホストへの間隔は、2 倍・読めた Retry-After・1 秒のうち最も長いもの、上限 10 秒。
// ほかのホストは変わらず、--delay より短くならず、狭まらない。
func TestRateLimitedHostGetsAWiderGapForTheRestOfTheRun(t *testing.T) {
	g := &HostGate{delay: 100 * time.Millisecond}
	gap := func(h string) time.Duration { return g.slot(h).gap }
	steps := []struct {
		name  string
		asked time.Duration
		want  time.Duration
	}{
		{"first 429, no Retry-After: 1s (2 × 100ms is shorter)", 0, time.Second},
		{"second: doubled", 0, 2 * time.Second},
		{"Retry-After longer than double: Retry-After", 7 * time.Second, 7 * time.Second},
		{"Retry-After shorter than double: double", time.Second, 10 * time.Second},
		{"capped", 0, 10 * time.Second},
	}
	for _, s := range steps {
		widenHost(g, "a.test", s.asked)
		if got := gap("a.test"); got != s.want {
			t.Fatalf("%s: gap %v, want %v", s.name, got, s.want)
		}
	}
	if gap("b.test") != 0 {
		t.Fatalf("another host's gap changed: %v", gap("b.test"))
	}
	// Retry-After が待つ上限（30 秒）を超えていれば間隔には使わない: 2 倍の規則だけ
	g2 := &HostGate{}
	widenHost(g2, "c.test", time.Hour)
	if got := g2.slot("c.test").gap; got != time.Second {
		t.Fatalf("Retry-After of an hour: gap %v, want 1s", got)
	}
	// 2 倍は --delay から数える（--delay 3s なら最初の 429 で 6s）
	g3 := &HostGate{delay: 3 * time.Second}
	widenHost(g3, "d.test", 0)
	if got := g3.slot("d.test").gap; got != 6*time.Second {
		t.Fatalf("delay 3s: gap %v, want 6s", got)
	}
}

// 429 のあとの要求は --delay と広げた間隔の長い方を待つ: --delay 3s なら 6s、--delay 20s なら（間隔は上限 10s でも）20s。
func TestTheWiderGapNeverShortensTheDelay(t *testing.T) {
	for _, c := range []struct{ delay, want time.Duration }{{3 * time.Second, 6 * time.Second}, {20 * time.Second, 20 * time.Second}} {
		g, slept := fakeGate(c.delay)
		turn := g.acquire("a.test", false)
		turn.backoff(0)
		turn.release()
		g.acquire("a.test", false).release()
		if len(*slept) != 1 || (*slept)[0] != c.want {
			t.Errorf("delay %v: slept %v, want [%v]", c.delay, *slept, c.want)
		}
	}
}

// 429 を受けた番を手放す前に間隔を広げる: 同じホストの番を待っていた要求は、広げた間隔を待つ（--delay だけで出ない）。
func TestAWaitingRequestSeesTheWiderGap(t *testing.T) {
	g, slept := fakeGate(100 * time.Millisecond)
	first := g.acquire("a.test", false)
	got := make(chan struct{})
	go func() {
		g.acquire("a.test", false).release() // first が手放すまで待つ
		close(got)
	}()
	first.backoff(0) // 429 だった
	first.release()
	<-got
	if len(*slept) != 1 || (*slept)[0] != time.Second {
		t.Fatalf("the waiting request slept %v, want [1s]", *slept)
	}
}

// やり直し（1 回）は Retry-After（無ければ 1 秒）だけ待つ。広げた間隔は使わない（同じホストを待っている要求が無ければ、待った時間は Retry-After の分）。
func TestTheRetryWaitsOnlyWhatRetryAfterAsked(t *testing.T) {
	g, slept := fakeGate(100 * time.Millisecond)
	widenHost(g, "a.test", 0) // 先に 1 回 429 を受けていた: 間隔 1s
	widenHost(g, "a.test", 0) // 2 回: 2s
	g.acquire("a.test", false).release()
	*slept = nil
	calls := 0
	f := &fakeSeq{seq: []fetch.Response{{Status: 429}, {Status: 200}}, calls: &calls}
	o := opts(3)
	o.Gate, o.Now, o.Sleep = g, g.now, g.sleep
	*slept = nil
	ch, err := Follow(context.Background(), f, "https://a.test/", o)
	if err != nil || ch.Stop != StopFinal {
		t.Fatalf("stop %s err %v", ch.Stop, err)
	}
	// 最初の要求は 2s の間隔を待ち、429 のあとは DefaultRetryWait（1s）だけ待ってやり直す（4s に広がった間隔は待たない）
	if len(*slept) != 2 || (*slept)[0] != 2*time.Second || (*slept)[1] != DefaultRetryWait || ch.Waited != DefaultRetryWait {
		t.Fatalf("slept %v, waited %v; want [2s 1s] and 1s", *slept, ch.Waited)
	}
	if got := g.slot("a.test").gap; got != 4*time.Second {
		t.Fatalf("gap after the third 429: %v, want 4s", got)
	}
}

// 429 を受けると、やり直しが 200 でも、そのホストへの次の要求は広げた間隔だけ待つ。ほかのホストは --delay のまま。
func TestARetriedRateLimitStillWidensTheGap(t *testing.T) {
	clock := now
	var slept []time.Duration
	g := &HostGate{delay: 100 * time.Millisecond, now: func() time.Time { return clock }, sleep: func(d time.Duration) { slept = append(slept, d); clock = clock.Add(d) }}
	calls := 0
	f := &fakeSeq{seq: []fetch.Response{{Status: 429}, {Status: 200}}, calls: &calls}
	o := opts(3)
	o.Gate, o.Now, o.Sleep = g, g.now, g.sleep
	ch, err := Follow(context.Background(), f, "https://a.test/", o)
	if err != nil || ch.Stop != StopFinal {
		t.Fatalf("stop %s err %v", ch.Stop, err)
	}
	// 429 のあと DefaultRetryWait 待ってやり直した（それ以前の待ちは無い）
	if len(slept) != 1 || slept[0] != DefaultRetryWait {
		t.Fatalf("slept %v", slept)
	}
	slept = nil
	g.acquire("a.test", false).release()
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("next request to a.test slept %v, want [1s]", slept)
	}
	slept = nil
	g.acquire("b.test", false).release()
	g.acquire("b.test", false).release()
	if len(slept) != 1 || slept[0] != 100*time.Millisecond {
		t.Fatalf("b.test slept %v, want [100ms]", slept)
	}
}

// 文が言う「待った時間」は、429 を受けてからやり直すまでに実際に経った時間: Retry-After の分のほかに時間が経てば、それも入る
// （同じホストを待っていた要求が先に番を取り、広げた間隔を寝た場合など。ここでは待ちの間に 3s 経つ時計で作る）。
func TestWaitedIsTheTimeThatPassedBeforeTheRetry(t *testing.T) {
	clock := now
	tick := func() time.Time { return clock }
	calls := 0
	f := &fakeSeq{seq: []fetch.Response{{Status: 429}, {Status: 200}}, calls: &calls}
	o := opts(3)
	o.Now, o.Sleep = tick, func(d time.Duration) { clock = clock.Add(d + 3*time.Second) }
	ch, err := Follow(context.Background(), f, "https://a.test/", o)
	if err != nil || !ch.Retried || ch.Waited != DefaultRetryWait+3*time.Second {
		t.Fatalf("retried %v waited %v err %v; want 4s", ch.Retried, ch.Waited, err)
	}
}

// 待った時間はミリ秒に丸める（文に ns の端数を出さない）。経過にも、Retry-After の日付から求めた待ちにも端数がありうる。
func TestWaitedIsRoundedToMilliseconds(t *testing.T) {
	clock := now
	calls := 0
	f := &fakeSeq{seq: []fetch.Response{{Status: 429}, {Status: 200}}, calls: &calls}
	o := opts(3)
	o.Now, o.Sleep = func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d + 123456789) }
	ch, err := Follow(context.Background(), f, "https://a.test/", o)
	if err != nil || ch.Waited != DefaultRetryWait+123*time.Millisecond {
		t.Fatalf("waited %v err %v; want 1.123s", ch.Waited, err)
	}
}

// 丸めは床（wait を下回らない）のあとにする: Retry-After の日付から求めた wait には端数があり、時計が進まなければ床の値がそのまま出る。
func TestWaitedIsRoundedAfterTheFloor(t *testing.T) {
	clock := now.Add(123456789) // 日付は秒単位なので、wait = 5s - 0.123456789s
	calls := 0
	date := now.Add(5 * time.Second).UTC().Format(time.RFC1123)
	f := &fakeSeq{seq: []fetch.Response{{Status: 429, Header: map[string][]string{"Retry-After": {date}}}, {Status: 200}}, calls: &calls}
	o := opts(3)
	o.Now, o.Sleep = func() time.Time { return clock }, func(time.Duration) {} // 時計は進まない
	ch, err := Follow(context.Background(), f, "https://a.test/", o)
	if err != nil || ch.Waited != 4877*time.Millisecond {
		t.Fatalf("waited %v err %v; want 4.877s", ch.Waited, err)
	}
}

// docs/test-perspectives.md §2.2: robots.txt は --max-body までダウンロードし、そこで切れても読めた分のルールを使う。
// 切れたことは何も報告しない（State は ok のまま、robots-unavailable にもならない）。
func TestRobotsCutAtMaxBodyUsesWhatWasReadAndReportsNothing(t *testing.T) {
	s := newSite(t, map[string]string{"/": "x"})
	s.robots = "User-agent: *\nDisallow: /early\n" + strings.Repeat("# padding padding padding padding\n", 200) + "Disallow: /late\n"
	c := fetch.New(fetch.Options{Timeout: 3 * time.Second, MaxBody: 2048, Now: testsupport.Clock(testsupport.Now)})
	t.Cleanup(c.Close)
	if len(s.robots) <= 2048 {
		t.Fatalf("the robots.txt in this test is %d bytes, want more than --max-body (2048)", len(s.robots))
	}
	r := ReadRobots(context.Background(), c, s.srv.URL, Options{Product: "tenken", Now: testsupport.Clock(testsupport.Now), Sleep: func(time.Duration) {}})
	if r.State != RobotsOK || r.Rules == nil {
		t.Fatalf("state %q rules %v, want ok with rules (a cut robots.txt is still read)", r.State, r.Rules)
	}
	if r.Rules.Allowed("/early") || !r.Rules.Allowed("/late") {
		t.Fatalf("/early allowed %v, /late allowed %v; want the rule before the cut used and the one after it not read", r.Rules.Allowed("/early"), r.Rules.Allowed("/late"))
	}
}
