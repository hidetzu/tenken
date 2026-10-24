package crawl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/testsupport"
)

var now = testsupport.Now

// fake は fetch.Fetcher の偽物: URL → 応答。要求の順を記録する。
type fake struct {
	responses map[string]fetch.Response
	calls     []string
	errs      map[string]error
}

func (f *fake) Do(_ context.Context, r fetch.Request) (fetch.Response, error) {
	f.calls = append(f.calls, r.URL)
	if err, ok := f.errs[r.URL]; ok {
		return fetch.Response{}, err
	}
	resp, ok := f.responses[r.URL]
	if !ok {
		return fetch.Response{URL: r.URL, Status: 200}, nil
	}
	resp.URL = r.URL
	return resp, nil
}

func (f *fake) Chain(context.Context, string, netip.Addr) (fetch.Chain, error) {
	return fetch.Chain{}, nil
}

func redirect(status int, location string) fetch.Response {
	return fetch.Response{Status: status, Header: map[string][]string{"Location": {location}}}
}

func opts(max int) FollowOptions {
	// 鎖の追い方を見るテスト: 127.0.0.1 のサーバーに当てるので、開始 URL のホストを 127.0.0.1（ローカル）にする
	// （プライベートアドレスを確かめる側は fetch と crawler と cli のテスト）
	return FollowOptions{MaxRedirects: max, Now: testsupport.Clock(now), Sleep: func(time.Duration) {}, StartHost: NewStartHost("http://127.0.0.1/")}
}

func urls(hops []Hop) string {
	var out []string
	for _, h := range hops {
		out = append(out, fmt.Sprintf("%s(%d)", h.URL, h.Status))
	}
	return strings.Join(out, " ")
}

func TestOneRedirectEndsAtTheFinalPage(t *testing.T) {
	f := &fake{responses: map[string]fetch.Response{"https://a.test/": redirect(301, "/b")}}
	c, err := Follow(context.Background(), f, "https://a.test/", opts(3))
	if err != nil {
		t.Fatal(err)
	}
	if c.Stop != StopFinal || c.Final == nil || c.Final.Status != 200 || urls(c.Hops) != "https://a.test/(301) https://a.test/b(200)" {
		t.Fatalf("stop %s, hops %s", c.Stop, urls(c.Hops))
	}
	if c.Hops[0].Next != "https://a.test/b" || c.Hops[0].Location != "/b" {
		t.Fatalf("hop 0 = %+v (relative Location must be resolved)", c.Hops[0])
	}
}

// Location に従うステータスは 301 / 302 / 303 / 307 / 308（RFC 9110 §15.4）。
func TestEveryRedirectStatusIsFollowed(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		f := &fake{responses: map[string]fetch.Response{"https://a.test/": redirect(status, "/b")}}
		c, err := Follow(context.Background(), f, "https://a.test/", opts(3))
		if err != nil || c.Stop != StopFinal || len(c.Hops) != 2 {
			t.Errorf("%d: stop %s, hops %s, err %v", status, c.Stop, urls(c.Hops), err)
		}
	}
}

// docs/test-perspectives.md §3.2: 段数の境界（しきい値 - 1、ちょうど、+ 1）。既定 3 なら 4 段目で超える（docs/cli-reference.md §3.2）。
func TestTooManyAtTheBoundary(t *testing.T) {
	chainOf := func(n int) *fake {
		f := &fake{responses: map[string]fetch.Response{}}
		for i := 0; i < n; i++ {
			f.responses[fmt.Sprintf("https://a.test/%d", i)] = redirect(301, fmt.Sprintf("/%d", i+1))
		}
		return f
	}
	for redirects, want := range map[int]Stop{2: StopFinal, 3: StopFinal, 4: StopTooMany, 5: StopTooMany} {
		c, err := Follow(context.Background(), chainOf(redirects), "https://a.test/0", opts(3))
		if err != nil {
			t.Fatal(err)
		}
		if c.Stop != want {
			t.Errorf("%d redirects with --max-redirects 3: stop %s, want %s (hops %s)", redirects, c.Stop, want, urls(c.Hops))
		}
		if want == StopTooMany && len(c.Hops) != 4 {
			t.Errorf("%d redirects: followed %d hops, want 4 (3 redirects and the one that exceeds)", redirects, len(c.Hops))
		}
	}
}

func TestLoopsStopWithoutRefetching(t *testing.T) {
	for name, f := range map[string]*fake{
		"A → B → A": {responses: map[string]fetch.Response{"https://a.test/": redirect(302, "/b"), "https://a.test/b": redirect(302, "/")}},
		"A → A":     {responses: map[string]fetch.Response{"https://a.test/": redirect(301, "https://a.test/")}},
	} {
		c, err := Follow(context.Background(), f, "https://a.test/", opts(10))
		if err != nil {
			t.Fatal(err)
		}
		if c.Stop != StopLoop || c.Final != nil {
			t.Errorf("%s: stop %s", name, c.Stop)
		}
		if len(f.calls) != len(c.Hops) {
			t.Errorf("%s: %d requests for %d hops (the repeated URL must not be fetched again)", name, len(f.calls), len(c.Hops))
		}
	}
}

func TestBadLocations(t *testing.T) {
	for name, resp := range map[string]fetch.Response{
		"missing":     {Status: 302},
		"unparsable":  redirect(301, "http://[::1"),
		"not http":    redirect(301, "mailto:x@a.test"),
		"no host":     redirect(301, "https:///path"),
		"javascript":  redirect(307, "javascript:alert(1)"),
		"only spaces": redirect(301, "   "),
	} {
		f := &fake{responses: map[string]fetch.Response{"https://a.test/": resp}}
		c, err := Follow(context.Background(), f, "https://a.test/", opts(3))
		if err != nil {
			t.Fatal(err)
		}
		if c.Stop != StopBadLocation || len(c.Hops) != 1 || c.Hops[0].Next != "" {
			t.Errorf("%s: stop %s, hops %+v", name, c.Stop, c.Hops)
		}
	}
	// 300 と 304 はリダイレクトとして追わない（RFC 9110 §15.4）: Location が無くても bad-location ではなく final
	f := &fake{responses: map[string]fetch.Response{"https://a.test/": {Status: 300}}}
	if c, _ := Follow(context.Background(), f, "https://a.test/", opts(3)); c.Stop != StopFinal {
		t.Errorf("300: stop %s, want final", c.Stop)
	}
}

func TestFragmentsAndQueriesInLocation(t *testing.T) {
	f := &fake{responses: map[string]fetch.Response{"https://a.test/x?y=1": redirect(301, "../z?q=2#frag")}}
	c, _ := Follow(context.Background(), f, "https://a.test/x?y=1", opts(3))
	if c.Hops[0].Next != "https://a.test/z?q=2" {
		t.Fatalf("next = %q", c.Hops[0].Next)
	}
}

// docs/test-perspectives.md §3.1: 429 は Retry-After（無ければ既定）を待って 1 回だけやり直す。
func TestRateLimitedIsRetriedOnceAfterRetryAfter(t *testing.T) {
	cases := []struct {
		name      string
		header    map[string][]string
		second    int
		wantWait  time.Duration
		wantStop  Stop
		wantCalls int
	}{
		{"seconds", map[string][]string{"Retry-After": {"2"}}, 200, 2 * time.Second, StopFinal, 2},
		{"http date", map[string][]string{"Retry-After": {now.Add(5 * time.Second).UTC().Format(time.RFC1123)}}, 200, 5 * time.Second, StopFinal, 2},
		{"date in the past", map[string][]string{"Retry-After": {now.Add(-5 * time.Second).UTC().Format(time.RFC1123)}}, 200, 0, StopFinal, 2},
		{"absent: default wait", nil, 200, DefaultRetryWait, StopFinal, 2},
		{"garbage: default wait", map[string][]string{"Retry-After": {"soon"}}, 200, DefaultRetryWait, StopFinal, 2},
		{"still 429", map[string][]string{"Retry-After": {"1"}}, 429, time.Second, StopRateLimited, 2},
		{"too long to wait", map[string][]string{"Retry-After": {"3600"}}, 200, -1, StopRateLimited, 1},
		{"exactly the longest wait", map[string][]string{"Retry-After": {"30"}}, 200, 30 * time.Second, StopFinal, 2},
	}
	for _, c := range cases {
		calls := 0
		f := &fakeSeq{seq: []fetch.Response{{Status: 429, Header: c.header}, {Status: c.second}}, calls: &calls}
		var slept time.Duration = -1
		o := opts(3)
		o.Sleep = func(d time.Duration) { slept = d }
		ch, err := Follow(context.Background(), f, "https://a.test/", o)
		if err != nil {
			t.Fatal(err)
		}
		if ch.Stop != c.wantStop || calls != c.wantCalls || slept != c.wantWait {
			t.Errorf("%s: stop %s (want %s), %d calls (want %d), slept %v (want %v)", c.name, ch.Stop, c.wantStop, calls, c.wantCalls, slept, c.wantWait)
		}
	}
}

// fakeSeq は同じ URL に順に答える偽物。
type fakeSeq struct {
	seq   []fetch.Response
	calls *int
}

func (f *fakeSeq) Do(_ context.Context, r fetch.Request) (fetch.Response, error) {
	i := *f.calls
	*f.calls++
	if i >= len(f.seq) {
		i = len(f.seq) - 1
	}
	resp := f.seq[i]
	resp.URL = r.URL
	return resp, nil
}

func (f *fakeSeq) Chain(context.Context, string, netip.Addr) (fetch.Chain, error) {
	return fetch.Chain{}, nil
}

func TestUnreachableInTheMiddleOfTheChainIsReturnedWithItsURL(t *testing.T) {
	f := &fake{
		responses: map[string]fetch.Response{"https://a.test/": redirect(301, "https://b.test/")},
		errs:      map[string]error{"https://b.test/": &fetch.UnreachableError{URL: "https://b.test/", Reason: fetch.ReasonDNS, Err: errors.New("x")}},
	}
	c, err := Follow(context.Background(), f, "https://a.test/", opts(3))
	var u *fetch.UnreachableError
	if !errors.As(err, &u) || u.URL != "https://b.test/" || u.Reason != fetch.ReasonDNS {
		t.Fatalf("err = %v", err)
	}
	if len(c.Hops) != 1 {
		t.Fatalf("hops before the failure = %d", len(c.Hops))
	}
}

func TestSkipVerifyAndGETAreUsedForEveryHop(t *testing.T) {
	var reqs []fetch.Request
	rec := &recorder{inner: &fake{responses: map[string]fetch.Response{"https://a.test/": redirect(301, "/b")}}, reqs: &reqs}
	o := opts(3)
	o.SkipVerify = true
	if _, err := Follow(context.Background(), rec, "https://a.test/", o); err != nil {
		t.Fatal(err)
	}
	for _, r := range reqs {
		if r.Method != "GET" || !r.SkipVerify {
			t.Errorf("request %+v: want GET with SkipVerify (docs/cli-reference.md §2.1)", r)
		}
	}
}

type recorder struct {
	inner fetch.Fetcher
	reqs  *[]fetch.Request
}

func (r *recorder) Do(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	*r.reqs = append(*r.reqs, req)
	return r.inner.Do(ctx, req)
}

func (r *recorder) Chain(ctx context.Context, u string, at netip.Addr) (fetch.Chain, error) {
	return r.inner.Chain(ctx, u, at)
}

// 本物の fetch と本物のサーバーで、鎖が端から端で辿れる。
func TestEndToEndWithRealServers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/next", http.StatusFound) })
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	s := testsupport.NewHTTPServer(t, mux)
	c := fetch.New(fetch.Options{Timeout: 2 * time.Second, MaxBody: 1 << 20, Now: testsupport.Clock(now)})
	t.Cleanup(c.Close)
	ch, err := Follow(context.Background(), c, s.URL+"/", opts(3))
	if err != nil {
		t.Fatal(err)
	}
	if ch.Stop != StopFinal || ch.Final.Status != 404 || len(ch.Hops) != 2 || ch.Hops[0].Status != 302 {
		t.Fatalf("stop %s, hops %s", ch.Stop, urls(ch.Hops))
	}
}

// docs/test-perspectives.md §3.1 は「If absent, 1 second」「If `Retry-After` is over 30 seconds, do not wait」と書く。ここで値を留める（二度書くなら照らす）。
func TestRetryWaitsAreTheDocumentedValues(t *testing.T) {
	if DefaultRetryWait != time.Second || MaxRetryWait != 30*time.Second {
		t.Fatalf("default %v, max %v; docs/test-perspectives.md §3.1 says 1s and 30s", DefaultRetryWait, MaxRetryWait)
	}
}

// Retry-After は外からの数: 読めた値はそのまま（上限より大きくても）、溢れる数は「読めない」。負にならない（verification.md § Priority 3）。
func TestRetryAfterIsReadAsSentOrMarkedUnreadable(t *testing.T) {
	clock := testsupport.Clock(now)
	for raw, want := range map[string]struct {
		readable bool
		asked    time.Duration
	}{
		"2":                    {true, 2 * time.Second},
		"30":                   {true, 30 * time.Second},
		"3600":                 {true, time.Hour},
		"9223372036":           {true, 9223372036 * time.Second}, // time.Duration に収まる最大の秒数
		"9223372037":           {false, 0},                       // 収まらない: 読めない（負にならない）
		"99999999999999999999": {false, 0},
		"soon":                 {false, 0},
		" 5 ":                  {true, 5 * time.Second},
	} {
		rl := retryAfter(fetch.Response{Header: map[string][]string{"Retry-After": {raw}}}, clock)
		if rl.readable != want.readable || rl.asked != want.asked || rl.raw != strings.TrimSpace(raw) {
			t.Errorf("Retry-After %q: readable %v asked %v raw %q, want %v %v", raw, rl.readable, rl.asked, rl.raw, want.readable, want.asked)
		}
		if rl.asked < 0 {
			t.Errorf("Retry-After %q: negative wait %v", raw, rl.asked)
		}
	}
	// HTTP の日付の 3 つの形（RFC 9110 §5.6.7）
	for _, layout := range []string{time.RFC1123, time.RFC850, time.ANSIC} {
		far := now.Add(2 * time.Hour).UTC().Format(layout)
		if rl := retryAfter(fetch.Response{Header: map[string][]string{"Retry-After": {far}}}, clock); !rl.readable || rl.asked != 2*time.Hour {
			t.Errorf("date 2h ahead as %q: %+v", far, rl)
		}
	}
	if rl := retryAfter(fetch.Response{}, clock); rl.raw != "" || rl.readable {
		t.Errorf("absent: %+v", rl)
	}
}

// 鎖は、やり直したかどうかと求められた待ちを覚えている（cli の文がそれを見る）。
func TestTheChainRecordsWhetherARetryHappened(t *testing.T) {
	calls := 0
	o := opts(3)
	f := &fakeSeq{seq: []fetch.Response{{Status: 429, Header: map[string][]string{"Retry-After": {"2"}}}, {Status: 429}}, calls: &calls}
	c, _ := Follow(context.Background(), f, "https://a.test/", o)
	if !c.Retried || c.Waited != 2*time.Second || c.RetryAfterRaw != "2" || !c.RetryAfterReadable || c.Stop != StopRateLimited {
		t.Fatalf("%+v", c)
	}
	calls = 0
	f = &fakeSeq{seq: []fetch.Response{{Status: 429, Header: map[string][]string{"Retry-After": {"3600"}}}}, calls: &calls}
	c, _ = Follow(context.Background(), f, "https://a.test/", o)
	if c.Retried || c.Waited != 0 || c.RetryAfterRaw != "3600" || !c.RetryAfterReadable || c.Stop != StopRateLimited {
		t.Fatalf("%+v", c)
	}
	calls = 0
	f = &fakeSeq{seq: []fetch.Response{{Status: 429}, {Status: 429}}, calls: &calls}
	c, _ = Follow(context.Background(), f, "https://a.test/", o)
	if !c.Retried || c.Waited != DefaultRetryWait || c.RetryAfterRaw != "" || c.RetryAfterReadable {
		t.Fatalf("%+v", c)
	}
}

// docs/test-perspectives.md §2.2: 開始 URL のホストは利用者が書いた URL のホスト（大文字と小文字、末尾の点、IPv6 の角括弧、
// ポートの違いは同じホスト）。プライベートアドレスの直書きと localhost は接続する前からローカル。それ以外のプライベートアドレスの直書きは、
// 取りに行く前に止める（StartHost.Private）。
func TestStartHostIsTheHostTheUserWrote(t *testing.T) {
	s := NewStartHost("http://Site.Test.:8080/a")
	for _, u := range []string{"http://site.test/", "https://SITE.TEST:443/x", "http://site.test.:1/"} {
		if !s.Is(u) {
			t.Errorf("%s is not the start host", u)
		}
	}
	if s.Is("http://www.site.test/") || s.Local() || s.policy("http://site.test/") != fetch.PrivateIfOnlyPrivate {
		t.Errorf("www is the start host, or an undecided name is local / not decided on the first connection")
	}
	for raw, local := range map[string]bool{"http://127.0.0.1:3000/": true, "http://[::1]:3000/": true, "http://localhost:3000/": true, "http://10.0.0.5/": true, "http://site.test/": false} {
		if got := NewStartHost(raw).Local(); got != local {
			t.Errorf("%s: local %v before any connection, want %v", raw, got, local)
		}
	}
	v6 := NewStartHost("http://[::1]:3000/")
	if !v6.Is("http://[::1]:9/") || v6.Private("http://[::1]:9/") || !v6.Private("http://127.0.0.1/") || v6.policy("http://[::1]/") != fetch.PrivateAllowed {
		t.Errorf("a local IPv6 start host: the same host must be allowed, another private host stopped")
	}
	if !s.Private("http://127.0.0.1/") || s.Private("http://site.test/") {
		t.Errorf("Private: a literal private address must be stopped, a name is checked when connecting")
	}
}

// ローカルかは開始 URL のホストへの最初の応答（接続した先が確かめたプライベートアドレスだったか）で 1 度だけ決まり、変わらない。
// 決まったあとの開始 URL のホストへの要求は、ローカルなら PrivateAllowed、そうでなければ PrivateDenied。別のホストは常に PrivateDenied。
func TestTheStartHostIsLocalOnlyIfItsFirstConnectionWasPrivate(t *testing.T) {
	for _, firstPrivate := range []bool{true, false} {
		private, public := netip.MustParseAddr("10.0.0.6"), netip.MustParseAddr(testsupport.PublicStandIn)
		first, next := redirect(302, "/next"), redirect(302, "http://other.test/")
		first.ConnectedAddr, next.ConnectedAddr = public, private // 2 回目の応答は決め直さない
		if firstPrivate {
			first.ConnectedAddr, next.ConnectedAddr = private, public
		}
		var reqs []fetch.Request
		f := &recorder{inner: &fake{responses: map[string]fetch.Response{
			"http://site.test/": first, "http://site.test/next": next, "http://other.test/": {Status: 200},
		}}, reqs: &reqs}
		o := opts(5)
		o.StartHost = NewStartHost("http://site.test/")
		if _, err := Follow(context.Background(), f, "http://site.test/", o); err != nil {
			t.Fatal(err)
		}
		after := fetch.PrivateDenied
		if firstPrivate {
			after = fetch.PrivateAllowed
		}
		want := []fetch.PrivatePolicy{fetch.PrivateIfOnlyPrivate, after, fetch.PrivateDenied}
		var got []fetch.PrivatePolicy
		for _, r := range reqs {
			got = append(got, r.Private)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) || o.StartHost.Local() != firstPrivate {
			t.Errorf("first connection private %v: policies %v, want %v; local %v", firstPrivate, got, want, o.StartHost.Local())
		}
	}
}

// 429 のやり直しは、最初の応答（429）で決まった扱いで送る: 最初の接続が公開のアドレスなら PrivateDenied、プライベートアドレスなら
// PrivateAllowed。やり直しの応答で決め直さない。
func TestARetryUsesThePolicyDecidedByTheFirstResponse(t *testing.T) {
	for _, firstPrivate := range []bool{false, true} {
		first := fetch.Response{Status: 429, Header: map[string][]string{"Retry-After": {"0"}}, ConnectedAddr: netip.MustParseAddr(testsupport.PublicStandIn)}
		second := fetch.Response{Status: 200, ConnectedAddr: netip.MustParseAddr("10.0.0.6")}
		want := fetch.PrivateDenied
		if firstPrivate {
			first.ConnectedAddr, second.ConnectedAddr = second.ConnectedAddr, first.ConnectedAddr
			want = fetch.PrivateAllowed
		}
		calls := 0
		var reqs []fetch.Request
		f := &recorder{inner: &fakeSeq{seq: []fetch.Response{first, second}, calls: &calls}, reqs: &reqs}
		o := opts(3)
		o.StartHost = NewStartHost("http://site.test/")
		if _, err := Follow(context.Background(), f, "http://site.test/", o); err != nil {
			t.Fatal(err)
		}
		if len(reqs) != 2 || reqs[0].Private != fetch.PrivateIfOnlyPrivate || reqs[1].Private != want || o.StartHost.Local() != firstPrivate {
			var got []fetch.PrivatePolicy
			for _, r := range reqs {
				got = append(got, r.Private)
			}
			t.Errorf("first connection private %v: policies %v, want [%v %v]; local %v", firstPrivate, got, fetch.PrivateIfOnlyPrivate, want, o.StartHost.Local())
		}
	}
}

// 最初の応答がプロキシを通った（ViaProxy。ConnectedAddr は無い）なら、開始 URL のホストはローカルではなく、FirstConnectionProxied。
// 確かめた公開のアドレスなら、ローカルではなく、プロキシでもない。2 回目の応答では変わらない。
func TestTheStartHostRecordsWhetherItsFirstConnectionWentThroughAProxy(t *testing.T) {
	for _, c := range []struct {
		name    string
		first   netip.Addr
		proxied bool
	}{
		{"through a proxy", netip.Addr{}, true},
		{"a verified public address", netip.MustParseAddr(testsupport.PublicStandIn), false},
	} {
		s := NewStartHost("http://site.test/")
		s.observe("http://site.test/", fetch.Response{Status: 301, ConnectedAddr: c.first, ViaProxy: c.proxied})
		s.observe("https://site.test/", fetch.Response{Status: 200, ConnectedAddr: netip.MustParseAddr("10.0.0.6")})
		if s.Local() || s.FirstConnectionProxied() != c.proxied || s.policy("https://site.test/next") != fetch.PrivateDenied {
			t.Errorf("%s: local %v proxied %v policy %v, want false %v denied", c.name, s.Local(), s.FirstConnectionProxied(), s.policy("https://site.test/next"), c.proxied)
		}
	}
	if NewStartHost("http://127.0.0.1/").FirstConnectionProxied() {
		t.Errorf("a literal private start host is local from the start, not proxied")
	}
}

// FirstConnectionProxied は fetch.Response.ViaProxy だけで決まる: ConnectedAddr が無いことからは推し量らない。
// ConnectedAddr が無くても ViaProxy でなければプロキシではなく、ViaProxy なら（ConnectedAddr があっても）プロキシ。
// どちらもローカルではない（ViaProxy の応答はプライベートアドレスを確かめていない）。
func TestTheStartHostReadsViaProxyNotAMissingConnectedAddr(t *testing.T) {
	for _, c := range []struct {
		name    string
		resp    fetch.Response
		proxied bool
	}{
		{"no connected address, not through a proxy", fetch.Response{Status: 200}, false},
		{"through a proxy", fetch.Response{Status: 200, ViaProxy: true}, true},
		{"through a proxy, with an address the fetcher reported anyway", fetch.Response{Status: 200, ViaProxy: true, ConnectedAddr: netip.MustParseAddr(testsupport.PublicStandIn)}, true},
	} {
		s := NewStartHost("http://site.test/")
		s.observe("http://site.test/", c.resp)
		if s.FirstConnectionProxied() != c.proxied || s.Local() {
			t.Errorf("%s: proxied %v local %v, want %v false", c.name, s.FirstConnectionProxied(), s.Local(), c.proxied)
		}
	}
}
