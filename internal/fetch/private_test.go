package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/testsupport"
)

// fakeDNS は偽の名前解決と、どこへ接続しようとしたかの記録。⚠ 公開のネットワークにも内部のネットワークにも出ない:
// 接続はすべて 127.0.0.1 の自前のサーバーへ向け直し、プライベートアドレスへの接続はテストの失敗として記録する。
type fakeDNS struct {
	t       *testing.T
	mu      sync.Mutex
	answers map[string][][]netip.Addr // ホスト名 → 呼ばれた回ごとの答え（最後の答えを繰り返す）
	calls   map[string]int
	dialed  []string
	local   string // 向け直す先（127.0.0.1:port）
}

func newFakeDNS(t *testing.T, local string) *fakeDNS {
	return &fakeDNS{t: t, answers: map[string][][]netip.Addr{}, calls: map[string]int{}, local: local}
}

func (d *fakeDNS) answer(host string, rounds ...string) {
	for _, r := range rounds {
		var ips []netip.Addr
		for _, s := range strings.Split(r, ",") {
			ips = append(ips, netip.MustParseAddr(s))
		}
		d.answers[host] = append(d.answers[host], ips)
	}
}

func (d *fakeDNS) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rounds, ok := d.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	i := d.calls[host]
	d.calls[host]++
	if i >= len(rounds) {
		i = len(rounds) - 1
	}
	return rounds[i], nil
}

func (d *fakeDNS) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, addr)
	d.mu.Unlock()
	host, _, _ := net.SplitHostPort(addr)
	if ip, err := netip.ParseAddr(host); err == nil && isPrivateAddr(ip) && addr != d.local {
		d.t.Errorf("connected to a private address %s", addr)
		return nil, errors.New("test: refused to dial a private address")
	}
	var nd net.Dialer
	return nd.DialContext(ctx, network, d.local)
}

func (d *fakeDNS) dialedAddrs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialed...)
}

func okServer(t *testing.T) (string, *int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	s := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	u, _ := url.Parse(s.URL)
	return u.Host, &hits
}

// docs/test-perspectives.md §2.2: 名前を解決した結果がプライベートアドレスなら、接続しない（ErrPrivateAddress。到達不能ではない）。
func TestPrivateResolvedHostIsNotConnected(t *testing.T) {
	local, hits := okServer(t)
	_, port, _ := net.SplitHostPort(local)
	cases := []struct{ name, answer string }{
		{"private v4", "10.0.0.5"},
		{"loopback", "127.0.0.1"},
		{"link-local", "169.254.169.254"},
		{"unspecified", "0.0.0.0"},
		{"IPv4-mapped IPv6", "::ffff:10.0.0.5"},
		{"private v6", "fd00::1"},
		{"every answer is private", "10.0.0.5,192.168.1.1"},
	}
	for _, c := range cases {
		dns := newFakeDNS(t, "127.0.0.1:1") // 接続しようとすれば、閉じたポート
		dns.answer("intranet.test", c.answer)
		cl := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial})
		_, err := cl.Do(context.Background(), Request{Method: "GET", URL: "http://intranet.test:" + port + "/"})
		var pe *PrivateAddressError
		var ue *UnreachableError
		if !errors.Is(err, ErrPrivateAddress) || !errors.As(err, &pe) || pe.Host != "intranet.test" || errors.As(err, &ue) {
			t.Errorf("%s: err = %v, want ErrPrivateAddress (not UnreachableError)", c.name, err)
		}
		if got := dns.dialedAddrs(); len(got) != 0 {
			t.Errorf("%s: dialed %v, want nothing", c.name, got)
		}
	}
	// IP を直書きした URL も、接続の直前に同じく確かめる
	dns := newFakeDNS(t, "127.0.0.1:1")
	_, err := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial}).Do(context.Background(), Request{Method: "GET", URL: "http://10.0.0.5:" + port + "/"})
	if !errors.Is(err, ErrPrivateAddress) || len(dns.dialedAddrs()) != 0 {
		t.Errorf("IP literal: err = %v, dialed %v", err, dns.dialedAddrs())
	}
	if *hits != 0 {
		t.Errorf("the server was reached %d times", *hits)
	}
}

// プライベートでない IP には、確かめたその IP を指定して接続する: 確かめてから接続するまでに解決し直しが入らない（DNS rebinding）。
// プライベートな IP と混じっていれば、プライベートでない IP にだけ接続する。
func TestTheCheckedAddressIsTheOneConnected(t *testing.T) {
	local, hits := okServer(t)
	_, port, _ := net.SplitHostPort(local)
	dns := newFakeDNS(t, local)
	// 1 回目は公開のアドレス、2 回目からはプライベートアドレスを返す（解決し直されればプライベートアドレスに接続してしまう）
	dns.answer("site.test", testsupport.PublicStandIn, "127.0.0.1")
	dns.answer("mixed.test", "10.0.0.5,"+testsupport.OtherPublicStandIn)
	cl := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial})
	for _, u := range []string{"http://site.test:" + port + "/", "http://mixed.test:" + port + "/"} {
		resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: u})
		if err != nil || resp.Status != 200 {
			t.Fatalf("%s: %v %d", u, err, resp.Status)
		}
	}
	got := dns.dialedAddrs()
	want := []string{testsupport.PublicStandIn + ":" + port, testsupport.OtherPublicStandIn + ":" + port}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("dialed %v, want %v (the checked public addresses only)", got, want)
	}
	if dns.calls["site.test"] != 1 || *hits != 2 {
		t.Errorf("resolved site.test %d times, server hits %d", dns.calls["site.test"], *hits)
	}
}

// 例外（PrivateAllowed）と、プロキシを通る要求は確かめない（docs/cli-reference.md §5.2）。
// 名前の解決の失敗は、確かめる口でも dns のまま。
func TestAllowPrivateProxiesAndDNSFailures(t *testing.T) {
	local, hits := okServer(t)
	_, port, _ := net.SplitHostPort(local)
	dns := newFakeDNS(t, local)
	dns.answer("staging.test", "10.0.0.5")
	cl := client(t, Options{Resolve: dns.resolve, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		var nd net.Dialer
		return nd.DialContext(ctx, network, local) // 確かめない口: 名前のまま渡り、ここで自前のサーバーへ向け直す
	}})
	if resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: "http://staging.test:" + port + "/", Private: PrivateAllowed}); err != nil || resp.Status != 200 {
		t.Errorf("PrivateAllowed: %v", err)
	}
	if dns.calls["staging.test"] != 0 {
		t.Errorf("PrivateAllowed resolved the name %d times (it must not check)", dns.calls["staging.test"])
	}
	// プロキシ（127.0.0.1 の自前のサーバーが http:// の要求をそのまま受ける）を通る要求: プロキシのアドレスはプライベートアドレスでも確かめない
	proxy, _ := url.Parse("http://" + local)
	viaProxy := client(t, Options{Resolve: dns.resolve, Proxy: func(*url.URL) (*url.URL, error) { return proxy, nil }})
	if resp, err := viaProxy.Do(context.Background(), Request{Method: "GET", URL: "http://intranet.test/"}); err != nil || resp.Status != 200 {
		t.Errorf("through a proxy: %v", err)
	}
	if dns.calls["intranet.test"] != 0 || *hits != 2 {
		t.Errorf("through a proxy: resolved %d times, hits %d", dns.calls["intranet.test"], *hits)
	}
	_, err := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial}).Do(context.Background(), Request{Method: "GET", URL: "http://nowhere.test/"})
	var ue *UnreachableError
	if !errors.As(err, &ue) || ue.Reason != ReasonDNS {
		t.Errorf("DNS failure: %v, want dns", err)
	}
}

// 確かめる口も net.Dialer と同じに、黙る家族からもう一方へ移る（Happy Eyeballs）。同じ家族の中でも、黙る IP の次を試す。
// ⚠ 移らなければ、IPv6 が壊れた二重スタックのサイトを到達不能と言ってしまう（CLAUDE.md §1）。
func TestASilentAddressFallsBackToTheNext(t *testing.T) {
	local, hits := okServer(t)
	_, port, _ := net.SplitHostPort(local)
	silent := map[string]bool{"[" + testsupport.PublicStandInV6 + "]:" + port: true, testsupport.PublicStandIn + ":" + port: true}
	dns := newFakeDNS(t, local)
	dns.answer("dual.test", testsupport.PublicStandInV6+","+testsupport.OtherPublicStandIn)
	dns.answer("serial.test", testsupport.PublicStandIn+","+testsupport.ThirdPublicStandIn)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if silent[addr] {
			dns.mu.Lock()
			dns.dialed = append(dns.dialed, addr)
			dns.mu.Unlock()
			<-ctx.Done() // 黙る: 期限まで何も返さない
			return nil, ctx.Err()
		}
		return dns.dial(ctx, network, addr)
	}
	for _, c := range []struct {
		host    string
		timeout time.Duration
	}{
		{"dual.test", time.Second},       // 300ms でもう一方の家族を始める
		{"serial.test", 4 * time.Second}, // 1 つの IP に 2 秒ずつ
	} {
		cl := client(t, Options{Resolve: dns.resolve, DialContext: dial, Timeout: c.timeout})
		resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: "http://" + c.host + ":" + port + "/"})
		if err != nil || resp.Status != 200 {
			t.Errorf("%s: %v (dialed %v)", c.host, err, dns.dialedAddrs())
		}
	}
	if *hits != 2 {
		t.Errorf("server hits %d, want 2", *hits)
	}
}

// 例外として開いた接続を、確かめるべき要求が使い回さない（Transport を分けている）。答えが空なら、プライベートアドレスではなく dns。
func TestAnExemptConnectionIsNotReusedByAGuardedRequest(t *testing.T) {
	local, hits := okServer(t)
	_, port, _ := net.SplitHostPort(local)
	dns := newFakeDNS(t, local)
	dns.answer("intranet.test", "10.0.0.5")
	cl := client(t, Options{Resolve: dns.resolve, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		var nd net.Dialer
		return nd.DialContext(ctx, network, local)
	}})
	u := "http://intranet.test:" + port + "/"
	if _, err := cl.Do(context.Background(), Request{Method: "GET", URL: u, Private: PrivateAllowed}); err != nil {
		t.Fatalf("exempt: %v", err)
	}
	if _, err := cl.Do(context.Background(), Request{Method: "GET", URL: u}); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("guarded after exempt: err = %v, want ErrPrivateAddress (the kept-alive connection must not be reused)", err)
	}
	if *hits != 1 {
		t.Errorf("server hits %d, want 1", *hits)
	}
	empty := func(context.Context, string) ([]netip.Addr, error) { return nil, nil }
	_, err := client(t, Options{Resolve: empty}).Do(context.Background(), Request{Method: "GET", URL: u})
	var ue *UnreachableError
	if !errors.As(err, &ue) || ue.Reason != ReasonDNS || errors.Is(err, ErrPrivateAddress) {
		t.Errorf("empty answer: %v, want dns (not private)", err)
	}
}

// どの IP にも繋がらなければ、net.Dialer と同じく最初の IP の失敗で reason が決まる（1 つ目が時間切れ、2 つ目が拒否なら timeout）。
// 家族を競わせるときも、先に返った失敗ではなく、先に試した家族の失敗。
func TestTheFirstAddressDecidesTheReasonWhenAllFail(t *testing.T) {
	dns := newFakeDNS(t, "127.0.0.1:1")
	dns.answer("all.test", testsupport.PublicStandIn+","+testsupport.OtherPublicStandIn)
	dns.answer("dual.test", testsupport.PublicStandInV6+","+testsupport.OtherPublicStandIn) // 先の家族（v6）が黙り、もう一方（v4）が先に拒否を返す
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, testsupport.PublicStandIn+":") {
			<-ctx.Done() // 1 つ目は黙る
			return nil, ctx.Err()
		}
		if strings.HasPrefix(addr, "["+testsupport.PublicStandInV6+"]:") {
			// 先の家族は 500ms で時間切れを返す（要求の期限より前。そうでないと Transport が自分の時間切れを先に返し、区別できない）。
			// もう一方の家族は 300ms で始まり、すぐ拒否を返す: 先に返るのは拒否
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
			}
			return nil, &net.OpError{Op: "dial", Net: network, Err: dialTimeout{}}
		}
		return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
	}
	for _, host := range []string{"all.test", "dual.test"} {
		_, err := client(t, Options{Resolve: dns.resolve, DialContext: dial, Timeout: 4 * time.Second}).Do(context.Background(), Request{Method: "GET", URL: "http://" + host + ":80/"})
		var ue *UnreachableError
		if !errors.As(err, &ue) || ue.Reason != ReasonTimeout {
			t.Errorf("%s: err = %v, want timeout (the first address's failure)", host, err)
		}
	}
}

// dialTimeout は、接続の時間切れを表す net.Error（Timeout が true）。
type dialTimeout struct{}

func (dialTimeout) Error() string   { return "i/o timeout" }
func (dialTimeout) Timeout() bool   { return true }
func (dialTimeout) Temporary() bool { return true }

// docs/test-perspectives.md §2.2: 開始 URL のホストへの最初の接続（PrivateIfOnlyPrivate）は、1 回の解決の答えがどれもプライベートアドレスなら
// そこへ接続し、応答がそれを言う（ConnectedPrivate）。プライベートでないアドレスが 1 つでも混じれば、プライベートでないアドレスにだけ接続し、
// ConnectedPrivate は立たない。PrivateDenied はプライベートアドレスへ接続せず、PrivateAllowed は確かめない（どちらも ConnectedPrivate を立てない）。
func TestEachPrivatePolicyConnectsWhereItSays(t *testing.T) {
	local, _ := okServer(t)
	_, port, _ := net.SplitHostPort(local)
	cases := []struct {
		name       string
		policy     PrivatePolicy
		answer     string // "" なら URL は 127.0.0.1 の直書き
		wantErr    error
		wantDialed string
		wantPriv   bool
		wantLookup int
	}{
		{"if only private: every answer private", PrivateIfOnlyPrivate, "127.0.0.1", nil, "127.0.0.1:" + port, true, 1},
		{"if only private: an IP literal", PrivateIfOnlyPrivate, "", nil, "127.0.0.1:" + port, true, 0},
		{"if only private: a mixed answer connects to the public one only", PrivateIfOnlyPrivate, testsupport.PublicStandIn + ",127.0.0.1", nil, testsupport.PublicStandIn + ":" + port, false, 1},
		{"if only private: a public answer", PrivateIfOnlyPrivate, testsupport.PublicStandIn, nil, testsupport.PublicStandIn + ":" + port, false, 1},
		{"denied: every answer private", PrivateDenied, "127.0.0.1", ErrPrivateAddress, "", false, 1},
		{"denied: a public answer", PrivateDenied, testsupport.PublicStandIn, nil, testsupport.PublicStandIn + ":" + port, false, 1},
		{"allowed: not checked, not resolved", PrivateAllowed, "127.0.0.1", nil, "start.test:" + port, false, 0},
		{"an unknown policy is denied (fails closed)", PrivatePolicy(99), "127.0.0.1", ErrPrivateAddress, "", false, 1},
		{"a negative policy is denied (fails closed)", PrivatePolicy(-1), "127.0.0.1", ErrPrivateAddress, "", false, 1},
	}
	for _, c := range cases {
		dns := newFakeDNS(t, local)
		u := "http://127.0.0.1:" + port + "/"
		if c.answer != "" {
			dns.answer("start.test", c.answer)
			u = "http://start.test:" + port + "/"
		}
		cl := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial})
		resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: u, Private: c.policy})
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
			}
		} else if err != nil || resp.Status != 200 || resp.ConnectedPrivate() != c.wantPriv {
			t.Errorf("%s: err %v status %d ConnectedPrivate %v (want %v)", c.name, err, resp.Status, resp.ConnectedPrivate(), c.wantPriv)
		}
		// ConnectedAddr は確かめて接続した IP（確かめない PrivateAllowed では無効な値）
		if wantAddr, _, _ := net.SplitHostPort(c.wantDialed); err == nil && c.policy != PrivateAllowed && resp.ConnectedAddr.String() != wantAddr {
			t.Errorf("%s: ConnectedAddr %v, want %s", c.name, resp.ConnectedAddr, wantAddr)
		} else if c.policy == PrivateAllowed && resp.ConnectedAddr.IsValid() {
			t.Errorf("%s: ConnectedAddr %v, want none (not checked)", c.name, resp.ConnectedAddr)
		}
		if got := strings.Join(dns.dialedAddrs(), " "); got != c.wantDialed {
			t.Errorf("%s: dialed %q, want %q", c.name, got, c.wantDialed)
		}
		if dns.calls["start.test"] != c.wantLookup {
			t.Errorf("%s: resolved %d times, want %d", c.name, dns.calls["start.test"], c.wantLookup)
		}
	}
}

// ConnectedPrivate は、使い回した接続でも、TLS（HTTP/2 を含む）の上でも、接続した先を言う。プロキシを通る要求は確かめないので立たない。
func TestConnectedPrivateSurvivesReuseTLSAndIsUnknownThroughAProxy(t *testing.T) {
	proto := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		w.WriteHeader(200)
	})
	leaf := testsupport.SelfSigned(t, testsupport.Now, "127.0.0.1").TLS
	h1 := testsupport.NewTLSServer(t, proto, leaf)
	// HTTP/2 は testsupport が立てない。httptest のサーバーも 127.0.0.1 にだけ開く（テストが自分で立てたもの）
	h2 := httptest.NewUnstartedServer(proto)
	h2.EnableHTTP2 = true
	h2.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	h2.StartTLS()
	t.Cleanup(h2.Close)
	for _, c := range []struct{ srv, want string }{{h1.URL, "HTTP/1.1"}, {h2.URL, "HTTP/2.0"}} {
		su, _ := url.Parse(c.srv)
		dns := newFakeDNS(t, su.Host)
		dns.answer("start.test", "127.0.0.1")
		cl := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial})
		for i := 0; i < 2; i++ {
			resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: "https://start.test:" + su.Port() + "/", SkipVerify: true, Private: PrivateIfOnlyPrivate})
			if err != nil || !resp.ConnectedPrivate() || strings.Join(resp.Header["X-Proto"], "") != c.want {
				t.Fatalf("%s request %d (%v): err %v ConnectedPrivate %v", c.want, i+1, resp.Header["X-Proto"], err, resp.ConnectedPrivate())
			}
		}
		if len(dns.dialedAddrs()) != 1 {
			t.Errorf("%s: dialed %v, want one connection reused", c.want, dns.dialedAddrs())
		}
	}

	plain, _ := okServer(t)
	_, port, _ := net.SplitHostPort(plain)
	proxyURL, _ := url.Parse("http://" + plain) // 自前のサーバーがプロキシの代わりに答える
	pdns := newFakeDNS(t, plain)
	pc := client(t, Options{Resolve: pdns.resolve, DialContext: pdns.dial, Proxy: func(*url.URL) (*url.URL, error) { return proxyURL, nil }})
	resp, err := pc.Do(context.Background(), Request{Method: "GET", URL: "http://127.0.0.1:" + port + "/", Private: PrivateIfOnlyPrivate})
	if err != nil || resp.ConnectedPrivate() {
		t.Errorf("through a proxy: err %v ConnectedPrivate %v (want false: not checked)", err, resp.ConnectedPrivate())
	}
}

// ViaProxy は、要求がプロキシを通ったことを、ConnectedAddr とは別に直接言う（crawl.StartHost はこれで決める）。
// 直接の要求（HTTP/1.1 と HTTP/2、公開のアドレスの代わりとプライベートアドレス、使い回した接続、確かめない PrivateAllowed）では偽、
// http:// のプロキシ（http:// の相手と、https:// の相手への CONNECT）と https:// のプロキシ（CONNECT）を通った要求では真。
func TestViaProxySaysWhetherTheRequestWentThroughAProxy(t *testing.T) {
	proto := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		w.WriteHeader(200)
	})
	leaf := testsupport.SelfSigned(t, testsupport.Now, "127.0.0.1").TLS
	h1 := testsupport.NewTLSServer(t, proto, leaf)
	// HTTP/2 は testsupport が立てない。httptest のサーバーも 127.0.0.1 にだけ開く（テストが自分で立てたもの）
	h2 := httptest.NewUnstartedServer(proto)
	h2.EnableHTTP2 = true
	h2.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	h2.StartTLS()
	t.Cleanup(h2.Close)

	for _, srv := range []struct{ url, proto string }{{h1.URL, "HTTP/1.1"}, {h2.URL, "HTTP/2.0"}} {
		su, _ := url.Parse(srv.url)
		for _, c := range []struct {
			name, answer string
			policy       PrivatePolicy
			wantAddr     string // "" なら確かめない（ConnectedAddr は無効な値）
		}{
			{"a public address", testsupport.PublicStandIn, PrivateIfOnlyPrivate, testsupport.PublicStandIn},
			{"a private address", "127.0.0.1", PrivateIfOnlyPrivate, "127.0.0.1"},
			{"a public address, denied policy", testsupport.PublicStandIn, PrivateDenied, testsupport.PublicStandIn},
			{"not checked (allowed)", "127.0.0.1", PrivateAllowed, ""},
		} {
			dns := newFakeDNS(t, su.Host)
			dns.answer("start.test", c.answer)
			// 確かめない口（PrivateAllowed）も、名前のまま渡された接続を fakeDNS.dial が自前のサーバーへ向け直す
			cl := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial})
			for i := 1; i <= 2; i++ { // 2 回目は使い回した接続
				resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: "https://start.test:" + su.Port() + "/", SkipVerify: true, Private: c.policy})
				gotAddr := ""
				if resp.ConnectedAddr.IsValid() {
					gotAddr = resp.ConnectedAddr.String()
				}
				if err != nil || resp.ViaProxy || gotAddr != c.wantAddr || strings.Join(resp.Header["X-Proto"], "") != srv.proto {
					t.Errorf("%s, %s, request %d: err %v ViaProxy %v ConnectedAddr %q proto %v, want no error, false, %q, %s",
						srv.proto, c.name, i, err, resp.ViaProxy, gotAddr, resp.Header["X-Proto"], c.wantAddr, srv.proto)
				}
			}
			if len(dns.dialedAddrs()) != 1 {
				t.Errorf("%s, %s: dialed %v, want one connection reused", srv.proto, c.name, dns.dialedAddrs())
			}
		}
	}

	// プロキシを通る要求: 相手は 127.0.0.1 の直書き（プロキシが名前を解決しに外へ出ないように）。⚠ 名前は誰も解決しない
	ca := testsupport.NewCA(t, testsupport.Now)
	plainProxy, plainSeen := connectProxy(t)
	tlsProxy, tlsSeen := httpsProxy(t, ca, false)
	h1u, _ := url.Parse(h1.URL)
	for _, c := range []struct {
		name, target string
		proxy        *url.URL
		seen         func() []string
		want         string // プロキシが記録したはずのもの（通ったことの対照）
	}{
		{"an http:// target through an http:// proxy", "http://127.0.0.1:1/", &url.URL{Scheme: "http", Host: plainProxy}, plainSeen, "GET http://127.0.0.1:1/ auth="},
		{"an https:// target through CONNECT on an http:// proxy", h1.URL + "/", &url.URL{Scheme: "http", Host: plainProxy}, plainSeen, h1u.Host + " auth="},
		{"an https:// target through CONNECT on an https:// proxy", h1.URL + "/", tlsProxy, tlsSeen, h1u.Host + " auth="},
	} {
		before := len(c.seen())
		dns := newFakeDNS(t, "")
		cl := client(t, Options{RootCAs: ca.Pool(), Resolve: dns.resolve, Proxy: func(*url.URL) (*url.URL, error) { return c.proxy, nil }})
		resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: c.target, SkipVerify: true, Private: PrivateIfOnlyPrivate})
		if err != nil || resp.Status != 200 || !resp.ViaProxy || resp.ConnectedAddr.IsValid() || resp.ConnectedPrivate() {
			t.Errorf("%s: err %v status %d ViaProxy %v ConnectedAddr %v, want 200, true, none", c.name, err, resp.Status, resp.ViaProxy, resp.ConnectedAddr)
		}
		if log := c.seen(); len(log) != before+1 || log[before] != c.want {
			t.Errorf("%s: the proxy saw %v, want %q appended", c.name, log, c.want)
		}
		if len(dns.calls) != 0 {
			t.Errorf("%s: resolved %v, want no lookup", c.name, dns.calls)
		}
	}
}

// 要求がプロキシを通るかは Do と Chain が Options.Proxy を 1 度だけ呼んで決め、Transport はその決定に従う（呼び直さない）。
// Options.Proxy は 2 度目から答えを変える: Transport が呼び直せば、呼ばれた数が 2 になり、プロキシの記録と ViaProxy が食い違う。
func TestTheProxyIsDecidedOncePerRequestAndTheTransportFollowsIt(t *testing.T) {
	ca := testsupport.NewCA(t, testsupport.Now)
	leaf := ca.Leaf(t, testsupport.Now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}})
	h1 := testsupport.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), leaf.TLS)
	h1u, _ := url.Parse(h1.URL)
	direct, _ := okServer(t)
	proxyAddr, seen := connectProxy(t)
	proxy := &url.URL{Scheme: "http", Host: proxyAddr}
	for _, firstProxied := range []bool{true, false} {
		// changing は、最初の呼び出しには firstProxied の答えを、2 度目からは逆の答えを返す
		changing := func() (Options, func() int) {
			var mu sync.Mutex
			calls := 0
			proxyFor := func(*url.URL) (*url.URL, error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if (calls == 1) == firstProxied {
					return proxy, nil
				}
				return nil, nil
			}
			count := func() int {
				mu.Lock()
				defer mu.Unlock()
				return calls
			}
			return Options{RootCAs: ca.Pool(), Proxy: proxyFor}, count
		}
		for _, c := range []struct {
			name, target, logged string
		}{
			{"Do, http://", "http://" + direct + "/", "GET http://" + direct + "/ auth="},
			{"Do, https://", h1.URL + "/", h1u.Host + " auth="},
		} {
			o, calls := changing()
			cl := client(t, o)
			before := len(seen())
			resp, err := cl.Do(context.Background(), Request{Method: "GET", URL: c.target, SkipVerify: true, Private: PrivateAllowed})
			throughProxy := len(seen()) == before+1 && seen()[before] == c.logged
			if err != nil || resp.Status != 200 || calls() != 1 || resp.ViaProxy != firstProxied || throughProxy != firstProxied {
				t.Errorf("%s, first answer proxied %v: err %v status %d Options.Proxy called %d times ViaProxy %v; the proxy saw it %v (log %v), want 1 call and %v for both",
					c.name, firstProxied, err, resp.Status, calls(), resp.ViaProxy, throughProxy, seen()[before:], firstProxied)
			}
		}
		o, calls := changing()
		cl := client(t, o)
		before := len(seen())
		ch, err := cl.Chain(context.Background(), h1.URL+"/", netip.Addr{})
		throughProxy := len(seen()) == before+1 && seen()[before] == h1u.Host+" auth="
		if err != nil || len(ch.Certificates) == 0 || calls() != 1 || throughProxy != firstProxied {
			t.Errorf("Chain, first answer proxied %v: err %v Options.Proxy called %d times; the proxy saw it %v (log %v), want 1 call and %v",
				firstProxied, err, calls(), throughProxy, seen()[before:], firstProxied)
		}
	}
}

// Transport に、プロキシの決定を載せずに渡った要求（exchange を通らない実装の誤り）は、直接つながずに失敗する（errNoProxyDecision）。
// 対照: 決定（直接）を載せた要求は、同じ Transport で接続の口まで届く。
func TestARequestWithoutAProxyDecisionFailsClosedWithoutDialing(t *testing.T) {
	var mu sync.Mutex
	dialed := 0
	refused := errors.New("test: the dial stub was reached")
	cl := client(t, Options{DialContext: func(context.Context, string, string) (net.Conn, error) {
		mu.Lock()
		dialed++
		mu.Unlock()
		return nil, refused // ⚠ どこへも接続しない
	}})
	for _, policy := range []PrivatePolicy{PrivateAllowed, PrivateDenied} {
		tr := cl.shared[verifiedIdx][policy].Transport
		for _, c := range []struct {
			name     string
			decision bool
		}{
			{"no decision", false},
			{"control: a direct decision", true},
		} {
			mu.Lock()
			dialed = 0
			mu.Unlock()
			ctx := context.Background()
			if c.decision {
				ctx = context.WithValue(ctx, proxyKey{}, proxyDecision{})
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+testsupport.PublicStandIn+":1/", nil) // 公開のアドレスの代わり: 確かめる口も接続の口まで通す
			resp, err := tr.RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
			}
			mu.Lock()
			got := dialed
			mu.Unlock()
			if !c.decision && (!errors.Is(err, errNoProxyDecision) || got != 0) {
				t.Errorf("policy %v, %s: err %v, dialed %d times, want errNoProxyDecision and no dial", policy, c.name, err, got)
			}
			if c.decision && (!errors.Is(err, refused) || got != 1) {
				t.Errorf("policy %v, %s: err %v, dialed %d times, want the dial stub reached once", policy, c.name, err, got)
			}
		}
	}
}

// docs/test-perspectives.md §3.3: Chain の at が有効なら、名前を解決せずにその IP へ接続する（SNI は URL のホスト名のまま）。
// at が無効なら、今までどおり名前で接続する。
func TestChainConnectsToThePinnedAddressWithoutResolving(t *testing.T) {
	var sni []string
	var mu sync.Mutex
	leaf := testsupport.SelfSigned(t, testsupport.Now, "start.test").TLS
	s := httptest.NewUnstartedServer(http.NotFoundHandler()) // 127.0.0.1 にだけ開く（テストが自分で立てたもの）
	s.TLS = &tls.Config{GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		sni = append(sni, hi.ServerName)
		mu.Unlock()
		return &leaf, nil
	}}
	s.StartTLS()
	t.Cleanup(s.Close)
	su, _ := url.Parse(s.URL)
	dns := newFakeDNS(t, su.Host)
	dns.answer("start.test", "127.0.0.1") // 解決すればプライベートアドレス
	cl := client(t, Options{Resolve: dns.resolve, DialContext: dns.dial})
	ch, err := cl.Chain(context.Background(), "https://start.test:"+su.Port()+"/", netip.MustParseAddr(testsupport.PublicStandIn))
	if err != nil || len(ch.Certificates) == 0 {
		t.Fatalf("pinned: %v", err)
	}
	mu.Lock()
	gotSNI := strings.Join(sni, " ")
	mu.Unlock()
	if got := strings.Join(dns.dialedAddrs(), " "); got != testsupport.PublicStandIn+":"+su.Port() || dns.calls["start.test"] != 0 || gotSNI != "start.test" {
		t.Errorf("pinned: dialed %q, resolved %d times, SNI %q (want the pinned IP, no lookup, SNI start.test)", got, dns.calls["start.test"], gotSNI)
	}
	// at が無効: 名前のまま DialContext に渡す（今までどおり）
	if _, err := cl.Chain(context.Background(), "https://start.test:"+su.Port()+"/", netip.Addr{}); err != nil {
		t.Fatalf("unpinned: %v", err)
	}
	if got := dns.dialedAddrs(); len(got) != 2 || got[1] != "start.test:"+su.Port() {
		t.Errorf("unpinned: dialed %v", got)
	}
}
