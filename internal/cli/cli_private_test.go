package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hidetzu/tenken/internal/testsupport"
)

// privateNet は、名前を解決するとプライベートアドレスになるホストを、外にも内にも出ずに再現する（docs/test-perspectives.md §2.2）。
// intranet.test は 10.0.0.5、staging.test は 10.0.0.6（開始 URL のホスト）、public.test は testsupport.PublicStandIn（プライベートアドレスでない）に解決する。
// 接続は 127.0.0.1 の自前のサーバーへ向け直す。⚠ 確かめる口が 10.0.0.5 へ接続しようとしたら、それ自体をテストの失敗にする。
func privateNet(t *testing.T, h *harness) {
	t.Helper()
	answers := map[string]string{"intranet.test": "10.0.0.5", "staging.test": "10.0.0.6", "public.test": testsupport.PublicStandIn}
	h.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := answers[host]; ok {
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	h.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		switch host {
		case testsupport.PublicStandIn, "10.0.0.6", "staging.test", "127.0.0.1":
			// public.test（確かめた IP）と、開始 URL のホスト（最初の接続は確かめた 10.0.0.6、ローカルと決まったあとは確かめない口が名前のまま）
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		}
		t.Errorf("connected to %s", addr)
		return nil, errors.New("test: unexpected address")
	}
}

func hitCounter(n *atomic.Int32, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
	}
}

func privateCount(m map[string]any) any {
	return summaryOf(m)["not_fetched"].(map[string]any)["private"]
}

// docs/test-perspectives.md §2.2: 名前を解決した結果がプライベートアドレスなら、取りに行かず not_fetched.private に数え、指摘は出さない。
// 点検はそのまま続く（プライベートでない名前のリンクは確かめる）。
func TestLinksResolvingToPrivateAddressesAreCountedNotFetched(t *testing.T) {
	h := newHarness(t)
	privateNet(t, h)
	var internal, public atomic.Int32
	other := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "intranet.test") {
			hitCounter(&internal, 200)(w, r)
			return
		}
		hitCounter(&public, 200)(w, r)
	}))
	u, _ := url.Parse(other.URL)
	port := u.Port()
	site := h.htmlSite(map[string]string{"/": fmt.Sprintf(`<a href="http://intranet.test:%s/x">i</a><a href="http://public.test:%s/ok">p</a>`, port, port)}, nil)
	code, out, _ := h.run("--only", "links", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || privateCount(m) != 1.0 {
		t.Fatalf("code %d rules %q private %v\n%s", code, ruleIDs(m), privateCount(m), out)
	}
	if internal.Load() != 0 || public.Load() == 0 {
		t.Errorf("hits: intranet %d (want 0), public %d (want > 0)", internal.Load(), public.Load())
	}
}

// 公開の名前から、プライベートアドレスに解決される名前へのリダイレクト: その前で止め、行き先を not_fetched.private に数える。
// sitemap のファイルがプライベートアドレスに解決される名前でも、取りに行かず数える（指摘は出さない）。
func TestRedirectsAndSitemapsIntoResolvedPrivateAddresses(t *testing.T) {
	h := newHarness(t)
	privateNet(t, h)
	other := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, p, _ := net.SplitHostPort(r.Host)
		redirectTo(302, "http://intranet.test:"+p+"/x").ServeHTTP(w, r)
	}))
	u, _ := url.Parse(other.URL)
	port := u.Port()
	site := h.htmlSite(map[string]string{"/": fmt.Sprintf(`<a href="http://public.test:%s/r">r</a>`, port)}, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Sitemap: http://intranet.test:%s/s.xml\nUser-agent: *\nDisallow:\n", port)
		},
	})
	code, out, _ := h.run("--only", "links", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || privateCount(m) != 1.0 {
		t.Fatalf("redirect: code %d rules %q private %v\n%s", code, ruleIDs(m), privateCount(m), out)
	}
	code, out, _ = h.run("--only", "sitemap", "--format", "json", site)
	m = decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || privateCount(m) != 1.0 {
		t.Fatalf("sitemap: code %d rules %q private %v\n%s", code, ruleIDs(m), privateCount(m), out)
	}
}

// 例外は今のまま（docs/cli-reference.md §5.2）: 開始 URL のホストは、名前を解決した結果がプライベートアドレスでも点検し、同じホストの参照も取りに行く。
func TestAStartHostResolvingToAPrivateAddressIsStillChecked(t *testing.T) {
	h := newHarness(t)
	privateNet(t, h)
	site := h.htmlSite(map[string]string{"/": `<a href="/a">a</a>`, "/a": "x"}, nil)
	u, _ := url.Parse(site)
	start := "https://staging.test:" + u.Port() + "/"
	code, out, errOut := h.run("--only", "links", "--format", "json", start)
	m := decode(t, out)
	if code != ExitOK || summaryOf(m)["pages_crawled"] != 2.0 || privateCount(m) != 0.0 {
		t.Fatalf("code %d pages %v private %v (stderr %q)\n%s", code, summaryOf(m)["pages_crawled"], privateCount(m), errOut, out)
	}
}

// サブリソース（img、script）、sitemap index の子、sitemap の URL のリダイレクトの先が、プライベートアドレスに解決される名前でも同じ:
// 取りに行かず not_fetched.private に URL ごとに 1、指摘は出さない（sitemap の URL がリダイレクトすることは観測したので
// sitemap.url-redirects は出る。docs/test-perspectives.md §2.2）。
func TestSubresourcesAndSitemapChildrenResolvingToPrivateAddresses(t *testing.T) {
	h := newHarness(t)
	privateNet(t, h)
	_, port, _ := net.SplitHostPort(closedPort(t)) // intranet.test のポート: 取りに行けば届かない（そもそも 10.0.0.5 へは接続しない）
	in := func(p string) string { return "https://intranet.test:" + port + p }
	site := h.htmlSite(map[string]string{"/": fmt.Sprintf(`<img src="%s"><script src="%s"></script>`, in("/i.png"), in("/a.js"))}, map[string]http.HandlerFunc{
		"/r": redirectTo(302, in("/x")).ServeHTTP,
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Sitemap: https://%s/index.xml\nUser-agent: *\nDisallow:\n", r.Host)
		},
		"/index.xml": func(w http.ResponseWriter, r *http.Request) {
			xmlHandler(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+
				`<sitemap><loc>`+in("/child.xml")+`</loc></sitemap><sitemap><loc>https://`+r.Host+`/s.xml</loc></sitemap></sitemapindex>`)(w, r)
		},
		"/s.xml": func(w http.ResponseWriter, r *http.Request) { xmlHandler(urlset("https://"+r.Host+"/r"))(w, r) },
	})
	code, out, _ := h.run("--only", "links", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || privateCount(m) != 2.0 {
		t.Fatalf("subresources: code %d rules %q private %v\n%s", code, ruleIDs(m), privateCount(m), out)
	}
	code, out, _ = h.run("--only", "sitemap", "--format", "json", site)
	m = decode(t, out)
	if code != ExitOK || ruleIDs(m) != "sitemap.url-redirects" || privateCount(m) != 2.0 {
		t.Fatalf("sitemap: code %d rules %q private %v\n%s", code, ruleIDs(m), privateCount(m), out)
	}
}

// robots.txt のリダイレクトの先がプライベートアドレス（IP の直書き、名前を解決した結果）なら、そこへ接続せず、robots.txt を
// 使えないものとして扱う: 巡回は開始ページで止まり（RFC 9309 §2.3.1.4）、行き先を not_fetched.private に 1（巡回と sitemap を
// またいでも）、sitemap を選べば sitemap.robots-unreachable（reason: redirect）。文はプライベートアドレスへのリダイレクトだと言う
// （docs/test-perspectives.md §2.2）。
func TestARobotsRedirectIntoAPrivateAddressMakesRobotsUnavailable(t *testing.T) {
	_, port, _ := net.SplitHostPort(closedPort(t))
	for _, target := range []string{"http://10.0.0.5:" + port + "/robots.txt", "http://intranet.test:" + port + "/robots.txt"} {
		h := newHarness(t)
		privateNet(t, h)
		site := h.htmlSite(map[string]string{"/": `<a href="/a">a</a>`, "/a": "x"}, map[string]http.HandlerFunc{
			"/robots.txt": redirectTo(301, target).ServeHTTP,
		})
		code, out, _ := h.run("--only", "links,sitemap", "--format", "json", site)
		m := decode(t, out)
		if code != ExitOK || summaryOf(m)["pages_crawled"] != 1.0 || privateCount(m) != 1.0 {
			t.Fatalf("%s: code %d pages %v private %v\n%s", target, code, summaryOf(m)["pages_crawled"], privateCount(m), out)
		}
		var skippedLinks, robotsFinding bool
		for _, s := range m["skipped"].([]any) {
			sk := s.(map[string]any)
			if sk["check"] == "links" && sk["code"] == "robots-unavailable" && strings.Contains(sk["reason"].(string), target+", a private address that tenken does not connect to") {
				skippedLinks = true
			}
		}
		for _, f := range findings(m) {
			if f["rule"] == "sitemap.robots-unreachable" && f["detail"].(map[string]any)["reason"] == "redirect" && strings.Contains(f["message"].(string), "a private address that tenken does not connect to") && strings.Contains(f["message"].(string), "(robots.txt is treated as unreachable, a complete disallow: RFC 9309 §2.3.1.4)") {
				robotsFinding = true
			}
		}
		if !skippedLinks || !robotsFinding {
			t.Errorf("%s: skipped links %v, sitemap.robots-unreachable %v\n%s", target, skippedLinks, robotsFinding, out)
		}
	}
}

// 上と同じ止め方を、ほかの道でも: sitemap だけを選んだ実行（巡回が無いので sitemap が数える）、HTML でない開始ページ
// （巡回はしないが、読んだ robots.txt の鎖が止まったことは数える）、プロキシを通る要求（fetch は確かめないので、IP の直書きを
// 止めるのは鎖の確かめだけ）。どれも not_fetched.private に 1 で、プライベートアドレスへは要求が出ない。
func TestARobotsRedirectIntoAPrivateAddressIsStoppedOnEveryPath(t *testing.T) {
	_, port, _ := net.SplitHostPort(closedPort(t))
	target := "http://10.0.0.5:" + port + "/robots.txt"
	robots := redirectTo(301, target).ServeHTTP
	check := func(name string, h *harness, site string, args ...string) map[string]any {
		t.Helper()
		code, out, _ := h.run(append(append([]string{"--format", "json"}, args...), site)...)
		m := decode(t, out)
		if code != ExitOK || privateCount(m) != 1.0 {
			t.Errorf("%s: code %d private %v\n%s", name, code, privateCount(m), out)
		}
		return m
	}

	h := newHarness(t)
	privateNet(t, h)
	site := h.htmlSite(map[string]string{"/": `<a href="/a">a</a>`}, map[string]http.HandlerFunc{"/robots.txt": robots})
	m := check("--only sitemap", h, site, "--only", "sitemap")
	if !strings.Contains(ruleIDs(m), "sitemap.robots-unreachable") {
		t.Errorf("--only sitemap: rules %q", ruleIDs(m))
	}

	h = newHarness(t)
	privateNet(t, h)
	json := h.htmlSite(nil, map[string]http.HandlerFunc{
		"/robots.txt": robots,
		"/{$}": func(w http.ResponseWriter, r *http.Request) { // 開始ページだけ（"/" はすべてに当たり、/sitemap.xml も JSON になる）
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"a":1}`)
		},
	})
	check("a non-HTML start page", h, json, "--only", "links")
	check("a non-HTML start page, with the sitemap", h, json, "--only", "links,sitemap") // 巡回と sitemap をまたいでも 1

	h = newHarness(t)
	var viaProxy atomic.Int32
	proxySrv := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaProxy.Add(1)
		w.WriteHeader(200)
	}))
	proxyURL, _ := url.Parse(proxySrv.URL)
	h.proxyFor = func(u *url.URL) (*url.URL, error) {
		if u.Hostname() == "10.0.0.5" {
			return proxyURL, nil // プライベートアドレスの行き先だけプロキシを通る（通れば、プロキシが受ける）
		}
		return nil, nil
	}
	site = h.htmlSite(map[string]string{"/": `<a href="/a">a</a>`}, map[string]http.HandlerFunc{"/robots.txt": robots})
	check("through a proxy", h, site, "--only", "links,sitemap")
	if viaProxy.Load() != 0 {
		t.Errorf("through a proxy: the proxy received %d requests for the private target", viaProxy.Load())
	}
}
