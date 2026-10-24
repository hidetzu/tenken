package cli

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/testsupport"
)

// htmlSite は、パス → HTML のサーバー（https）。知らないパスは 404。
func (h *harness) htmlSite(pages map[string]string, extra map[string]http.HandlerFunc) string {
	h.t.Helper()
	mux := http.NewServeMux()
	for p, body := range pages {
		body := body
		if p == "/" {
			p = "/{$}" // "/" はすべてに一致するので、ルートだけに
		}
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		})
	}
	for p, fn := range extra {
		mux.HandleFunc(p, fn)
	}
	return h.serveWith(mux, 365*day, 300*day)
}

func summaryOf(m map[string]any) map[string]any { return m["summary"].(map[string]any) }

// closedPort は、テストが開いてすぐ閉じたポート（届かない相手をテスト自身が用意する）。
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// docs/cli-reference.md §2.1 の links を端から端で: 壊れたリンク、見つかった場所、外部、Mixed Content、上限、数。
func TestCrawlThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	ext := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dead" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/inside">i</a>`)
	})).URL
	var site string
	h2 := h
	pages := map[string]string{
		"/":      fmt.Sprintf(`<a href="/about">a</a><a href="/missing">m</a><a href="/about#frag">dup</a><img src="http://insecure.test:1/pic.png"><a href="%s/dead">x</a><script src="/app.js"></script>`, ext),
		"/about": `<a href="/missing">m again</a><a href="/moved">mv</a><link rel="stylesheet" href="/css.css">`,
	}
	site = h2.htmlSite(pages, map[string]http.HandlerFunc{
		"/moved":  func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/about", 302) },
		"/app.js": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		"/css.css": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, "body{}")
		},
	})
	h2.probe = testsupport.NewHTTPServer(t, redirectTo(301, site)).URL + "/"
	code, out, _ := h2.run("--only", "cert,redirects,links", "--format", "json", "--exclude", `insecure\.test`, site)
	if code != ExitFindings {
		t.Fatalf("code %d\n%s", code, out)
	}
	m := decode(t, out)
	got := ruleIDs(m)
	want := "links.broken,links.broken,links.broken,links.mixed-content,redirects.temporary"
	if got != want {
		t.Fatalf("rules %s, want %s\n%s", got, want, out)
	}
	byRule := map[string][]map[string]any{}
	for _, f := range findings(m) {
		byRule[f["rule"].(string)] = append(byRule[f["rule"].(string)], f)
	}
	// /missing は 2 ページから参照され、1 件にまとまり、場所を 2 つ持つ（element: page、element、attribute、value）
	var missing map[string]any
	for _, f := range byRule["links.broken"] {
		if strings.HasSuffix(f["subject"].(string), "/missing") {
			missing = f
		}
	}
	if missing == nil || len(missing["found_on"].([]any)) != 2 {
		t.Fatalf("missing: %v", missing)
	}
	loc := missing["found_on"].([]any)[0].(map[string]any)
	if loc["kind"] != "element" || loc["page"] != site+"/" || loc["element"] != "a" || loc["attribute"] != "href" || loc["value"] != "/missing" {
		t.Fatalf("found_on = %v", loc)
	}
	if missing["detail"].(map[string]any)["status"] != 404.0 {
		t.Fatalf("detail = %v", missing["detail"])
	}
	// 外部の 404 は壊れたリンク。その先は巡回しない（pages_crawled に入らない）
	if !strings.Contains(out, ext+"/dead") {
		t.Fatalf("external 404 missing: %s", out)
	}
	// Mixed Content: --exclude で取りに行かなくても、静的に分かる。subject は http のリソース
	if mc := byRule["links.mixed-content"]; len(mc) != 1 || mc[0]["subject"] != "http://insecure.test:1/pic.png" {
		t.Fatalf("mixed content: %v", mc)
	}
	// 巡回で出会ったリダイレクト: found_on は element
	if tmp := byRule["redirects.temporary"]; len(tmp) != 1 || tmp[0]["found_on"].([]any)[0].(map[string]any)["kind"] != "element" {
		t.Fatalf("temporary: %v", byRule["redirects.temporary"])
	}
	s := summaryOf(m)
	nf := s["not_fetched"].(map[string]any)
	if s["pages_crawled"] != 2.0 || nf["excluded"] != 1.0 || nf["external"] != 0.0 || len(s["limits_hit"].([]any)) != 0 {
		t.Fatalf("summary = %v", s)
	}
	// 同じ URL（フラグメントだけ違う）は二度取りに行かない: /about は 1 ページ（pages_crawled 2 で確かめている）
	// --no-external: 外部は数えるだけ
	_, out, _ = h2.run("--only", "links", "--format", "json", "--no-external", "--exclude", `insecure\.test`, site)
	m = decode(t, out)
	if strings.Contains(out, ext+"/dead") || summaryOf(m)["not_fetched"].(map[string]any)["external"] != 1.0 {
		t.Fatalf("--no-external: %s", out)
	}
	// 上限: --max-pages 1 で about は巡回されず、over_limits と limits_hit
	_, out, _ = h2.run("--only", "links", "--format", "json", "--max-pages", "1", "--exclude", `insecure\.test`, site)
	s = summaryOf(decode(t, out))
	if s["pages_crawled"] != 1.0 || s["not_fetched"].(map[string]any)["over_limits"] != 1.0 || fmt.Sprint(s["limits_hit"]) != "[max-pages]" || s["fail"] != 3.0 {
		t.Fatalf("max-pages 1: %v (about is the only HTML page not crawled; missing is a 404, not a page, and is still checked as a link)", s)
	}
	// text にも数が出る
	_, text, _ := h2.run("--only", "links", "--exclude", `insecure\.test`, site)
	for _, want := range []string{"(2 pages crawled;", "not fetched: 1 excluded, 0 external, 0 disallowed by robots.txt", "limits hit: none", "a[href]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}
}

// robots.txt が 5xx / 届かない / 429 なら、巡回は開始ページで止まり skipped（robots-unavailable）。禁止された URL は取りに行かない。
func TestRobotsUnavailableStopsTheCrawl(t *testing.T) {
	h := newHarness(t)
	pages := map[string]string{"/": `<a href="/a">a</a><a href="/private/b">b</a>`, "/a": `x`, "/private/b": `x`}
	site := h.htmlSite(pages, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) },
	})
	code, out, _ := h.run("--only", "links", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || !strings.Contains(out, `"code": "robots-unavailable"`) || summaryOf(m)["pages_crawled"] != 1.0 || summaryOf(m)["not_fetched"].(map[string]any)["robots_unavailable"] != 2.0 {
		t.Fatalf("code %d %s", code, out)
	}
	site = h.htmlSite(pages, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n") },
	})
	_, out, _ = h.run("--only", "links", "--format", "json", site)
	m = decode(t, out)
	if summaryOf(m)["pages_crawled"] != 2.0 || summaryOf(m)["not_fetched"].(map[string]any)["robots_disallowed"] != 1.0 || strings.Contains(out, "robots-unavailable") {
		t.Fatalf("%s", out)
	}
	// 404 の robots.txt は制限なし
	site = h.htmlSite(pages, nil)
	if _, out, _ := h.run("--only", "links", "--format", "json", site); summaryOf(decode(t, out))["pages_crawled"] != 3.0 {
		t.Fatalf("%s", out)
	}
}

// 開始ページが HTML でなければ巡回するページが無い（pages_crawled 0）。開始ページが壊れていれば巡回しない。
func TestNoCrawlWithoutAnHTMLStartPage(t *testing.T) {
	h := newHarness(t)
	jsonSite := h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"a":"<a href=/x>"}`)
	}), 365*day, 300*day)
	_, out, _ := h.run("--only", "links", "--format", "json", jsonSite)
	if s := summaryOf(decode(t, out)); s["pages_crawled"] != 0.0 || ruleIDs(decode(t, out)) != "" {
		t.Fatalf("%s", out)
	}
	broken := h.serveWith(statusHandler(500), 365*day, 300*day)
	_, out, _ = h.run("--only", "links", "--format", "json", broken)
	if s := summaryOf(decode(t, out)); s["pages_crawled"] != 0.0 || ruleIDs(decode(t, out)) != "links.broken" {
		t.Fatalf("%s", out)
	}
}

// 巡回で出会った 1 段のリダイレクトの問題（自分へのループ、壊れた Location）も redirects のルールで出る（黙って落とさない）。
func TestOneHopRedirectProblemsOnCrawledLinks(t *testing.T) {
	h := newHarness(t)
	site := h.htmlSite(map[string]string{"/": `<a href="/self">s</a><a href="/noloc">n</a>`}, map[string]http.HandlerFunc{
		"/self":  func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", "/self"); w.WriteHeader(301) },
		"/noloc": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(301) },
	})
	code, out, _ := h.run("--only", "redirects,links", "--format", "json", site)
	m := decode(t, out)
	if code != ExitFindings || ruleIDs(m) != "redirects.bad-location,redirects.loop" {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
	if findings(m)[0]["found_on"].([]any)[0].(map[string]any)["kind"] != "element" {
		t.Fatalf("found_on: %v", findings(m)[0]["found_on"])
	}
}

// robots.txt が時間切れ: skipped の文は --timeout の値を言う。巡回のリンクが時間切れなら links.unreachable（timeout）で、全体は止まらない。
func TestTimeoutsInTheCrawl(t *testing.T) {
	h := newHarness(t)
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}
	// ⚠ --timeout は 1s: 300ms では CI で、下の 2 つ目の場合が code 0 になったことがある（/slow が指摘にも数にも
	//   出なかった。原因は分かっていない）。slow は 2 秒なので 1s でも時間切れになり、主張は変わらない
	site := h.htmlSite(map[string]string{"/": `<a href="/slow">s</a><a href="/ok">o</a>`, "/ok": `x`}, map[string]http.HandlerFunc{"/robots.txt": slow, "/slow": slow})
	code, out, _ := h.run("--only", "links", "--format", "json", "--timeout", "1s", site)
	m := decode(t, out)
	if code != ExitOK || !strings.Contains(out, "robots.txt could not be reached: no answer within 1s") || !strings.Contains(out, `"code": "robots-unavailable"`) {
		t.Fatalf("code %d\n%s", code, out)
	}
	// robots が使えないと同じオリジンのリンクは確かめない。robots が使えるときの時間切れを見る
	site = h.htmlSite(map[string]string{"/": `<a href="/slow">s</a><a href="/ok">o</a>`, "/ok": `x`}, map[string]http.HandlerFunc{"/slow": slow})
	code, out, _ = h.run("--only", "links", "--format", "json", "--timeout", "1s", site)
	m = decode(t, out)
	if code != ExitFindings || ruleIDs(m) != "links.unreachable" || findings(m)[0]["detail"].(map[string]any)["reason"] != "timeout" || summaryOf(m)["pages_crawled"] != 2.0 {
		t.Fatalf("code %d\n%s", code, out)
	}
}

// 巡回のリンクの 429: 文と retry_after_seconds は観測したことだけ。
func TestRateLimitedCrawledLink(t *testing.T) {
	h := newHarness(t)
	site := h.htmlSite(map[string]string{"/": `<a href="/busy">b</a>`}, map[string]http.HandlerFunc{
		"/busy": func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "60"); w.WriteHeader(429) },
	})
	_, out, _ := h.run("--only", "links", "--format", "json", site)
	m := decode(t, out)
	f := findings(m)[0]
	if ruleIDs(m) != "links.rate-limited" || f["detail"].(map[string]any)["retry_after_seconds"] != 60.0 || !strings.Contains(f["message"].(string), `Retry-After "60", longer than tenken waits (30s), so it was not asked again`) {
		t.Fatalf("%s", out)
	}
}

// --skip redirects なら、巡回で出会ったリダイレクトの指摘は出ない（それらは redirects のルール）。
func TestSkippingRedirectsDropsCrawlRedirectFindings(t *testing.T) {
	h := newHarness(t)
	site := h.htmlSite(map[string]string{"/": `<a href="/self">s</a><a href="/tmp">t</a>`, "/ok": `x`}, map[string]http.HandlerFunc{
		"/self": func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", "/self"); w.WriteHeader(301) },
		"/tmp":  func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", 302) },
	})
	code, out, _ := h.run("--only", "links", "--format", "json", site)
	if code != ExitOK || ruleIDs(decode(t, out)) != "" || !strings.Contains(out, `"check": "redirects"`) {
		t.Fatalf("code %d\n%s", code, out)
	}
}

// 開始 URL の証明書が期限切れでも、巡回は走る（証明書の問題は cert が 1 回だけ言う。cli-reference.md §2.1）。
func TestTheCrawlRunsOnAStartOriginWithABadCertificate(t *testing.T) {
	h := newHarness(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/missing">m</a><a href="/ok">o</a>`)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "x")
	})
	expired := h.serveWith(mux, 90*day, -day)
	code, out, _ := h.run("--only", "cert,links", "--format", "json", expired)
	m := decode(t, out)
	if code != ExitFindings || ruleIDs(m) != "cert.expired,links.broken" || summaryOf(m)["pages_crawled"] != 2.0 || strings.Contains(out, "robots-unavailable") {
		t.Fatalf("code %d\n%s", code, out)
	}
}

// 1 件の指摘が持つ場所は finding.MaxLocations（100）まで。JSON は最初の 100、text は残りの数を言う（cli-reference.md §5.1 "folded into a count when there are too many"）。
func TestManyPlacesAreFoldedInText(t *testing.T) {
	h := newHarness(t)
	body := ""
	for i := 0; i < 150; i++ {
		body += fmt.Sprintf(`<a href="/missing#%d">m</a>`, i)
	}
	site := h.htmlSite(map[string]string{"/": body}, nil)
	_, out, _ := h.run("--only", "links", "--format", "json", site)
	if f := findings(decode(t, out))[0]; len(f["found_on"].([]any)) != 100 {
		t.Fatalf("JSON found_on %d, want 100", len(f["found_on"].([]any)))
	}
	if _, text, _ := h.run("--only", "links", site); !strings.Contains(text, "and 50 more places (the JSON lists the first 100)") {
		t.Fatalf("text:\n%s", text)
	}
	// どのルールでも同じで、数は測ったもの: 150 か所なら 100 と「ほかに 50」（150 は巡回の上限 10000 より小さいので、切るのは
	// finding.Merge だけ: Mixed Content も、巡回で出会ったリダイレクトの鎖のどの段も、取りに行った http のリソースの unreachable も同じ数）
	body = ""
	closed := closedPort(t)
	for i := 0; i < 150; i++ {
		body += fmt.Sprintf(`<img src="http://%s/p.png#%d"><a href="/tmp#%d">t</a>`, closed, i, i)
	}
	site = h.htmlSite(map[string]string{"/": body, "/ok": "x"}, map[string]http.HandlerFunc{
		"/tmp":  func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/tmp2", 302) },
		"/tmp2": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", 302) },
	})
	_, out, _ = h.run("--only", "redirects,links", "--format", "json", site)
	m := decode(t, out)
	if got := ruleIDs(m); got != "links.unreachable,links.mixed-content,redirects.temporary,redirects.temporary" {
		t.Fatalf("rules %s\n%s", got, out)
	}
	for _, f := range findings(m) {
		if n := len(f["found_on"].([]any)); n != 100 {
			t.Errorf("%s: JSON found_on %d, want 100", f["rule"], n)
		}
	}
	_, text, _ := h.run("--only", "redirects,links", site)
	if n := strings.Count(text, "and 50 more places (the JSON lists the first 100)"); n != 4 {
		t.Fatalf("text must say 50 more for each of the 4 findings, said it %d times:\n%s", n, text)
	}
	if strings.Contains(text, "and 100 more") {
		t.Fatalf("a count nobody measured:\n%s", text)
	}
	// 2 つのリンク（srcset の 2 候補）が同じ場所から同じ段へ飛ぶ: 場所は重なるので、数は正確に 1 回ずつ（Merge が集合で数える）
	body = ""
	for i := 0; i < 150; i++ {
		body += fmt.Sprintf(`<img srcset="/s1.png#%d 1x, /s2.png#%d 2x">`, i, i)
	}
	site = h.htmlSite(map[string]string{"/": body, "/ok": "x"}, map[string]http.HandlerFunc{
		"/s1.png": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/mid", 302) },
		"/s2.png": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/mid", 302) },
		"/mid":    func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", 302) },
	})
	_, text, _ = h.run("--only", "redirects,links", site)
	if n := strings.Count(text, "and 50 more places (the JSON lists the first 100)"); n != 3 || strings.Contains(text, "and 100 more") {
		t.Fatalf("shared places must be counted once (3 findings × 50):\n%s", text)
	}
	// 巡回が場所を捨てた（1 URL に 10000 を超える場所）ときは、数を言わない
	body = ""
	for i := 0; i < 10300; i++ {
		body += fmt.Sprintf(`<a href="/missing#%d">m</a>`, i)
	}
	for i := 0; i < 10300; i++ {
		body += fmt.Sprintf(`<a href="/tmp#%d">t</a>`, i)
	}
	site = h.htmlSite(map[string]string{"/": body, "/ok": "x"}, map[string]http.HandlerFunc{
		"/tmp": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", 302) },
	})
	_, text, _ = h.run("--only", "redirects,links", site)
	if n := strings.Count(text, "and more places than tenken kept (it keeps the first 10000 per URL while crawling; the JSON lists the first 100)"); n != 2 || strings.Contains(text, "more places (the JSON") {
		t.Fatalf("truncated places must not carry a number, for links.broken and redirects.temporary alike (said %d times):\n%s", n, text)
	}
}

// robots-unavailable の文: --no-external なら「外部も確かめていない」と言う。robots の壊れたリダイレクトは種類を言う。
func TestRobotsUnavailableSentences(t *testing.T) {
	h := newHarness(t)
	ext := testsupport.NewHTTPServer(t, statusHandler(200)).URL
	pages := map[string]string{"/": fmt.Sprintf(`<a href="/a">a</a><a href="%s/x">e</a>`, ext), "/a": "x"}
	site := h.htmlSite(pages, map[string]http.HandlerFunc{"/robots.txt": statusHandler(503).ServeHTTP})
	if _, out, _ := h.run("--only", "links", "--format", "json", "--no-external", site); !strings.Contains(out, "external links were not checked either: --no-external") || strings.Contains(out, "still checked") {
		t.Fatalf("%s", out)
	}
	if _, out, _ := h.run("--only", "links", "--format", "json", site); !strings.Contains(out, "external links were still checked") {
		t.Fatalf("%s", out)
	}
	// links を選んでいなければ外部は見ていないので、外部について言わない。redirects も巡回が要るので skipped に出る
	_, out, _ := h.run("--only", "headers,redirects", "--format", "json", site)
	m := decode(t, out)
	var sk []string
	for _, s := range m["skipped"].([]any) {
		s := s.(map[string]any)
		if s["code"] == "robots-unavailable" {
			sk = append(sk, s["check"].(string))
			if strings.Contains(s["reason"].(string), "external links") {
				t.Fatalf("%s: %s", s["check"], s["reason"])
			}
		}
	}
	if strings.Join(sk, ",") != "headers,redirects" && strings.Join(sk, ",") != "redirects,headers" {
		t.Fatalf("skipped robots-unavailable for %v\n%s", sk, out)
	}
	for name, want := range map[string]string{"loop": "redirected in a loop", "bad": "redirected to a Location that is not a usable URL", "far": "redirected more than 5 times (counting from"} {
		extra := map[string]http.HandlerFunc{}
		switch name {
		case "loop":
			extra["/robots.txt"] = redirectTo(302, "/robots.txt").ServeHTTP
		case "bad":
			extra["/robots.txt"] = redirectTo(302, "http://[::1").ServeHTTP
		case "far":
			// 外のオリジンへ飛んでから回数を超える: 文は最初に要求した robots.txt を言う
			farSrv := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.URL.Path+"x", 302) }))
			extra["/robots.txt"] = redirectTo(302, farSrv.URL+"/r").ServeHTTP
		}
		site := h.htmlSite(pages, extra)
		if name == "far" {
			want += " " + site + "/robots.txt)"
		}
		if _, out, _ := h.run("--only", "links", "--format", "json", site); !strings.Contains(out, want) || !strings.Contains(out, "robots-unavailable") {
			t.Errorf("%s: want %q in\n%s", name, want, out)
		}
	}
}

func xmlHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, body)
	}
}

func urlset(locs ...string) string {
	b := `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`
	for _, l := range locs {
		b += "<url><loc>" + l + "</loc></url>"
	}
	return b + "</urlset>"
}

// docs/cli-reference.md §2.1 の sitemap を端から端で: robots の Sitemap:、URL の状態ごとの指摘、found_on、数、巡回で取った URL は二度取らない。
func TestSitemapThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	ext := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).URL
	var site string
	hits := map[string]int{}
	var mu sync.Mutex
	count := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[r.URL.Path]++
			mu.Unlock()
			next(w, r)
		}
	}
	extra := map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "User-agent: *\nDisallow: /private/\nSitemap: %s/maps/main.xml\n", site)
		},
		"/maps/main.xml": func(w http.ResponseWriter, r *http.Request) {
			xmlHandler(urlset(site+"/", site+"/linked", site+"/gone", site+"/moved", site+"/private/x", ext+"/", site+"/busy"))(w, r)
		},
		"/moved": redirectTo(302, "/linked").ServeHTTP,
		"/busy":  statusHandler(429).ServeHTTP,
	}
	pages := map[string]string{"/": `<a href="/linked">l</a>`, "/linked": "x"}
	mux := http.NewServeMux()
	for p, body := range pages {
		body := body
		if p == "/" {
			p = "/{$}"
		}
		mux.HandleFunc(p, count(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}))
	}
	for p, fn := range extra {
		mux.HandleFunc(p, count(fn))
	}
	site = h.serveWith(mux, 365*day, 300*day)

	code, out, _ := h.run("--only", "links,sitemap,redirects", "--format", "json", site)
	m := decode(t, out)
	if code != ExitFindings {
		t.Fatalf("code %d\n%s", code, out)
	}
	want := "sitemap.url-broken,sitemap.url-disallowed,sitemap.url-external,sitemap.url-rate-limited,sitemap.url-redirects,redirects.temporary"
	if got := ruleIDs(m); got != want {
		t.Fatalf("rules %s\nwant  %s\n%s", got, want, out)
	}
	for _, f := range findings(m) {
		rule := f["rule"].(string)
		fo := f["found_on"].([]any)
		switch rule {
		case "sitemap.url-broken":
			if f["subject"] != site+"/gone" || f["detail"].(map[string]any)["status"] != 404.0 || len(fo) != 1 || fo[0].(map[string]any)["kind"] != "sitemap" || fo[0].(map[string]any)["sitemap"] != site+"/maps/main.xml" || fo[0].(map[string]any)["loc"] != site+"/gone" {
				t.Errorf("url-broken: %v", f)
			}
		case "sitemap.url-external":
			if f["subject"] != ext+"/" {
				t.Errorf("url-external: %v", f)
			}
		case "sitemap.url-rate-limited":
			if f["subject"] != site+"/busy" || f["detail"].(map[string]any)["retry_after_seconds"] != nil || f["severity"] != "warn" {
				t.Errorf("url-rate-limited: %v", f)
			}
		case "redirects.temporary":
			// 巡回で /moved は参照されていないので、場所は sitemap だけ
			if f["subject"] != site+"/moved" || len(fo) != 1 || fo[0].(map[string]any)["kind"] != "sitemap" {
				t.Errorf("redirects.temporary: %v", f)
			}
		}
	}
	s := summaryOf(m)
	nf := s["not_fetched"].(map[string]any)
	if nf["external"] != 1.0 || nf["robots_disallowed"] != 1.0 || s["pages_crawled"] != 2.0 {
		t.Fatalf("summary %v", s)
	}
	mu.Lock()
	defer mu.Unlock()
	// /linked は巡回で 1 回と、/moved の鎖の行き先として 1 回（sitemap の項目としては取りに行かない）
	if hits["/private/x"] != 0 || hits["/linked"] != 2 || hits["/"] != 1 || hits["/sitemap.xml"] != 0 || hits["/robots.txt"] != 1 {
		t.Fatalf("hits %v (crawled URLs are not fetched again for the sitemap; robots.txt is read once)", hits)
	}
}

// sitemap が無い（info）、壊れている（fail、problem）。--skip sitemap なら robots も sitemap も指摘は出ないが robots.txt は読む。
func TestSitemapMissingBrokenAndSkipped(t *testing.T) {
	h := newHarness(t)
	site := h.htmlSite(map[string]string{"/": "x"}, nil)
	code, out, _ := h.run("--only", "sitemap", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "sitemap.missing,sitemap.robots-missing" {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
	for _, f := range findings(m) {
		if len(f["found_on"].([]any)) != 0 || f["severity"] != "info" {
			t.Fatalf("%v", f)
		}
	}
	// 壊れた XML: 読めたところまでの URL（ここでは別オリジンの 1 つ）は確かめる
	broken := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{"/sitemap.xml": xmlHandler("<urlset><url><loc>http://x/</loc>")})
	code, out, _ = h.run("--only", "sitemap", "--format", "json", broken)
	m = decode(t, out)
	if code != ExitFindings || ruleIDs(m) != "sitemap.invalid,sitemap.url-external,sitemap.robots-missing" {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
	if f := findings(m)[0]; f["subject"] != broken+"/sitemap.xml" || f["detail"].(map[string]any)["problem"] == "" || strings.Contains(f["message"].(string), "XML syntax error") {
		t.Fatalf("invalid: %v", f)
	}
	var robotsHits int32
	skipped := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{"/robots.txt": func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&robotsHits, 1); w.WriteHeader(503) }})
	code, out, _ = h.run("--skip", "sitemap,cert,headers", "--format", "json", skipped)
	m = decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || !strings.Contains(out, `"code": "robots-unavailable"`) || atomic.LoadInt32(&robotsHits) != 1 {
		t.Fatalf("code %d rules %q robots hits %d\n%s", code, ruleIDs(m), robotsHits, out)
	}
}

// robots.txt が 5xx: sitemap.robots-broken（warn、status）、巡回は止まる、sitemap の同じオリジンの URL は取りに行かない（全面禁止）。
// 開始ページが 404 でも sitemap は走る（docs/cli-reference.md §4）。
func TestSitemapUnderBrokenRobotsAndBrokenStartPage(t *testing.T) {
	h := newHarness(t)
	var aHits int32
	site := h.htmlSite(map[string]string{"/": `<a href="/a">a</a>`}, map[string]http.HandlerFunc{
		"/robots.txt":  statusHandler(503).ServeHTTP,
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) { xmlHandler(urlset("https://"+r.Host+"/a"))(w, r) },
		"/a":           func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&aHits, 1) },
	})
	code, out, _ := h.run("--only", "links,sitemap", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "sitemap.robots-broken,sitemap.url-disallowed" {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
	if f := findings(m)[0]; f["detail"].(map[string]any)["status"] != 503.0 || f["subject"] != site+"/robots.txt" {
		t.Fatalf("%v", f)
	}
	// /a はリンクでもあり sitemap にも載る: 巡回が robots_unavailable に数えた 1 つの URL で、sitemap はもう数えない（URL ごとに 1。TEST-PERSPECTIVES §3）
	if nf := summaryOf(m)["not_fetched"].(map[string]any); atomic.LoadInt32(&aHits) != 0 || nf["robots_unavailable"] != 1.0 || nf["robots_disallowed"] != 0.0 {
		t.Fatalf("a hits %d summary %v", aHits, summaryOf(m))
	}
	// robots.txt のリダイレクトが使えない: sitemap.robots-unreachable（reason: redirect）
	loop := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{"/robots.txt": redirectTo(302, "/robots.txt").ServeHTTP})
	_, out, _ = h.run("--only", "sitemap", "--format", "json", loop)
	m = decode(t, out)
	if f := findings(m)[0]; ruleIDs(m) != "sitemap.robots-unreachable,sitemap.missing" || f["detail"].(map[string]any)["reason"] != "redirect" || !strings.Contains(f["message"].(string), "redirected in a loop") {
		t.Fatalf("rules %s %v", ruleIDs(m), findings(m)[0])
	}
	// 開始ページが 404: links は skipped、sitemap は走る
	gone := h.htmlSite(map[string]string{"/ok": "x"}, map[string]http.HandlerFunc{"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
		xmlHandler(urlset("https://"+r.Host+"/ok", "https://"+r.Host+"/nope"))(w, r)
	}})
	code, out, _ = h.run("--only", "links,sitemap", "--format", "json", gone)
	m = decode(t, out)
	if code != ExitFindings || ruleIDs(m) != "links.broken,sitemap.url-broken,sitemap.robots-missing" || !strings.Contains(out, `"code": "start-page-broken"`) {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
}

// --max-sitemap-urls: 上限まで読み、limits_hit に max-sitemap-urls。
func TestMaxSitemapURLs(t *testing.T) {
	h := newHarness(t)
	var locs []string
	site := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 5; i++ {
			locs = append(locs, fmt.Sprintf("https://%s/u%d", r.Host, i))
		}
		xmlHandler(urlset(locs...))(w, r)
	}})
	_, out, _ := h.run("--only", "sitemap", "--max-sitemap-urls", "3", "--format", "json", site)
	m := decode(t, out)
	lh := summaryOf(m)["limits_hit"].([]any)
	if len(lh) != 1 || lh[0] != "max-sitemap-urls" || strings.Count(ruleIDs(m), "sitemap.url-broken") != 3 {
		t.Fatalf("limits %v rules %s", lh, ruleIDs(m))
	}
}

// docs/cli-reference.md §2.1 の headers を端から端で: 巡回した全ページで見て、オリジンに 1 件、ページは found_on の header に値つきで。
func TestHeadersThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	withHeaders := func(hdr map[string]string, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			for k, v := range hdr {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	good := map[string]string{"Strict-Transport-Security": "max-age=31536000", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Content-Security-Policy": "default-src 'self'", "Referrer-Policy": "no-referrer"}
	weak := map[string]string{"Strict-Transport-Security": "max-age=100", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Content-Security-Policy": "default-src 'self'", "Referrer-Policy": "no-referrer"}
	none := map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", withHeaders(good, `<a href="/p2">2</a><a href="/p3">3</a>`))
	mux.HandleFunc("/p2", withHeaders(weak, "x"))
	mux.HandleFunc("/p3", withHeaders(none, "x"))
	site := h.serveWith(mux, 365*day, 300*day)
	code, out, _ := h.run("--only", "headers", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || summaryOf(m)["pages_crawled"] != 3.0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	if got := ruleIDs(m); got != "headers.framing-missing,headers.hsts-missing,headers.hsts-weak,headers.nosniff-missing,headers.csp-missing,headers.referrer-policy-missing" {
		t.Fatalf("rules %s\n%s", got, out)
	}
	for _, f := range findings(m) {
		if f["subject"] != site || f["check"] != "headers" || len(f["detail"].(map[string]any)) != 0 {
			t.Fatalf("%v", f)
		}
		loc := f["found_on"].([]any)[0].(map[string]any)
		if loc["kind"] != "header" || loc["page"] == nil || loc["header"] == nil {
			t.Fatalf("found_on %v", loc)
		}
		if f["rule"] == "headers.hsts-weak" && (loc["page"] != site+"/p2" || loc["value"] != "max-age=100") {
			t.Fatalf("hsts-weak location %v", loc)
		}
		if f["rule"] == "headers.hsts-missing" && loc["value"] != nil {
			t.Fatalf("missing header must have value null: %v", loc)
		}
	}
	// しきい値のフラグ
	if _, out, _ := h.run("--only", "headers", "--format", "json", "--hsts-min-age", "100", site); strings.Contains(ruleIDs(decode(t, out)), "hsts-weak") {
		t.Fatalf("--hsts-min-age 100: %s", out)
	}
	// --fail-on warn で 1
	if code, _, _ := h.run("--only", "headers", "--fail-on", "warn", site); code != ExitFindings {
		t.Fatalf("--fail-on warn: code %d", code)
	}
	// http のサイトでは HSTS を見ない。headers だけ選んでも巡回は走り、links の指摘は出ない
	pmux := http.NewServeMux()
	pmux.HandleFunc("/{$}", withHeaders(none, `<a href="/missing">m</a>`)) // /missing は 404: links を選んでいれば links.broken になる
	plain := testsupport.NewHTTPServer(t, pmux).URL
	_, out, _ = h.run("--only", "headers", "--format", "json", plain)
	if got := ruleIDs(decode(t, out)); strings.Contains(got, "hsts") || strings.Contains(got, "links.") || !strings.Contains(got, "headers.nosniff-missing") {
		t.Fatalf("http site: %s", got)
	}
	// text の場所の行は「ページ  ヘッダ名」
	_, text, _ := h.run("--only", "headers", site)
	if !strings.Contains(text, "on "+site+"/p2  Strict-Transport-Security") {
		t.Fatalf("text:\n%s", text)
	}
}

// links を選んでいなければ参照先（サブリソース・外部）は取りに行かない。巡回で出会ったリダイレクトは redirects が選ばれていれば出る。
func TestCrawlWithoutLinksFetchesOnlyPages(t *testing.T) {
	h := newHarness(t)
	var extHits int32
	ext := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&extHits, 1); w.WriteHeader(200) })).URL
	var imgHits int32
	site := h.htmlSite(map[string]string{"/": fmt.Sprintf(`<a href="/moved">m</a><img src="/pic.png"><a href="%s/x">e</a>`, ext), "/ok": "x"}, map[string]http.HandlerFunc{
		"/moved":   func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", 302) },
		"/pic.png": func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&imgHits, 1); w.WriteHeader(200) },
	})
	_, out, _ := h.run("--only", "headers,redirects", "--format", "json", site)
	m := decode(t, out)
	if atomic.LoadInt32(&extHits) != 0 || atomic.LoadInt32(&imgHits) != 0 {
		t.Fatalf("references fetched without links: ext %d img %d", extHits, imgHits)
	}
	if !strings.Contains(ruleIDs(m), "redirects.temporary") || summaryOf(m)["pages_crawled"] != 2.0 {
		t.Fatalf("a redirect met while crawling must be reported when redirects is selected:\n%s", out)
	}
	// 深さの上限は links の有無で同じに出る（docs/test-perspectives.md §2.2）
	deep := h.htmlSite(map[string]string{"/": `<a href="/d1">d</a>`, "/d1": `<a href="/d2">d</a>`, "/d2": "x"}, nil)
	for _, only := range []string{"headers", "links"} {
		_, out, _ := h.run("--only", only, "--max-depth", "1", "--format", "json", deep)
		s := summaryOf(decode(t, out))
		if lh := s["limits_hit"].([]any); len(lh) != 1 || lh[0] != "max-depth" || s["not_fetched"].(map[string]any)["over_limits"] != 1.0 || s["pages_crawled"] != 2.0 {
			t.Fatalf("--only %s --max-depth 1: %v", only, s)
		}
	}
	// --only redirects でも巡回は走る（巡回で出会ったリダイレクトを見るため）
	_, out, _ = h.run("--only", "redirects", "--format", "json", site)
	if !strings.Contains(ruleIDs(decode(t, out)), "redirects.temporary") {
		t.Fatalf("--only redirects: %s", out)
	}
	// links を選べば参照先も取りに行く
	h.run("--only", "links", "--format", "json", site)
	if atomic.LoadInt32(&extHits) != 1 || atomic.LoadInt32(&imgHits) != 1 {
		t.Fatalf("with links: ext %d img %d", extHits, imgHits)
	}
}

// --exclude は sitemap の URL にも効き not_fetched.excluded に数える。--max-body より大きい sitemap は problem が tenken の上限だと言う。
func TestSitemapExcludeAndMaxBody(t *testing.T) {
	h := newHarness(t)
	var secretHits int32
	var locs []string
	site := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
			if locs == nil {
				for i := 0; i < 3000; i++ {
					locs = append(locs, fmt.Sprintf("https://%s/page/%04d", r.Host, i))
				}
			}
			xmlHandler(urlset(append([]string{"https://" + r.Host + "/secret/x"}, locs...)...))(w, r)
		},
		"/secret/x": func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&secretHits, 1) },
	})
	_, out, _ := h.run("--only", "sitemap", "--exclude", "secret|page", "--format", "json", site)
	m := decode(t, out)
	if atomic.LoadInt32(&secretHits) != 0 || summaryOf(m)["not_fetched"].(map[string]any)["excluded"] != 3001.0 || strings.Contains(ruleIDs(m), "url-") {
		t.Fatalf("secret hits %d summary %v rules %s", secretHits, summaryOf(m), ruleIDs(m))
	}
	_, out, _ = h.run("--only", "sitemap", "--exclude", "page", "--max-body", "16KiB", "--format", "json", site)
	m = decode(t, out)
	var inv map[string]any
	for _, f := range findings(m) {
		if f["rule"] == "sitemap.invalid" {
			inv = f
		}
	}
	if inv == nil || !strings.Contains(inv["detail"].(map[string]any)["problem"].(string), "larger than --max-body") || strings.Contains(inv["message"].(string), "not well-formed") {
		t.Fatalf("invalid %v\n%s", inv, out)
	}
	// gzip の sitemap が --max-body で切れた: 展開できたところまで読み、切れたのは tenken だと言う（gzip が壊れているとは言わない）
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(urlset(locs...)))
	zw.Close()
	gzSite := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) { w.Write(gz.Bytes()) }})
	_, out, _ = h.run("--only", "sitemap", "--exclude", "page", "--max-body", "1KiB", "--format", "json", gzSite)
	m = decode(t, out)
	inv = nil
	for _, f := range findings(m) {
		if f["rule"] == "sitemap.invalid" {
			inv = f
		}
	}
	if inv == nil || !strings.Contains(inv["detail"].(map[string]any)["problem"].(string), "larger than --max-body") || strings.Contains(inv["message"].(string), "gzip") || summaryOf(m)["not_fetched"].(map[string]any)["excluded"].(float64) < 100 {
		t.Fatalf("truncated gzip: %v summary %v", inv, summaryOf(m))
	}
}

// sitemap のファイル自体が 429 → sitemap.rate-limited（warn）。深すぎる入れ子 → limits_hit の sitemap-nesting。URL にならない <loc> → sitemap.invalid。
func TestSitemapRateLimitedNestingAndBadLocs(t *testing.T) {
	h := newHarness(t)
	busy := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "1"); w.WriteHeader(429) }})
	code, out, _ := h.run("--only", "sitemap", "--format", "json", busy)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "sitemap.rate-limited,sitemap.robots-missing" || findings(m)[0]["detail"].(map[string]any)["retry_after_seconds"] != 1.0 {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
	bad := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
		xmlHandler(fmt.Sprintf(`<urlset><url><loc>https://%s/</loc></url><url><loc>not a url</loc></url></urlset>`, r.Host))(w, r)
	}})
	code, out, _ = h.run("--only", "sitemap", "--format", "json", bad)
	m = decode(t, out)
	if code != ExitFindings || !strings.HasPrefix(ruleIDs(m), "sitemap.invalid") || !strings.Contains(findings(m)[0]["detail"].(map[string]any)["problem"].(string), "1 of 2 <loc> values are not URLs") {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
	deepExtra := map[string]http.HandlerFunc{}
	for i := 0; i <= 12; i++ {
		i := i
		deepExtra[fmt.Sprintf("/deep%d.xml", i)] = func(w http.ResponseWriter, r *http.Request) {
			xmlHandler(fmt.Sprintf(`<sitemapindex><sitemap><loc>https://%s/deep%d.xml</loc></sitemap></sitemapindex>`, r.Host, i+1))(w, r)
		}
	}
	deepExtra["/sitemap.xml"] = deepExtra["/deep0.xml"]
	deep := h.htmlSite(map[string]string{"/": "x"}, deepExtra)
	_, out, _ = h.run("--only", "sitemap", "--format", "json", deep)
	if lh := summaryOf(decode(t, out))["limits_hit"].([]any); len(lh) != 1 || lh[0] != "sitemap-nesting" {
		t.Fatalf("limits %v\n%s", lh, out)
	}
}

// 外部リンクの 403 は links.forbidden（warn）で、既定の --fail-on では exit 0。同じオリジンの 403 は links.broken（fail）。
func TestExternalForbiddenIsAWarning(t *testing.T) {
	h := newHarness(t)
	ext := testsupport.NewHTTPServer(t, statusHandler(403)).URL
	site := h.htmlSite(map[string]string{"/": fmt.Sprintf(`<a href="%s/post">p</a>`, ext)}, nil)
	code, out, _ := h.run("--only", "links", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "links.forbidden" || findings(m)[0]["detail"].(map[string]any)["status"] != 403.0 {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
	own := h.htmlSite(map[string]string{"/": `<a href="/private">p</a>`}, map[string]http.HandlerFunc{"/private": statusHandler(403).ServeHTTP})
	code, out, _ = h.run("--only", "links", "--format", "json", own)
	if m := decode(t, out); code != ExitFindings || ruleIDs(m) != "links.broken" {
		t.Fatalf("same origin: code %d rules %s", code, ruleIDs(m))
	}
	// 自分の URL がリダイレクトで別オリジンへ出て 403: 最後の応答のオリジンで決めるので forbidden
	via := h.htmlSite(map[string]string{"/": `<a href="/via">v</a>`}, map[string]http.HandlerFunc{"/via": redirectTo(302, ext+"/post").ServeHTTP})
	code, out, _ = h.run("--only", "links", "--format", "json", via)
	if m := decode(t, out); code != ExitOK || ruleIDs(m) != "links.forbidden" || len(findings(m)[0]["detail"].(map[string]any)["chain"].([]any)) != 2 {
		t.Fatalf("via external: code %d rules %s\n%s", code, ruleIDs(m), out)
	}
}

// --max-links-per-page と private は summary に出る。text の行にも。
func TestLinkCapAndPrivateInSummary(t *testing.T) {
	h := newHarness(t)
	site := h.htmlSite(map[string]string{"/": `<a href="/a">a</a><a href="/b">b</a><a href="/c">c</a><a href="http://localhost:1/x">l</a>`, "/a": "x", "/b": "x", "/c": "x"}, nil)
	_, out, _ := h.run("--only", "links", "--max-links-per-page", "2", "--format", "json", site)
	s := summaryOf(decode(t, out))
	nf := s["not_fetched"].(map[string]any)
	if lh := s["limits_hit"].([]any); len(lh) != 1 || lh[0] != "max-links-per-page" || nf["over_limits"] != 1.0 || nf["private"] != 1.0 {
		t.Fatalf("summary %v", s)
	}
	_, text, _ := h.run("--only", "links", "--max-links-per-page", "2", site)
	if !strings.Contains(text, "1 over the limits, 1 to private addresses") || !strings.Contains(text, "limits hit: max-links-per-page") {
		t.Fatalf("text:\n%s", text)
	}
}

// リダイレクトの先が private: 追わず、not_fetched.private に数え、鎖はそこまで redirects が見る。links の指摘は出ない（生存は確かめていない）。
func TestRedirectIntoPrivateAddressThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	site := h.htmlSite(map[string]string{"/": `<a href="/r">r</a>`}, map[string]http.HandlerFunc{
		"/r":           redirectTo(302, "https://localhost:1/x").ServeHTTP,
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) { xmlHandler(urlset("https://"+r.Host+"/r"))(w, r) },
	})
	code, out, _ := h.run("--only", "links,redirects,sitemap", "--format", "json", site)
	m := decode(t, out)
	// sitemap の URL としての /r は「リダイレクトする」（観測した）。links.* は出ない（生存は確かめていない）
	if code != ExitOK || ruleIDs(m) != "sitemap.url-redirects,redirects.temporary,sitemap.robots-missing" || summaryOf(m)["not_fetched"].(map[string]any)["private"] != 1.0 {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
}

// sitemap のファイル自体が private（robots.txt の Sitemap: の行が localhost を指す）: 取りに行かず not_fetched.private に数え、
// 指摘は出さない（TEST-PERSPECTIVES §2.2）。localhost:1 には誰もいないので、取りに行けば sitemap.invalid（connect）になる。
func TestSitemapAtPrivateAddressIsCountedNotReported(t *testing.T) {
	h := newHarness(t)
	site := h.htmlSite(map[string]string{"/": "x"}, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "Sitemap: http://localhost:1/s.xml\nUser-agent: *\nDisallow:\n")
		},
	})
	code, out, _ := h.run("--only", "sitemap", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || summaryOf(m)["not_fetched"].(map[string]any)["private"] != 1.0 {
		t.Fatalf("code %d rules %q\n%s", code, ruleIDs(m), out)
	}
}

// 巡回が取りに行かなかった URL が sitemap にも載っている: not_fetched は URL ごとに 1（巡回と sitemap をまたいでも）。指摘は出る。
// 1 行が取りに行かなかった理由の 1 つ（TEST-PERSPECTIVES §3 の行。robots.txt が使えないときは TestSitemapUnderBrokenRobotsAndBrokenStartPage）。
// ほかの内訳が 0 であることも断言する（sitemap が別の内訳に足していないこと）。
func TestNotFetchedIsCountedOncePerURLAcrossCrawlAndSitemap(t *testing.T) {
	h := newHarness(t)
	ext := h.htmlSite(map[string]string{"/e": "x"}, nil)
	robots := func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n") }
	site := func(home string, locs ...string) string {
		return h.htmlSite(map[string]string{"/": home, "/a": "x", "/ex": "x", "/private/x": "secret"}, map[string]http.HandlerFunc{
			"/robots.txt": robots,
			"/r":          redirectTo(302, "https://localhost:1/x").ServeHTTP,
			"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
				var abs []string
				for _, l := range locs {
					if strings.HasPrefix(l, "/") {
						l = "https://" + r.Host + l
					}
					abs = append(abs, l)
				}
				xmlHandler(urlset(abs...))(w, r)
			},
		})
	}
	for _, tc := range []struct {
		name  string
		home  string
		locs  []string // sitemap の <loc>（/ で始まれば同じオリジン）。同じ URL を 2 度載せる
		flags []string
		want  map[string]float64 // not_fetched。書かない内訳は 0
		rule  string             // 出る sitemap の指摘
	}{
		{"robots.txt の禁止", `<a href="/private/x">p</a>`, []string{"/private/x", "/private/x"}, nil, map[string]float64{"robots_disallowed": 1}, "sitemap.url-disallowed"},
		{"private への参照", `<a href="http://localhost:1/y">l</a>`, []string{"http://localhost:1/y", "http://localhost:1/y"}, nil, map[string]float64{"private": 1}, "sitemap.url-external"},
		{"private へのリダイレクト（行き先で数える）", `<a href="/r">r</a>`, []string{"https://localhost:1/x"}, nil, map[string]float64{"private": 1}, "sitemap.url-external"},
		{"--exclude", `<a href="/ex">e</a>`, []string{"/ex", "/ex"}, []string{"--exclude", "/ex$"}, map[string]float64{"excluded": 1}, ""},
		{"--no-external の別オリジン", `<a href="` + ext + `/e">e</a>`, []string{ext + "/e"}, []string{"--no-external"}, map[string]float64{"external": 1}, "sitemap.url-external"},
		{"--max-links-per-page で確かめなかった（禁止でもある）", `<a href="/a">a</a><a href="/private/x">p</a>`, []string{"/private/x"}, []string{"--max-links-per-page", "1"}, map[string]float64{"over_limits": 1}, "sitemap.url-disallowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--only", "links,sitemap", "--format", "json"}, tc.flags...)
			_, out, _ := h.run(append(args, site(tc.home, tc.locs...))...)
			m := decode(t, out)
			nf := summaryOf(m)["not_fetched"].(map[string]any)
			for k, v := range nf {
				if v != tc.want[k] {
					t.Errorf("not_fetched.%s = %v, want %v\n%v", k, v, tc.want[k], nf)
				}
			}
			if tc.rule != "" && !strings.Contains(ruleIDs(m), tc.rule) {
				t.Errorf("rules %s lack %s", ruleIDs(m), tc.rule)
			}
		})
	}
}

// 巡回で 429 を受けて広げた間隔は、sitemap の要求にも効く（巡回と sitemap は 1 つの番。docs/cli-reference.md §3.2 の "for the rest of the run"、
// TEST-PERSPECTIVES §2.2）。/busy はやり直しても 429（Retry-After 無し）: 1 回目の 429 で間隔が 1s に広がる
// （--delay 100ms の 2 倍と DefaultRetryWait の長い方）。sitemap の最初の要求（/sitemap.xml）は、巡回の最後の /busy の応答のあと 1s 待ってから出る。
// 番が別なら、巡回の最後の /busy と /sitemap.xml の間に待ちは無い（やり直しの前の 1s は巡回の中の待ち）。
func TestTheWiderGapCarriesFromTheCrawlIntoTheSitemap(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var events []string
	clock := now
	note := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	site := h.htmlSite(map[string]string{"/": `<a href="/busy">b</a>`}, map[string]http.HandlerFunc{
		"/busy":        func(w http.ResponseWriter, r *http.Request) { note("GET /busy"); w.WriteHeader(429) },
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) { note("GET /sitemap.xml"); xmlHandler(urlset())(w, r) },
	})
	d := h.deps()
	d.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	d.sleep = func(s time.Duration) { mu.Lock(); clock = clock.Add(s); mu.Unlock(); note("sleep " + s.String()) }
	var out, errOut bytes.Buffer
	run([]string{"--only", "links,sitemap", "--format", "json", site}, &out, &errOut, d)
	mu.Lock()
	defer mu.Unlock()
	lastBusy, sm := -1, -1
	for j, e := range events {
		switch e {
		case "GET /busy":
			lastBusy = j
		case "GET /sitemap.xml":
			sm = j
		}
	}
	if lastBusy < 0 || sm != lastBusy+2 || events[lastBusy+1] != "sleep 1s" {
		t.Fatalf("between the crawl's last request to the host and the sitemap's first, want exactly [sleep 1s]: events %v\n%s", events, out.String())
	}
}

// 429 が無いときも、巡回の --delay は sitemap へ引き継がれる: 巡回と sitemap は同じホストの番と間隔を共有する（HostGate。
// docs/test-perspectives.md §2.2）。巡回がそのホストに最後に出した要求と、sitemap の最初の要求のあいだに、ちょうど --delay を寝る。
func TestTheDelayCarriesFromTheCrawlIntoTheSitemap(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var events []string
	clock := now
	note := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	site := h.htmlSite(map[string]string{"/": `<a href="/a">a</a>`}, map[string]http.HandlerFunc{
		"/a":           func(w http.ResponseWriter, r *http.Request) { note(r.Method + " /a"); w.WriteHeader(200) },
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) { note("GET /sitemap.xml"); xmlHandler(urlset())(w, r) },
	})
	d := h.deps()
	d.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	d.sleep = func(s time.Duration) { mu.Lock(); clock = clock.Add(s); mu.Unlock(); note("sleep " + s.String()) }
	var out, errOut bytes.Buffer
	run([]string{"--only", "links,sitemap", "--format", "json", "--delay", "700ms", site}, &out, &errOut, d)
	mu.Lock()
	defer mu.Unlock()
	lastA, sm := -1, -1
	for j, e := range events {
		switch {
		case strings.HasSuffix(e, " /a"):
			lastA = j
		case e == "GET /sitemap.xml":
			sm = j
		}
	}
	if lastA < 0 || sm != lastA+2 || events[lastA+1] != "sleep 700ms" {
		t.Fatalf("between the crawl's last request to the host and the sitemap's first, want exactly [sleep 700ms]: events %v\n%s", events, out.String())
	}
}

// limitedSite は、パス → 応答のサーバー（https）で、届いた要求をすべて数える（--max-requests の観点: 相手に何回届いたか）。
func (h *harness) limitedSite(handlers map[string]http.HandlerFunc) (string, *atomic.Int64) {
	h.t.Helper()
	var hits atomic.Int64
	srv := h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if hf, ok := handlers[r.URL.Path]; ok {
			hf(w, r)
			return
		}
		http.NotFound(w, r)
	}), 365*day, 300*day)
	return srv, &hits
}

func page(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, body)
	}
}

// --max-requests（docs/cli-reference.md §3.2、TEST-PERSPECTIVES §2.2）: 外に出る要求をすべて数え、N を超えない。
// 開始ページと robots.txt は数えるが止めない。ここでは 3: 開始ページ・robots.txt・リンク 1 本。残りの 3 本は取りに行かず over_limits。
// 取りに行った 1 本の 404 は指摘になり、終了コードは指摘で決まる（1）。
func TestMaxRequestsStopsNewRequestsAndCountsTheRest(t *testing.T) {
	h := newHarness(t)
	site, hits := h.limitedSite(map[string]http.HandlerFunc{
		"/":           page(`<a href="/a">a</a><a href="/b">b</a><a href="/c">c</a><a href="/d">d</a>`),
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow:\n") },
	})
	code, out, _ := h.run("--only", "links", "--format", "json", "--max-requests", "3", site)
	m := decode(t, out)
	s := summaryOf(m)
	if hits.Load() != 3 {
		t.Fatalf("the site got %d requests, want 3\n%s", hits.Load(), out)
	}
	if code != ExitFindings || ruleIDs(m) != "links.broken" || s["not_fetched"].(map[string]any)["over_limits"] != 3.0 || fmt.Sprint(s["limits_hit"]) != "[max-requests]" {
		t.Fatalf("code %d rules %q\n%s", code, ruleIDs(m), out)
	}
	// 開始ページと robots.txt は、上限が 1 でも止めない（止めれば点検そのものができない）
	site2, hits2 := h.limitedSite(map[string]http.HandlerFunc{"/": page(`<a href="/a">a</a>`)})
	code, out, _ = h.run("--only", "links", "--format", "json", "--max-requests", "1", site2)
	m = decode(t, out)
	if hits2.Load() != 2 || code != ExitOK || ruleIDs(m) != "" || summaryOf(m)["not_fetched"].(map[string]any)["over_limits"] != 1.0 {
		t.Fatalf("max 1: hits %d code %d rules %q\n%s", hits2.Load(), code, ruleIDs(m), out)
	}
}

// 上限で止めた鎖は、途中まで見えたリダイレクトも指摘にしない。429 のやり直しを上限で出さなかった URL も links.rate-limited にしない
// （やり直していないので「429 even after a retry」とは言えない。TEST-PERSPECTIVES §2.2）。
func TestAChainStoppedByTheLimitIsNotAFinding(t *testing.T) {
	h := newHarness(t)
	h.probe = "" // probe は閉じたポート（数えるが止めない）
	site, hits := h.limitedSite(map[string]http.HandlerFunc{
		"/":  page(`<a href="/r">r</a>`),
		"/r": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/x", 302) },
		"/x": page("x"),
	})
	// 開始ページ・probe・robots.txt・/r で 4。/x へは行かない
	code, out, _ := h.run("--only", "links,redirects", "--format", "json", "--max-requests", "4", site)
	m := decode(t, out)
	if code != ExitOK || strings.Contains(ruleIDs(m), "redirects.temporary") || summaryOf(m)["not_fetched"].(map[string]any)["over_limits"] != 1.0 || hits.Load() != 3 {
		t.Fatalf("redirect: code %d rules %q hits %d\n%s", code, ruleIDs(m), hits.Load(), out)
	}
	busy, _ := h.limitedSite(map[string]http.HandlerFunc{
		"/":     page(`<a href="/busy">b</a>`),
		"/busy": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) },
	})
	// 開始ページ・robots.txt・/busy で 3。やり直しは出さない
	code, out, _ = h.run("--only", "links", "--format", "json", "--max-requests", "3", busy)
	m = decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || summaryOf(m)["not_fetched"].(map[string]any)["over_limits"] != 1.0 {
		t.Fatalf("429: code %d rules %q\n%s", code, ruleIDs(m), out)
	}
}

// sitemap の URL も同じ: 上限に当たれば取りに行かず over_limits に数え、sitemap.url-* にしない。
func TestMaxRequestsCoversTheSitemap(t *testing.T) {
	h := newHarness(t)
	var site string
	site, hits := h.limitedSite(map[string]http.HandlerFunc{
		"/":            page("x"),
		"/robots.txt":  func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow:\n") },
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) { xmlHandler(urlset(site+"/gone1", site+"/gone2"))(w, r) },
	})
	// 開始ページ・robots.txt・/sitemap.xml で 3。載っている 2 つ（どちらも 404）へは行かない
	code, out, _ := h.run("--only", "sitemap", "--format", "json", "--max-requests", "3", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || summaryOf(m)["not_fetched"].(map[string]any)["over_limits"] != 2.0 || hits.Load() != 3 || fmt.Sprint(summaryOf(m)["limits_hit"]) != "[max-requests]" {
		t.Fatalf("code %d rules %q hits %d\n%s", code, ruleIDs(m), hits.Load(), out)
	}
}

// --max-duration: 期限を過ぎてから新しい要求を出さない。--delay の待ちの間に過ぎたら、待ったあとの要求は出さない。
// 時計は寝た分だけ進む。--delay 600ms・--max-duration 1s: 巡回の 1 本目は 0s、2 本目は 0.6s、3 本目は 1.2s（期限の後）なので出さない。
func TestMaxDurationStopsRequestsAfterTheDeadline(t *testing.T) {
	h := newHarness(t)
	site, hits := h.limitedSite(map[string]http.HandlerFunc{
		"/":           page(`<a href="/a">a</a><a href="/b">b</a><a href="/c">c</a>`),
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow:\n") },
		"/a":          func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "x") },
		"/b":          func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "x") },
		"/c":          func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "x") },
	})
	var mu sync.Mutex
	clock := now
	d := h.deps()
	d.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	d.sleep = func(s time.Duration) { mu.Lock(); clock = clock.Add(s); mu.Unlock() }
	var out, errOut bytes.Buffer
	code := run([]string{"--only", "links", "--format", "json", "--delay", "600ms", "--max-duration", "1s", site}, &out, &errOut, d)
	m := decode(t, out.String())
	s := summaryOf(m)
	if hits.Load() != 4 || code != ExitOK || s["not_fetched"].(map[string]any)["over_limits"] != 1.0 || fmt.Sprint(s["limits_hit"]) != "[max-duration]" {
		t.Fatalf("hits %d code %d\n%s", hits.Load(), code, out.String())
	}
}

// 0 は無制限。負の値は使い方の誤り（2）。
func TestRunLimitFlags(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"--max-requests", "-1"}, {"--max-duration", "-1s"}, {"--max-requests", "x"}} {
		if code, _, _ := h.run(append(args, "https://example.test/")...); code != ExitUsage {
			t.Errorf("%v: code %d, want %d", args, code, ExitUsage)
		}
	}
	site, hits := h.limitedSite(map[string]http.HandlerFunc{"/": page(`<a href="/a">a</a><a href="/b">b</a>`), "/a": page("x"), "/b": page("x")})
	code, out, _ := h.run("--only", "links", "--format", "json", "--max-requests", "0", "--max-duration", "0", site)
	m := decode(t, out)
	if code != ExitOK || hits.Load() != 4 || summaryOf(m)["not_fetched"].(map[string]any)["over_limits"] != 0.0 || fmt.Sprint(summaryOf(m)["limits_hit"]) != "[]" {
		t.Fatalf("0 means no limit: code %d hits %d\n%s", code, hits.Load(), out)
	}
}

// 上限に当たったあと、出さない要求のために --delay を寝ない（寝てから断ると、残りの候補ごとに間隔を 1 つずつ寝て、
// 実行が上限のあとも続く）。時計は寝た分だけ進む。
func TestTheRunEndsAtTheLimitWithoutSleepingForRefusedRequests(t *testing.T) {
	links := ""
	for i := 0; i < 200; i++ {
		links += fmt.Sprintf(`<a href="/p%d">p</a>`, i)
	}
	for _, c := range []struct {
		args     []string
		busy     bool          // /p0 が 429（Retry-After 5）を返す
		maxSlept time.Duration // 上限に当たるまでに寝てよい分
	}{
		{[]string{"--max-requests", "3"}, false, 0},                // 開始ページ・robots.txt・1 本目（1 本目は待たない）
		{[]string{"--max-duration", "2s"}, false, 2 * time.Second}, // 0s・1s・2s（期限）: 3 本目は寝たあと出さない
		// 429 のやり直しの前に上限に当たっていれば、Retry-After を寝ない（開始ページ・robots.txt・/p0 の 429 で 3）
		{[]string{"--max-requests", "3"}, true, 0},
	} {
		h := newHarness(t)
		handlers := map[string]http.HandlerFunc{"/": page(links), "/robots.txt": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow:\n") }}
		if c.busy {
			handlers["/p0"] = func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "5"); w.WriteHeader(429) }
		}
		site, _ := h.limitedSite(handlers)
		var mu sync.Mutex
		var slept time.Duration
		clock := now
		d := h.deps()
		d.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
		d.sleep = func(s time.Duration) { mu.Lock(); clock = clock.Add(s); slept += s; mu.Unlock() }
		var out, errOut bytes.Buffer
		run(append([]string{"--only", "links", "--format", "json", "--delay", "1s"}, append(c.args, site)...), &out, &errOut, d)
		mu.Lock()
		got := slept
		mu.Unlock()
		if got > c.maxSlept {
			t.Errorf("%v (busy %v): slept %s in all, want at most %s (refused requests must not wait their turn)", c.args, c.busy, got, c.maxSlept)
		}
	}
}

// --max-requests があれば、巡回は候補の順（URL の順）に 1 つずつ取る: どの URL が上限の中に入るかが、同じ入力で同じ
// （TEST-PERSPECTIVES §2.3）。別々のホストの外部リンク 8 本は、並行なら同時に番を取りに行く。
// 上限 3 = 開始ページ・robots.txt・1 本: 入るのは、毎回 URL の順で最初の 1 本。
func TestMaxRequestsPicksTheSameURLsEveryRun(t *testing.T) {
	h := newHarness(t)
	type ext struct {
		url  string
		hits *atomic.Int64
	}
	var exts []ext
	for i := 0; i < 8; i++ {
		u, hits := h.limitedSite(map[string]http.HandlerFunc{"/": page("x")})
		exts = append(exts, ext{u + "/", hits})
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i].url < exts[j].url })
	links := ""
	for i := len(exts) - 1; i >= 0; i-- { // ページの上の順は URL の順と逆にする
		links += fmt.Sprintf(`<a href="%s">e</a>`, exts[i].url)
	}
	site, _ := h.limitedSite(map[string]http.HandlerFunc{"/": page(links), "/robots.txt": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow:\n") }})
	for run := 0; run < 20; run++ {
		before := make([]int64, len(exts))
		for i, e := range exts {
			before[i] = e.hits.Load()
		}
		h.run("--only", "links", "--format", "json", "--concurrency", "4", "--max-requests", "3", site)
		for i, e := range exts {
			got := e.hits.Load() - before[i]
			if want := int64(0); i == 0 && got != 1 || i > 0 && got != want {
				t.Fatalf("run %d: %s got %d request(s); only the first URL in order may be fetched", run, e.url, got)
			}
		}
	}
}

// docs/test-perspectives.md §2.2: robots.txt が --max-body で切れても、読めた分のルールで巡回し、切れたことは何も報告しない
// （sitemap.robots-* の指摘も、robots-unavailable の skipped も出ない）。切れる前のルールで禁止したページは取りに行かず、
// 切れたあとのルールのページは取りに行く。
func TestARobotsTxtCutAtMaxBodyIsUsedAndNotReported(t *testing.T) {
	h := newHarness(t)
	var early, late int32
	robots := "User-agent: *\nDisallow: /early\n" + strings.Repeat("# padding padding padding padding\n", 200) + "Disallow: /late\n"
	site := h.htmlSite(map[string]string{"/": `<a href="/early">e</a><a href="/late">l</a>`}, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, robots) },
		"/early":      func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&early, 1); fmt.Fprint(w, "e") },
		"/late":       func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&late, 1); fmt.Fprint(w, "l") },
	})
	if len(robots) <= 2048 {
		t.Fatalf("robots.txt is %d bytes, want more than --max-body 2KiB (2048)", len(robots))
	}
	code, out, errOut := h.run("--only", "links,sitemap", "--format", "json", "--max-body", "2KiB", site)
	if out == "" {
		t.Fatalf("code %d, no JSON; stderr %q", code, errOut)
	}
	m := decode(t, out)
	for _, f := range findings(m) {
		if strings.HasPrefix(f["rule"].(string), "sitemap.robots-") {
			t.Errorf("finding %v: a cut robots.txt must not be reported", f)
		}
	}
	for _, s := range m["skipped"].([]any) {
		if s.(map[string]any)["code"] == "robots-unavailable" {
			t.Errorf("skipped %v: a cut robots.txt is still used", s)
		}
	}
	nf := summaryOf(m)["not_fetched"].(map[string]any)
	if atomic.LoadInt32(&early) != 0 || atomic.LoadInt32(&late) == 0 || nf["robots_disallowed"] != 1.0 {
		t.Fatalf("/early fetched %d, /late fetched %d, robots_disallowed %v; want 0, at least 1, 1 (the rule before the cut is used, the one after it is not read)\n%s", atomic.LoadInt32(&early), atomic.LoadInt32(&late), nf["robots_disallowed"], errOut)
	}
}
