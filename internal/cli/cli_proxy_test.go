package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hidetzu/tenken/internal/testsupport"
)

// docs/test-perspectives.md §2.3: プロキシを通した要求があったときは、宛先を確かめられないことを標準エラーに 1 行で言う。
// 通したかどうかは、プロキシの決め方（本番は Go の ProxyFromEnvironment）だけが決める: 環境変数があっても通さなかったなら
// （NO_PROXY の一覧、Go が捨てる値、CGI）言わない。標準出力（text / JSON）と終了コードは変えない。
func TestAProxyInUseIsSaidOnStandardError(t *testing.T) {
	const line = "tenken: a proxy is in use (%s); where the proxy connects is not checked, so private addresses are not kept out\n"
	h := newHarness(t)
	var viaProxy atomic.Int32
	// 1 つのサーバーが、サイトとプロキシを兼ねる（http:// の要求は、プロキシにも絶対 URL でそのまま届く）
	srv := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.IsAbs() {
			viaProxy.Add(1) // プロキシとして受けた
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "x")
	}))
	proxyURL, _ := url.Parse(srv.URL)
	site := srv.URL + "/"
	_, plain, plainErr := h.run("--only", "headers", site)
	cases := []struct {
		name  string
		env   map[string]string
		proxy bool   // プロキシの決め方がプロキシを返すか
		names string // "" なら 1 行は出ない
	}{
		{"no proxy", nil, false, ""},
		{"a variable is set, but the proxy function did not use it", map[string]string{"HTTP_PROXY": "http://p.test", "NO_PROXY": "localhost,*"}, false, ""},
		{"HTTP_PROXY", map[string]string{"HTTP_PROXY": "http://p.test"}, true, "HTTP_PROXY"},
		{"lower case", map[string]string{"http_proxy": "http://p.test"}, true, "http_proxy"},
		{"upper case wins", map[string]string{"HTTP_PROXY": "http://p.test", "http_proxy": "http://q.test"}, true, "HTTP_PROXY"},
	}
	for _, c := range cases {
		h.env = c.env
		h.proxyFor = func(*url.URL) (*url.URL, error) {
			if c.proxy {
				return proxyURL, nil
			}
			return nil, nil
		}
		before := viaProxy.Load()
		code, out, errOut := h.run("--only", "headers", site)
		want := plainErr
		if c.names != "" {
			want = plainErr + strings.Replace(line, "%s", c.names, 1)
		}
		if code != ExitOK || out != plain || errOut != want {
			t.Errorf("%s: code %d\nstdout changed: %v\nstderr %q\nwant   %q", c.name, code, out != plain, errOut, want)
		}
		if used := viaProxy.Load() > before; used != c.proxy {
			t.Errorf("%s: went through the proxy = %v, want %v", c.name, used, c.proxy)
		}
		_, jsonOut, _ := h.run("--only", "headers", "--format", "json", site)
		decode(t, jsonOut) // JSON の標準出力は壊れない
	}
	// 点検できなかった（exit 3）で終わる実行でも、プロキシを通した要求があれば言う: 開始 URL への要求が、閉じたポートの
	// プロキシを通って届かなかった。ここで言うのが、利用者にいちばん要る
	h.env = map[string]string{"HTTP_PROXY": "http://p.test"}
	closed, _ := url.Parse("http://" + closedPort(t))
	h.proxyFor = func(*url.URL) (*url.URL, error) { return closed, nil }
	code, _, errOut := h.run("--only", "headers", site)
	if code != ExitCouldNotCheck || !strings.HasSuffix(errOut, strings.Replace(line, "%s", "HTTP_PROXY", 1)) || strings.Count(errOut, "a proxy is in use") != 1 {
		t.Errorf("could not check through a proxy: code %d stderr %q", code, errOut)
	}
	// 接続しない実行（使い方の誤り、--version、点検を何も選ばない）では言わない
	h.env = map[string]string{"HTTP_PROXY": "http://p.test"}
	h.proxyFor = func(*url.URL) (*url.URL, error) { return proxyURL, nil }
	for _, args := range [][]string{{"--nope"}, {"--version"}, {"--only", "cert", site}} {
		_, _, errOut := h.run(args...)
		if strings.Contains(errOut, "a proxy is in use") {
			t.Errorf("%v: %q", args, errOut)
		}
	}
}
