package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hidetzu/tenken/internal/testsupport"
)

// startHostNet は、プライベートアドレスへの接続の例外（開始 URL のホストだけ。docs/cli-reference.md §5.2 の private）を試す網。
// 名前は回ごとの答え（最後の答えを繰り返す。"," で複数）に解決し、接続はプライベートでない IP だけを 127.0.0.1 の自前のサーバーへ
// 向け直す。⚠ プライベートな IP への接続はそれ自体をテストの失敗にする（loopbackOK なら、127.0.0.1 の直書きと、確かめない口が名前のまま
// 渡すホストだけは通す: ローカルな開始 URL のホスト）。サーバーは Host で振り分け、ホストごとの要求を数える。
type startHostNet struct {
	t          *testing.T
	mu         sync.Mutex
	rounds     map[string][]string
	calls      map[string]int
	hits       map[string][]string // ホスト名 → 受けた要求のパス
	port       string
	loopbackOK bool
	// resolveNames なら、名前のまま渡された接続（確かめない口）を、OS の net.Dialer と同じく名前を解決して接続する
	// （答えの IP は上と同じに扱う: プライベートアドレスなら失敗にする）
	resolveNames bool
	dialed       []string                    // 接続しようとした IP:port（名前を解決した先も）
	handlers     map[string]http.HandlerFunc // ホスト名 → 振る舞い（無ければ 404）
}

func newStartHostNet(t *testing.T, h *harness, rounds map[string][]string) *startHostNet {
	t.Helper()
	return newStartHostNetWith(t, h, rounds, nil)
}

// newStartHostNetWith は、leaf が nil でなければ、その証明書で https のサーバーを立てる。
func newStartHostNetWith(t *testing.T, h *harness, rounds map[string][]string, leaf *tls.Certificate) *startHostNet {
	t.Helper()
	n := &startHostNet{t: t, rounds: rounds, calls: map[string]int{}, hits: map[string][]string{}, handlers: map[string]http.HandlerFunc{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(strings.Split(r.Host, ":")[0])
		n.mu.Lock()
		n.hits[host] = append(n.hits[host], r.URL.Path)
		fn := n.handlers[host]
		n.mu.Unlock()
		if fn == nil {
			w.WriteHeader(404)
			return
		}
		fn(w, r)
	})
	var srv *httptest.Server
	if leaf != nil {
		srv = testsupport.NewTLSServer(t, handler, *leaf)
	} else {
		srv = testsupport.NewHTTPServer(t, handler)
	}
	u, _ := url.Parse(srv.URL)
	n.port = u.Port()
	h.resolve = n.resolve
	h.dial = n.dial
	return n
}

func (n *startHostNet) url(host, path string) string { return "http://" + host + ":" + n.port + path }

func (n *startHostNet) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	rounds, ok := n.rounds[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	i := n.calls[host]
	n.calls[host]++
	if i >= len(rounds) {
		i = len(rounds) - 1
	}
	var out []netip.Addr
	for _, a := range strings.Split(rounds[i], ",") {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func (n *startHostNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip, perr := netip.ParseAddr(host)
	if perr != nil && n.resolveNames {
		ips, err := n.resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		ip, perr = ips[0], nil
		addr = net.JoinHostPort(ip.String(), port)
	}
	n.mu.Lock()
	n.dialed = append(n.dialed, addr)
	n.mu.Unlock()
	switch {
	case perr == nil && (ip.IsPrivate() || ip.IsLoopback()) && !(n.loopbackOK && ip.IsLoopback()):
		n.t.Errorf("connected to a private address %s", addr)
		return nil, errors.New("test: refused to dial a private address")
	case perr != nil && !n.loopbackOK:
		n.t.Errorf("dialed %s by name, without checking it", addr)
		return nil, errors.New("test: refused to dial an unchecked name")
	}
	var d net.Dialer
	return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
}

func (n *startHostNet) handle(host string, fn http.HandlerFunc) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handlers[host] = fn
}

func (n *startHostNet) dialedAddrs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.dialed...)
}

func (n *startHostNet) hitsOn(host string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.hits[host]...)
}

func htmlPage(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}
}

// skipReason は skipped のうち check の項目の code と reason（無ければ ""）。
func skipReason(m map[string]any, check string) (string, string) {
	for _, s := range m["skipped"].([]any) {
		sk := s.(map[string]any)
		if sk["check"] == check {
			return sk["code"].(string), sk["reason"].(string)
		}
	}
	return "", ""
}

// privateStopReason は、開始 URL の鎖が from から別のホストのプライベートアドレスの行き先 target へ飛んだときの could_not_check.reason
// （docs/cli-reference.md §4 の private-address）。
func privateStopReason(from, target string) string {
	return from + " redirected to " + target + ", a private address that tenken does not connect to: tenken's safety policy lets only the start URL's host be a private address, so nothing was checked; to check that site, give its URL as the start URL"
}

// expectCouldNotCheckPrivate は、開始 URL の鎖が from から別のホストのプライベートアドレスの行き先 target の前で止まり、点検できなかったこと
// （docs/cli-reference.md §4、§5.2）: 終了コード 3、could_not_check.code は private-address、reason は止めた URL と
// 安全のための決まりで取らなかったことを言う。findings と skipped は空の配列、not_fetched.private は 1。
// text でも 3 で、標準出力は空、標準エラーに同じ文（§4: 3 の text は文だけ）。
func expectCouldNotCheckPrivate(t *testing.T, h *harness, start, from, target string, args ...string) map[string]any {
	t.Helper()
	code, out, _ := h.run(append(append([]string{"--format", "json"}, args...), start)...)
	m := expectPrivateAddressJSON(t, code, out, privateStopReason(from, target))
	code, out, errOut := h.run(append(append([]string{}, args...), start)...)
	expectPrivateAddressText(t, code, out, errOut, privateStopReason(from, target))
	return m
}

// expectPrivateAddressJSON は、JSON の出力が 3、private-address、reason、空の findings と skipped、not_fetched.private 1 であること。
func expectPrivateAddressJSON(t *testing.T, code int, out, reason string) map[string]any {
	t.Helper()
	m := decode(t, out)
	cnc, _ := m["could_not_check"].(map[string]any)
	if code != ExitCouldNotCheck || cnc == nil || cnc["code"] != "private-address" || cnc["reason"] != reason {
		t.Fatalf("json: code %d could_not_check %v, want 3 and private-address with %q\n%s", code, cnc, reason, out)
	}
	if fs, sk := m["findings"].([]any), m["skipped"].([]any); fs == nil || sk == nil || len(fs) != 0 || len(sk) != 0 || privateCount(m) != 1.0 || summaryOf(m)["exit_code"] != 3.0 {
		t.Errorf("json: findings %v skipped %v private %v exit_code %v, want [] [] 1 3", m["findings"], m["skipped"], privateCount(m), summaryOf(m)["exit_code"])
	}
	return m
}

// expectPrivateAddressText は、text の出力が 3 で、標準出力が空、標準エラーに reason の 1 文（docs/cli-reference.md §4）であること。
func expectPrivateAddressText(t *testing.T, code int, out, errOut, reason string) {
	t.Helper()
	if code != ExitCouldNotCheck || out != "" || errOut != "tenken: could not check: "+reason+"\n" {
		t.Errorf("text: code %d stdout %q stderr %q, want 3, nothing, %q", code, out, errOut, reason)
	}
}

// startHostRebindReason は、開始 URL のホストが実行の途中でプライベートアドレスに解決されたときの could_not_check.reason
// （docs/cli-reference.md §4 の private-address。別のホストの文とは分ける）。
func startHostRebindReason(target string) string {
	return target + " resolved to a private address during the run; tenken's safety policy does not connect to a start URL host whose first connection found a public address, so nothing was checked; check why the host name's resolution changed"
}

// startHostProxiedReason は、開始 URL への最初の接続がプロキシを通り（ローカルかを確かめていない）、そのホストのあとの
// URL がプロキシを通らずにプライベートアドレスに解決されたときの could_not_check.reason（docs/cli-reference.md §4 の private-address）。
func startHostProxiedReason(target string) string {
	return target + " resolved to a private address, which tenken's safety policy does not connect to: the first connection to the start URL went through a proxy, so tenken did not check whether the start URL's host is local, and nothing was checked; use the proxy, or not, the same way for every URL of that host (HTTP_PROXY / HTTPS_PROXY / NO_PROXY)"
}

// docs/test-perspectives.md §2.1、§2.2: http:// だけがプロキシを通る。開始 URL（http://site.test）はプロキシが 301 で
// 同じホストの https:// へ飛ばし、そこはプロキシを通らずに 127.0.0.1 に解決される: 接続せず、点検できなかった（3、
// private-address）。文はプロキシを通ったことを言う（rebinding の文ではない）。外への接続はプロキシへの 1 本だけ。
func TestAStartHostFirstReachedThroughAProxyThatResolvesToAPrivateAddressCouldNotBeChecked(t *testing.T) {
	run := func(args ...string) (int, string, string, *startHostNet, string) {
		h := newHarness(t)
		n := newStartHostNet(t, h, map[string][]string{"site.test": {"127.0.0.1"}})
		n.loopbackOK = true // プロキシ（127.0.0.1 のテストのサーバー）へは名前を確かめずに接続する。記録は残る
		target := "https://site.test:" + n.port + "/"
		proxy := testsupport.NewHTTPServer(t, redirectTo(301, target)) // プロキシの代わり: 何を聞かれても 301
		pu, _ := url.Parse(proxy.URL)
		h.proxyFor = func(u *url.URL) (*url.URL, error) {
			if u.Scheme == "http" {
				return pu, nil
			}
			return nil, nil
		}
		code, out, errOut := h.run(append(append([]string{}, args...), n.url("site.test", "/"))...)
		return code, out, errOut, n, pu.Host
	}
	code, out, _, n, proxyHost := run("--only", "links", "--format", "json")
	expectPrivateAddressJSON(t, code, out, startHostProxiedReason("https://site.test:"+n.port+"/"))
	if got := strings.Join(n.dialedAddrs(), " "); got != proxyHost {
		t.Errorf("dialed %q, want only the proxy %s", got, proxyHost)
	}
	code, out, errOut, n, _ := run("--only", "links")
	want := "tenken: could not check: " + startHostProxiedReason("https://site.test:"+n.port+"/") + "\n"
	if code != ExitCouldNotCheck || out != "" || !strings.HasPrefix(errOut, want) {
		t.Errorf("text: code %d stdout %q stderr %q, want 3, nothing, starting with %q", code, out, errOut, want)
	}
}

// docs/test-perspectives.md §2.1、§2.2: 開始 URL の鎖の中で、開始 URL のホスト自身がプライベートアドレスに解決されるようになった
// （/ から同じホストの /next へ 302、/next のときに 127.0.0.1）: 接続せず、点検できなかった（3、private-address）。
// 文は別のホストのときの文ではなく、名前の解決が変わったことを言う。127.0.0.1 への接続はこの網で見える（失敗にする）。
func TestAStartHostRebindingWithinTheStartChainCouldNotBeChecked(t *testing.T) {
	run := func(args ...string) (int, string, string, *startHostNet) {
		h := newHarness(t)
		n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn, "127.0.0.1"}})
		n.handle("site.test", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Connection", "close") // /next の要求で解決し直させる
			if r.URL.Path == "/" {
				redirectTo(302, "/next").ServeHTTP(w, r)
				return
			}
			htmlPage("x")(w, r)
		})
		code, out, errOut := h.run(append(append([]string{}, args...), n.url("site.test", "/"))...)
		return code, out, errOut, n
	}
	code, out, _, n := run("--only", "links", "--format", "json")
	expectPrivateAddressJSON(t, code, out, startHostRebindReason(n.url("site.test", "/next")))
	if got := strings.Join(n.hitsOn("site.test"), " "); got != "/" {
		t.Errorf("site.test requests %q, want only /", got)
	}
	code, out, errOut, n := run("--only", "links")
	expectPrivateAddressText(t, code, out, errOut, startHostRebindReason(n.url("site.test", "/next")))
}

// docs/test-perspectives.md §2.1、§2.2: プライベートアドレスへの接続の例外は開始 URL のホストだけ。公開の開始 URL が別のホストの
// プライベートアドレスの直書き（127.0.0.1、10.0.0.5）へ飛ばしても、そこへは接続せず、点検できなかった（3、private-address）。
func TestAPublicStartRedirectingToAPrivateIPOfAnotherHostCouldNotBeChecked(t *testing.T) {
	for _, inside := range []string{"127.0.0.1", "10.0.0.5"} {
		h := newHarness(t)
		n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn}})
		n.handle("site.test", redirectTo(302, n.url(inside, "/admin")).ServeHTTP)
		expectCouldNotCheckPrivate(t, h, n.url("site.test", "/"), n.url("site.test", "/"), n.url(inside, "/admin"), "--only", "links")
		if got := n.hitsOn(inside); len(got) != 0 {
			t.Errorf("%s was requested: %v", inside, got)
		}
	}
}

// 同じく、別のホストの名前がプライベートアドレスだけに解決されるなら、接続しない（接続の直前に確かめる）。
func TestAPublicStartRedirectingToAHostNameResolvingToAPrivateAddressCouldNotBeChecked(t *testing.T) {
	h := newHarness(t)
	n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn}, "intranet.test": {"10.0.0.5"}})
	n.handle("site.test", redirectTo(302, n.url("intranet.test", "/")).ServeHTTP)
	expectCouldNotCheckPrivate(t, h, n.url("site.test", "/"), n.url("site.test", "/"), n.url("intranet.test", "/"), "--only", "links")
	if n.calls["intranet.test"] != 2 || len(n.hitsOn("intranet.test")) != 0 {
		t.Errorf("intranet.test resolved %d times (want 2: checked once per run), requested %v", n.calls["intranet.test"], n.hitsOn("intranet.test"))
	}
}

// 公開の開始 URL → 公開の別のホスト → プライベートアドレスのホスト: プライベートアドレスのホストの前で止まり、点検できなかった。止めた URL の手前の
// 往復（other.test）は取った。
func TestAMultiHopStartChainStoppedAtAPrivateHopCouldNotBeChecked(t *testing.T) {
	h := newHarness(t)
	n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn}, "other.test": {testsupport.OtherPublicStandIn}, "intranet.test": {"10.0.0.5"}})
	n.handle("site.test", redirectTo(302, n.url("other.test", "/")).ServeHTTP)
	n.handle("other.test", redirectTo(302, n.url("intranet.test", "/")).ServeHTTP)
	expectCouldNotCheckPrivate(t, h, n.url("site.test", "/"), n.url("other.test", "/"), n.url("intranet.test", "/"), "--only", "links,redirects")
	if len(n.hitsOn("other.test")) != 2 || len(n.hitsOn("intranet.test")) != 0 {
		t.Errorf("other.test %v (want one request per run), intranet.test %v (want none)", n.hitsOn("other.test"), n.hitsOn("intranet.test"))
	}
}

// 開始 URL のホストがローカルでも、別のホストのプライベートアドレスへのリダイレクトは同じく止まり、点検できなかった
// （localhost → 127.0.0.1、社内の名前 → 別の社内の名前。開始 URL のホストだけが信頼の境界）。
func TestALocalStartRedirectingToAnotherPrivateHostCouldNotBeChecked(t *testing.T) {
	for _, c := range []struct{ start, target string }{
		{"localhost", "127.0.0.1"},
		{"intranet.test", "sso.intranet.test"},
	} {
		h := newHarness(t)
		n := newStartHostNet(t, h, map[string][]string{"intranet.test": {"10.0.0.5"}, "sso.intranet.test": {"10.0.0.7"}})
		n.loopbackOK = true // localhost は確かめずに名前のまま接続する（最初からローカル）
		if c.start == "intranet.test" {
			// 最初の接続は確かめた 10.0.0.5 へ（プライベートアドレスだけなのでローカル）。10.0.0.7 への接続は失敗にする
			h.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(addr)
				if host != "10.0.0.5" && host != "intranet.test" {
					t.Errorf("connected to %s", addr)
					return nil, errors.New("test: unexpected address")
				}
				var d net.Dialer
				return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			}
		}
		n.handle(c.start, redirectTo(302, n.url(c.target, "/login")).ServeHTTP)
		expectCouldNotCheckPrivate(t, h, n.url(c.start, "/"), n.url(c.start, "/"), n.url(c.target, "/login"), "--only", "links")
		if got := n.hitsOn(c.target); len(got) != 0 {
			t.Errorf("%s was requested: %v", c.target, got)
		}
	}
}

// http:// の probe も同じ: 開始 URL のホスト（ここではローカルな 127.0.0.1）から別のホストのプライベートアドレスへ飛べば、
// そこへは接続しない。https へ飛ぶことは観測したので redirects.no-https は出ない。
func TestTheProbeDoesNotFollowARedirectIntoAnotherPrivateHost(t *testing.T) {
	h := newHarness(t)
	n := newStartHostNet(t, h, map[string][]string{"intranet.test": {"10.0.0.5"}})
	n.loopbackOK = true // 開始 URL のホストは 127.0.0.1（ローカル）
	site := h.htmlSite(map[string]string{"/": "x"}, nil)
	h.probe = n.url("127.0.0.1", "/")
	n.handle("127.0.0.1", redirectTo(301, "https://intranet.test:"+n.port+"/").ServeHTTP)
	code, out, _ := h.run("--only", "redirects", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "" || privateCount(m) != 1.0 {
		t.Fatalf("code %d rules %q private %v\n%s", code, ruleIDs(m), privateCount(m), out)
	}
	if c, _ := skipReason(m, "redirects"); c != "" {
		t.Errorf("skipped redirects: %q", c)
	}
	if len(n.hitsOn("127.0.0.1")) != 1 || n.calls["intranet.test"] != 1 || len(n.hitsOn("intranet.test")) != 0 {
		t.Errorf("probe hits %v, intranet.test resolved %d, requested %v", n.hitsOn("127.0.0.1"), n.calls["intranet.test"], n.hitsOn("intranet.test"))
	}
}

// DNS rebinding: 最初の接続で公開のアドレスに解決された開始 URL のホストは、ローカルではない。あとで同じ名前のまま
// プライベートアドレスだけに解決されても、そこへは接続しない（ホスト名が同じというだけでは許さない）。robots.txt なら使えない
// robots.txt として巡回は開始ページで止まり、同じホストへのリンクなら取りに行かない。どちらも not_fetched.private に 1。
func TestAStartHostThatRebindsToAPrivateAddressIsNotConnectedAgain(t *testing.T) {
	t.Run("robots.txt", func(t *testing.T) {
		h := newHarness(t)
		n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn, "127.0.0.1"}})
		n.handle("site.test", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Connection", "close") // 開始ページの接続を使い回させない: robots.txt の要求で解決し直させる
			htmlPage(`<a href="/a">a</a>`)(w, r)
		})
		code, out, _ := h.run("--only", "links", "--format", "json", n.url("site.test", "/"))
		m := decode(t, out)
		if code != ExitOK || privateCount(m) != 1.0 || summaryOf(m)["pages_crawled"] != 1.0 {
			t.Fatalf("code %d private %v pages %v\n%s", code, privateCount(m), summaryOf(m)["pages_crawled"], out)
		}
		if c, reason := skipReason(m, "links"); c != "robots-unavailable" || !strings.Contains(reason, n.url("site.test", "/robots.txt")) {
			t.Errorf("skipped links: %q %q", c, reason)
		}
		if got := strings.Join(n.hitsOn("site.test"), " "); got != "/" {
			t.Errorf("site.test requests %q, want only the start page", got)
		}
	})
	t.Run("a same-host link", func(t *testing.T) {
		h := newHarness(t)
		// 開始ページと robots.txt までは公開のアドレス、そのあとは 127.0.0.1。接続を毎回閉じて、要求ごとに解決させる
		n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn, testsupport.PublicStandIn, "127.0.0.1"}})
		n.handle("site.test", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Connection", "close")
			if r.URL.Path == "/robots.txt" {
				fmt.Fprint(w, "User-agent: *\nDisallow:\n")
				return
			}
			htmlPage(`<a href="/a">a</a>`)(w, r)
		})
		code, out, _ := h.run("--only", "links", "--format", "json", n.url("site.test", "/"))
		m := decode(t, out)
		if code != ExitOK || ruleIDs(m) != "" || privateCount(m) != 1.0 {
			t.Fatalf("code %d rules %q private %v\n%s", code, ruleIDs(m), privateCount(m), out)
		}
		if got := strings.Join(n.hitsOn("site.test"), " "); got != "/ /robots.txt" {
			t.Errorf("site.test requests %q, want the start page and robots.txt only", got)
		}
	})
}

// 開始 URL のホストがローカルかは、最初に届いた応答で決まる（429 でも）。最初の答えが公開のアドレスで 429 が返り、
// やり直しのときに 127.0.0.1 だけに解決されても、そこへは接続せず、ローカルにもならない: 点検できなかった
// （3、private-address、not_fetched.private は 1）。127.0.0.1 への接続はこの網で見える（loopbackOK で通し、記録する）。
func TestTheStartHostIsDecidedByItsFirstResponseEvenA429(t *testing.T) {
	h := newHarness(t)
	n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn, "127.0.0.1"}})
	n.loopbackOK, n.resolveNames = true, true
	var k atomic.Int32
	n.handle("site.test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close") // やり直しで解決し直させる
		if k.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow:\n")
			return
		}
		htmlPage(`<a href="/a">a</a>`)(w, r)
	})
	code, out, _ := h.run("--only", "links", "--format", "json", n.url("site.test", "/"))
	m := decode(t, out)
	cnc, _ := m["could_not_check"].(map[string]any)
	if code != ExitCouldNotCheck || cnc == nil || cnc["code"] != "private-address" || cnc["reason"] != startHostRebindReason(n.url("site.test", "/")) || privateCount(m) != 1.0 {
		t.Errorf("code %d could_not_check %v private %v, want 3, private-address with %q, 1", code, cnc, privateCount(m), startHostRebindReason(n.url("site.test", "/")))
	}
	for _, a := range n.dialedAddrs() {
		if strings.HasPrefix(a, "127.0.0.1:") {
			t.Errorf("dialed %v: the retry connected to 127.0.0.1", n.dialedAddrs())
			break
		}
	}
	if got := strings.Join(n.hitsOn("site.test"), " "); got != "/" {
		t.Errorf("site.test requests %q, want only the first (429) request", got)
	}
}

// 開始 URL のホストの最初の答えが公開とプライベートアドレスの混在なら、公開のアドレスにだけ接続し、ローカルではない: あとでプライベート
// アドレスだけに解決されても接続しない。
func TestAMixedAnswerAtTheStartConnectsToThePublicAddressAndIsNotLocal(t *testing.T) {
	h := newHarness(t)
	n := newStartHostNet(t, h, map[string][]string{"site.test": {testsupport.PublicStandIn + ",10.0.0.6", "10.0.0.6"}})
	n.handle("site.test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close") // 開始ページの接続を使い回させない: robots.txt の要求で解決し直させる
		htmlPage(`<a href="/a">a</a>`)(w, r)
	})
	code, out, _ := h.run("--only", "links", "--format", "json", n.url("site.test", "/"))
	m := decode(t, out)
	if code != ExitOK || privateCount(m) != 1.0 {
		t.Fatalf("code %d private %v\n%s", code, privateCount(m), out)
	}
	if c, _ := skipReason(m, "links"); c != "robots-unavailable" {
		t.Errorf("skipped links: %q, want robots-unavailable (robots.txt resolved only to 10.0.0.6 and was not connected)", c)
	}
	if got := strings.Join(n.hitsOn("site.test"), " "); got != "/" {
		t.Errorf("site.test requests %q, want only the start page", got)
	}
}

// ローカルなサイトの点検は今のまま: 127.0.0.1 の直書き、localhost、プライベートアドレスだけに解決される名前の開始 URL は、
// 同じホストのページ（/a）を巡回し、robots.txt も読む（not_fetched.private は 0）。
// cgnat.test は、RFC 1918 の外でグローバルでない範囲（100.64.0.0/10）に解決される開始 URL のホスト: 止めすぎないことを見る。
// ⚠ ローカルと決まることそのものはこのテストでは見ない（100.64/10 を表から外しても、公開のホストとして巡回するので通る）。
// 100.64/10 がプライベートアドレスであることは internal/fetch の境界値のテストが見る。
func TestLocalStartHostsAreCheckedAsBefore(t *testing.T) {
	for _, start := range []string{"127.0.0.1", "localhost", "intranet.test", "cgnat.test"} {
		h := newHarness(t)
		n := newStartHostNet(t, h, map[string][]string{"intranet.test": {"10.0.0.5"}, "cgnat.test": {"100.64.0.6"}})
		n.loopbackOK = true
		n.handle(start, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				fmt.Fprint(w, "User-agent: *\nDisallow:\n")
				return
			}
			htmlPage(`<a href="/a">a</a>`)(w, r)
		})
		if start == "intranet.test" || start == "cgnat.test" {
			// 最初の接続は確かめた 10.0.0.5 / 100.64.0.6 へ（dial は 127.0.0.1 へ向け直す）
			h.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				var d net.Dialer
				return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			}
		}
		code, out, _ := h.run("--only", "links", "--format", "json", n.url(start, "/"))
		m := decode(t, out)
		if code != ExitOK || ruleIDs(m) != "" || summaryOf(m)["pages_crawled"] != 2.0 || privateCount(m) != 0.0 {
			t.Errorf("%s: code %d rules %q pages %v private %v\n%s", start, code, ruleIDs(m), summaryOf(m)["pages_crawled"], privateCount(m), out)
		}
		if got := strings.Join(n.hitsOn(start), " "); got != "/ /robots.txt /a" {
			t.Errorf("%s: requests %q", start, got)
		}
	}
}

// docs/test-perspectives.md §3.3: cert の TLS の接続は、開始 URL の鎖が確かめて接続した IP へ向ける（名前を解決し直さない）。
// 開始 URL のホスト（またはリダイレクト先の公開の別のホスト）が、ページのときは公開のアドレスに、そのあと 127.0.0.1 に
// 解決されても、cert は公開のアドレスから証明書を読み、127.0.0.1 へは接続しない。
// 対照: 最初から 127.0.0.1 に解決される（ローカルな）開始 URL のホストなら、cert は 127.0.0.1 へ接続し、それがこの網で見える。
func TestCertConnectsToTheAddressTheStartChainConnectedTo(t *testing.T) {
	leaf := func(h *harness) *tls.Certificate {
		l := h.ca.Leaf(t, now, testsupport.LeafOptions{DNSNames: []string{"site.test", "other.test"}, NotBefore: now.Add(-day), NotAfter: now.Add(300 * day)})
		return &l.TLS
	}
	for _, c := range []struct {
		name, final string
		rounds      map[string][]string
	}{
		{"the start host", "site.test", map[string][]string{"site.test": {testsupport.PublicStandIn, "127.0.0.1"}}},
		{"a public host the start URL redirects to", "other.test", map[string][]string{"site.test": {testsupport.PublicStandIn, "127.0.0.1"}, "other.test": {testsupport.OtherPublicStandIn, "127.0.0.1"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			n := newStartHostNetWith(t, h, c.rounds, leaf(h))
			n.resolveNames = true // 名前で接続し直せば、2 回目の答え（127.0.0.1）へ行き、失敗になる
			n.handle("site.test", redirectTo(301, "https://other.test:"+n.port+"/").ServeHTTP)
			if c.final == "site.test" {
				n.handle("site.test", htmlPage("x"))
			}
			n.handle("other.test", htmlPage("x"))
			code, out, _ := h.run("--only", "cert", "--format", "json", "https://site.test:"+n.port+"/")
			m := decode(t, out)
			if code != ExitOK || ruleIDs(m) != "" || m["could_not_check"] != nil {
				t.Fatalf("code %d rules %q could_not_check %v\n%s", code, ruleIDs(m), m["could_not_check"], out)
			}
			for _, a := range n.dialedAddrs() {
				if strings.HasPrefix(a, "127.0.0.1:") {
					t.Errorf("dialed %s (cert resolved %s again)", a, c.final)
				}
			}
			if n.calls[c.final] != 1 {
				t.Errorf("%s resolved %d times, want 1 (the page only)", c.final, n.calls[c.final])
			}
		})
	}
	t.Run("control: a local start host is dialed at 127.0.0.1", func(t *testing.T) {
		h := newHarness(t)
		n := newStartHostNetWith(t, h, map[string][]string{"site.test": {"127.0.0.1"}}, leaf(h))
		n.loopbackOK = true
		n.handle("site.test", htmlPage("x"))
		code, out, _ := h.run("--only", "cert", "--format", "json", "https://site.test:"+n.port+"/")
		if code != ExitOK {
			t.Fatalf("code %d\n%s", code, out)
		}
		if got := strings.Join(n.dialedAddrs(), " "); got != "127.0.0.1:"+n.port+" 127.0.0.1:"+n.port {
			t.Errorf("dialed %q, want the page and cert both at 127.0.0.1", got)
		}
	})
}
