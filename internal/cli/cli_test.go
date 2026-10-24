package cli

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/check/redirects"
	"github.com/hidetzu/tenken/internal/crawl"
	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/testsupport"
)

var now = testsupport.Now

// harness は、時計・信頼ストア・接続の作り方を差し替えた run。
type harness struct {
	t        *testing.T
	ca       *testsupport.CA
	pool     *x509.CertPool // OS の信頼ストアの代わり
	fetchers int            // 接続を開く道具を作った回数（使い方の誤りでは 0 のはず）
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	resolve  func(ctx context.Context, host string) ([]netip.Addr, error) // 偽の名前解決（nil なら OS。名前を引くテストは差し替える）
	probe    string                                                       // 同じホストの http:// の代わり（テストは 80 番を開けない）
	env      map[string]string                                            // 環境変数の代わり（proxyNotice が変数の名前を言うのに読む）
	proxyFor func(*url.URL) (*url.URL, error)                             // プロキシの決め方の代わり（本番は Go の ProxyFromEnvironment）
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ca := testsupport.NewCA(t, now)
	return &harness{t: t, ca: ca, pool: ca.Pool()}
}

func (h *harness) deps() deps {
	return deps{
		now: testsupport.Clock(now),
		newFetcher: func(o fetch.Options) fetch.Fetcher {
			h.fetchers++
			o.DialContext = h.dial
			if h.resolve != nil {
				o.Resolve = h.resolve
			}
			return fetch.New(o)
		},
		proxy: func(u *url.URL) (*url.URL, error) { // 環境変数のプロキシを読まない
			if h.proxyFor != nil {
				return h.proxyFor(u)
			}
			return nil, nil
		},
		systemPool: func() (*x509.CertPool, error) { return h.pool, nil },
		goos:       "linux", // 信頼ストアを数えられる OS として走らせる（OS ごとの違いは TestTheEmptyTrustStoreIsJudgedWhereGoReadsFiles）
		sleep:      func(time.Duration) {},
		getenv: func(k string) string {
			return h.env[k] // 本物の環境変数を読まない
		},
		probeURL: func(*url.URL) string {
			if h.probe != "" {
				return h.probe
			}
			return "http://127.0.0.1:1/" // 閉じたポート: 届かない
		},
	}
}

// serveWith は handler で https のサーバーを立てる。
func (h *harness) serveWith(handler http.Handler, lifetime, remaining time.Duration) string {
	h.t.Helper()
	na := now.Add(remaining)
	l := h.ca.Leaf(h.t, now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: na.Add(-lifetime), NotAfter: na})
	return testsupport.NewTLSServer(h.t, handler, l.TLS).URL
}

func redirectTo(status int, to string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", to)
		w.WriteHeader(status)
	})
}

func statusHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
}

// run は引数で実行し、終了コードと標準出力・標準エラーを返す。
func (h *harness) run(args ...string) (int, string, string) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut, h.deps())
	return code, out.String(), errOut.String()
}

// serve は、残り remaining・有効期間 lifetime の証明書で https のサーバーを立て、その URL を返す。
func (h *harness) serve(lifetime, remaining time.Duration) string {
	h.t.Helper()
	na := now.Add(remaining)
	l := h.ca.Leaf(h.t, now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: na.Add(-lifetime), NotAfter: na})
	return testsupport.NewTLSServer(h.t, http.NotFoundHandler(), l.TLS).URL
}

// decode は JSON の標準出力を読む。
func decode(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	return m
}

func findings(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, f := range m["findings"].([]any) {
		out = append(out, f.(map[string]any))
	}
	return out
}

func ruleIDs(m map[string]any) string {
	var ids []string
	for _, f := range findings(m) {
		ids = append(ids, f["rule"].(string))
	}
	return strings.Join(ids, ",")
}

const day = 24 * time.Hour

func TestVersion(t *testing.T) {
	code, out, errOut := newHarness(t).run("--version")
	if code != ExitOK || out != "tenken "+version()+"\n" || errOut != "" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// 既定の User-Agent は --version と同じ版を言う（docs/cli-reference.md §3.5、docs/test-perspectives.md §2.3）。
func TestTheDefaultUserAgentCarriesTheVersion(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var got []string
	site := h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("User-Agent"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html></html>")
	}), 90*day, 60*day)
	if code, out, errOut := h.run("--only", "headers", site); code != ExitFindings && code != ExitOK {
		t.Fatalf("code %d\n%s\n%s", code, out, errOut)
	}
	want := "tenken/" + version() + " (+https://github.com/hidetzu/tenken)"
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("the server received no request")
	}
	for _, ua := range got {
		if ua != want {
			t.Errorf("User-Agent %q, want %q", ua, want)
		}
	}
}

// 版の順は -ldflags の値、モジュールの版（先頭の v を外す）、0.0.0-dev（docs/test-perspectives.md §2.3）。
// go install …@v0.1.0-dev.1 とリリースのアーカイブが同じ版を言う。
func TestVersionIsTheReleaseThenTheModuleVersionThenDev(t *testing.T) {
	mod := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/hidetzu/tenken", Version: v}}
	}
	for _, c := range []struct {
		name    string
		release string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"a release build wins over the module version", "0.1.0-dev.1", mod("v9.9.9"), true, "0.1.0-dev.1"},
		{"go install of a tag gives the tag without v", "", mod("v0.1.0-dev.1"), true, "0.1.0-dev.1"},
		{"a pseudo-version is kept as Go wrote it", "", mod("v0.0.0-20261024000000-0123456789ab"), true, "0.0.0-20261024000000-0123456789ab"},
		{"(devel) is not a version", "", mod("(devel)"), true, "0.0.0-dev"},
		{"an empty module version is not a version", "", mod(""), true, "0.0.0-dev"},
		{"no build information", "", nil, false, "0.0.0-dev"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.release, c.info, c.ok); got != c.want {
				t.Errorf("resolveVersion(%q, %v, %v) = %q, want %q", c.release, c.info, c.ok, got, c.want)
			}
		})
	}
}

// docs/cli-reference.md §1、§4: 使い方の誤りは 2。何も取りに行かず、標準出力に何も出さない。
func TestUsageErrorsStartNothing(t *testing.T) {
	h := newHarness(t)
	u := h.serve(365*day, 300*day)
	// ⚠ 各行は 1 つの誤りだけを持つ（別の理由で 2 になると、その誤りを見ていない）。--only cert は未実装のチェックを外すため
	for _, args := range [][]string{
		{"--only", "cert"},
		{"--only", "cert", "--nope=1", u},
		{"--only", "cert", "--nope", "1", u},
		{"--only", "cert", "-o", "cert", u},
		{"--only", "cert", u, "https://second.example/"},
		{"--only", "cert", "example.com"},
		{"--only", "cert", "ftp://example.com/"},
		{"--only", "cert", "--skip", "links", u},
		{"--only", "certs", u},
		{"--only", "cert", "--severity", "cert.expiredd=warn", u},
		{"--only", "cert", "--severity", "cert.expired=loud", u},
		{"--only", "cert", "--ignore", "links.brokn", u},
		{"--only", "cert", "--ignore", "links.broken@[", u},
		{"--only", "cert", "--exclude", "[", u},
		{"--only", "cert", "--cert-warn", "10%", "--cert-fail", "25%", u}, // 同じ単位で逆転
		{"--only", "cert", "--cert-warn", "5d", "--cert-fail", "10d", u},
		{"--only", "cert", "--cert-warn", "ten", u},
		{"--only", "cert", "--timeout", "soon", u},
		{"--only", "cert", "--max-body", "10MB", u},
		{"--only", "cert", "--max-pages", "0", u},
		{"--only", "cert", "--format", "xml", u},
		{"--only", "cert", "--fail-on", "info", u},
		{"--only", "cert", "--quiet=yes", u},
		{"--only", "cert", u, "--timeout"},
		{"--only", "cert", "--ca-file", filepath.Join(t.TempDir(), "missing.pem"), u},
	} {
		code, out, errOut := h.run(args...)
		if code != ExitUsage || out != "" || errOut == "" {
			t.Errorf("%v: code %d, stdout %q, stderr %q; want 2, empty stdout, a sentence on stderr", args, code, out, errOut)
		}
	}
	if h.fetchers != 0 {
		t.Fatalf("a usage error opened %d fetcher(s); nothing may be fetched (docs/test-perspectives.md §2.1)", h.fetchers)
	}
}

func TestDifferentUnitsAreNotAnOrderError(t *testing.T) {
	h := newHarness(t)
	if code, _, errOut := h.run("--only", "cert", "--cert-warn", "5d", "--cert-fail", "10%", h.serve(365*day, 300*day)); code != ExitOK {
		t.Fatalf("code %d: %s", code, errOut)
	}
}

// docs/cli-reference.md §4 の各行（0 / 1）と §3.3 / §3.4 の重さの操作。
func TestExitCodesFollowSeverityFlags(t *testing.T) {
	h := newHarness(t)
	fine := h.serve(365*day, 300*day)
	expired := h.serve(90*day, -day)
	future := h.serve(90*day, 100*day)   // 10 日後に始まる
	expiring := h.serve(100*day, 20*day) // 20% → 警告だけ
	cases := []struct {
		name  string
		args  []string
		code  int
		rules string
	}{
		{"nothing to report", []string{fine}, ExitOK, ""},
		{"expired", []string{expired}, ExitFindings, "cert.expired"},
		{"not yet valid", []string{future}, ExitFindings, "cert.not-yet-valid"},
		{"a warning alone is 0", []string{expiring}, ExitOK, "cert.expiring"},
		{"--fail-on warn", []string{"--fail-on", "warn", expiring}, ExitFindings, "cert.expiring"},
		{"--severity raises", []string{"--severity", "cert.expiring=fail", expiring}, ExitFindings, "cert.expiring"},
		{"--severity lowers", []string{"--severity", "cert.expired=info", expired}, ExitOK, "cert.expired"},
		{"--severity off", []string{"--severity", "cert.expired=off", expired}, ExitOK, ""},
		{"--ignore all of a rule", []string{"--ignore", "cert.expired", expired}, ExitOK, ""},
		{"--ignore by subject", []string{"--ignore", "cert.expired@^127\\.0\\.0\\.1:", expired}, ExitOK, ""},
		{"--ignore another subject", []string{"--ignore", "cert.expired@^example\\.test:", expired}, ExitFindings, "cert.expired"},
	}
	for _, c := range cases {
		args := append([]string{"--only", "cert", "--format", "json"}, c.args...)
		code, out, errOut := h.run(args...)
		if code != c.code {
			t.Errorf("%s: code %d, want %d (stderr %q)", c.name, code, c.code, errOut)
			continue
		}
		m := decode(t, out)
		if got := ruleIDs(m); got != c.rules {
			t.Errorf("%s: findings %q, want %q", c.name, got, c.rules)
		}
		if int(m["summary"].(map[string]any)["exit_code"].(float64)) != code {
			t.Errorf("%s: summary.exit_code does not match the exit code", c.name)
		}
	}
	// 隠した・抑止した数は黙って消さない（docs/cli-reference.md §3.3、§3.4）
	_, out, _ := h.run("--only", "cert", "--format", "json", "--ignore", "cert.expired", expired)
	if s := decode(t, out)["summary"].(map[string]any); s["ignored"].(map[string]any)["cert.expired"] != 1.0 {
		t.Errorf("summary.ignored = %v, want {cert.expired: 1}", s["ignored"])
	}
	_, out, _ = h.run("--only", "cert", "--format", "json", "--severity", "cert.expired=off", expired)
	if s := decode(t, out)["summary"].(map[string]any); s["suppressed"].(map[string]any)["cert.expired"] != 1.0 {
		t.Errorf("summary.suppressed = %v, want {cert.expired: 1}", s["suppressed"])
	}
	// 重さを変えた指摘は、変えたあとの重さで出る（§5.2 の severity）
	_, out, _ = h.run("--only", "cert", "--format", "json", "--severity", "cert.expired=info", expired)
	if f := findings(decode(t, out)); f[0]["severity"] != "info" {
		t.Errorf("severity = %v, want info", f[0]["severity"])
	}
}

// docs/cli-reference.md §5.2 の形。0 件でも同じ形（TEST-PERSPECTIVES §2.3）。
func TestJSONFollowsTheSchema(t *testing.T) {
	h := newHarness(t)
	u := h.serve(90*day, 5*day) // 5.5% → expiring-soon
	code, out, _ := h.run("--only", "cert", "--format", "json", u)
	if code != ExitFindings {
		t.Fatalf("code %d", code)
	}
	m := decode(t, out)
	for _, k := range []string{"schema_version", "tool", "target", "started_at", "finished_at", "could_not_check", "summary", "findings", "skipped"} {
		if _, ok := m[k]; !ok {
			t.Errorf("top-level key %s missing", k)
		}
	}
	if m["schema_version"] != 1.0 || m["target"] != u || m["could_not_check"] != nil {
		t.Errorf("schema_version %v, target %v, could_not_check %v", m["schema_version"], m["target"], m["could_not_check"])
	}
	if tool := m["tool"].(map[string]any); tool["name"] != "tenken" || tool["version"] != version() {
		t.Errorf("tool = %v", tool)
	}
	if m["started_at"] != now.UTC().Format(time.RFC3339) || !strings.HasSuffix(m["finished_at"].(string), "Z") {
		t.Errorf("started_at %v, finished_at %v (UTC RFC 3339)", m["started_at"], m["finished_at"])
	}
	s := m["summary"].(map[string]any)
	for _, k := range []string{"fail", "warn", "info", "ignored", "suppressed", "pages_crawled", "limits_hit", "not_fetched", "exit_code"} {
		if _, ok := s[k]; !ok {
			t.Errorf("summary.%s missing", k)
		}
	}
	for _, k := range []string{"excluded", "external", "robots_disallowed", "robots_unavailable", "over_max_body", "over_limits"} {
		if _, ok := s["not_fetched"].(map[string]any)[k]; !ok {
			t.Errorf("summary.not_fetched.%s missing", k)
		}
	}
	if s["fail"] != 1.0 || s["warn"] != 0.0 || s["info"] != 0.0 {
		t.Errorf("counts = %v %v %v", s["fail"], s["warn"], s["info"])
	}
	f := findings(m)[0]
	host := strings.TrimPrefix(u, "https://")
	if f["rule"] != "cert.expiring-soon" || f["check"] != "cert" || f["severity"] != "fail" || f["subject"] != host {
		t.Errorf("finding = %v", f)
	}
	for _, k := range []string{"message", "detail", "found_on"} {
		if _, ok := f[k]; !ok {
			t.Errorf("finding.%s missing", k)
		}
	}
	if len(f["found_on"].([]any)) != 0 {
		t.Errorf("cert.* has found_on %v, want []", f["found_on"])
	}
	// --only cert: ほかのチェックは not-selected として出る（黙って落とさない）
	var sk []string
	for _, x := range m["skipped"].([]any) {
		e := x.(map[string]any)
		sk = append(sk, e["check"].(string)+":"+e["code"].(string))
	}
	if got := strings.Join(sk, ","); got != "headers:not-selected,links:not-selected,redirects:not-selected,sitemap:not-selected" {
		t.Errorf("skipped = %s", got)
	}
	// 0 件でも同じ形
	_, out, _ = h.run("--only", "cert", "--format", "json", h.serve(365*day, 300*day))
	if m := decode(t, out); len(m["findings"].([]any)) != 0 || !strings.Contains(out, `"findings": []`) {
		t.Errorf("no findings: %s", out)
	}
}

func TestTheSameInputGivesTheSameOutput(t *testing.T) {
	h := newHarness(t)
	u := h.serve(90*day, -day)
	for _, format := range []string{"text", "json"} {
		_, a, _ := h.run("--only", "cert", "--format", format, u)
		_, b, _ := h.run("--only", "cert", "--format", format, u)
		if a != b {
			t.Errorf("%s differs between runs:\n%s\n---\n%s", format, a, b)
		}
	}
}

func TestTextOutputHasTheLinesOfTheSpec(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("--only", "cert", h.serve(90*day, -day))
	if code != ExitFindings {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"tenken " + version() + " — https://", "FAIL  cert.expired", "not fetched: 0 excluded", "limits hit: none",
		"skipped: headers (not-selected), links (not-selected), redirects (not-selected), sitemap (not-selected)", "ignored: 0", "result: 1 fail, 0 warn, 0 info — exit 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q:\n%s", want, out)
		}
	}
}

// docs/cli-reference.md §2.3: http だけのサイトでは cert は対象外（not-https）。取りに行かない。
func TestHTTPStartSkipsCertAsNotHTTPS(t *testing.T) {
	h := newHarness(t)
	u := testsupport.NewHTTPServer(t, http.NotFoundHandler()).URL
	code, out, _ := h.run("--only", "cert", "--format", "json", u)
	if code != ExitOK {
		t.Fatalf("code %d", code)
	}
	m := decode(t, out)
	if !strings.Contains(out, `"code": "not-https"`) || len(m["findings"].([]any)) != 0 {
		t.Fatalf("%s", out)
	}
	if h.fetchers != 0 {
		t.Fatalf("http start opened %d fetcher(s)", h.fetchers)
	}
}

// docs/cli-reference.md §4 の 3: 届かなければ 3。JSON でも出し、could_not_check に理由。text では標準出力に何も出さない。
func TestUnreachableStartIsExit3WithTheReason(t *testing.T) {
	h := newHarness(t)
	closed := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		return "https://" + addr + "/"
	}()
	plain := strings.Replace(testsupport.NewHTTPServer(t, http.NotFoundHandler()).URL, "http://", "https://", 1)
	silent := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
			}
		}()
		return "https://" + l.Addr().String() + "/"
	}()
	cases := []struct {
		name string
		url  string
		args []string
		dial func(ctx context.Context, network, addr string) (net.Conn, error)
		code string
	}{
		{"connection refused", closed, nil, nil, "connect"},
		{"not TLS", plain, nil, nil, "tls"},
		{"silent", silent, []string{"--timeout", "300ms"}, nil, "timeout"},
		{"dns", "https://no-such-host.test/", nil, func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "no-such-host.test", IsNotFound: true}
		}, "dns"},
	}
	for _, c := range cases {
		h.dial = c.dial
		args := append([]string{"--only", "cert", "--format", "json"}, c.args...)
		code, out, errOut := h.run(append(args, c.url)...)
		if code != ExitCouldNotCheck {
			t.Errorf("%s: code %d, want 3 (stderr %q)", c.name, code, errOut)
			continue
		}
		m := decode(t, out)
		cnc, _ := m["could_not_check"].(map[string]any)
		if cnc == nil || cnc["code"] != c.code || cnc["reason"] == "" {
			t.Errorf("%s: could_not_check = %v, want code %s", c.name, m["could_not_check"], c.code)
		}
		if len(m["findings"].([]any)) != 0 || len(m["skipped"].([]any)) != 0 || m["summary"].(map[string]any)["exit_code"] != 3.0 {
			t.Errorf("%s: findings/skipped must be empty and exit_code 3: %s", c.name, out)
		}
		if !strings.Contains(errOut, "could not check") || strings.Contains(errOut, "x509") || strings.Contains(errOut, "dial tcp") {
			t.Errorf("%s: stderr %q", c.name, errOut)
		}
		code, out, errOut = h.run("--only", "cert", c.url)
		if code != ExitCouldNotCheck || out != "" || errOut == "" {
			t.Errorf("%s (text): code %d, stdout %q, stderr %q", c.name, code, out, errOut)
		}
	}
	h.dial = nil
}

// docs/cli-reference.md §4: 期限切れでも接続できれば 1 であって 3 ではない（TEST-PERSPECTIVES §2.1）。
func TestExpiredCertificateIsAFindingNotCouldNotCheck(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("--only", "cert", "--format", "json", h.serve(90*day, -30*day))
	if code != ExitCouldNotCheck && code == ExitFindings && strings.Contains(out, `"rule": "cert.expired"`) {
		return
	}
	t.Fatalf("code %d: %s", code, out)
}

func TestThresholdFlagsReachTheCheck(t *testing.T) {
	h := newHarness(t)
	u := h.serve(365*day, 20*day) // 5%: 既定では expiring-soon
	for _, c := range []struct {
		args  []string
		rules string
	}{
		{nil, "cert.expiring-soon"},
		{[]string{"--cert-fail", "10d"}, "cert.expiring"},
		{[]string{"--cert-fail", "10d", "--cert-warn", "15d"}, ""},
		{[]string{"--cert-warn", "25d", "--cert-fail", "21d"}, "cert.expiring-soon"},
	} {
		_, out, _ := h.run(append([]string{"--only", "cert", "--format", "json", u}, c.args...)...)
		if got := ruleIDs(decode(t, out)); got != c.rules {
			t.Errorf("%v: %q, want %q", c.args, got, c.rules)
		}
	}
}

// docs/cli-reference.md §3.1: --ca-file は OS の信頼ストアに加える。
func TestCAFileAddsTrust(t *testing.T) {
	h := newHarness(t)
	other := testsupport.NewCA(t, now)
	l := other.Leaf(t, now, testsupport.LeafOptions{IPs: []net.IP{net.ParseIP("127.0.0.1")}})
	u := testsupport.NewTLSServer(t, http.NotFoundHandler(), l.TLS).URL
	pemFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(pemFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Root.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := h.run("--only", "cert", "--format", "json", u); ruleIDs(decode(t, out)) != "cert.untrusted" {
		t.Fatalf("without --ca-file: %s", out)
	}
	if code, _, errOut := h.run("--only", "cert", "--ca-file", pemFile, u); code != ExitOK {
		t.Fatalf("with --ca-file: code %d %s", code, errOut)
	}
	// 加えるのであって置き換えない: OS の信頼ストアの CA はまだ通る
	if code, _, errOut := h.run("--only", "cert", "--ca-file", pemFile, h.serve(365*day, 300*day)); code != ExitOK {
		t.Fatalf("--ca-file replaced the trust store: code %d %s", code, errOut)
	}
	notPEM := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(notPEM, []byte("hello"), 0o600)
	if code, _, _ := h.run("--only", "cert", "--ca-file", notPEM, u); code != ExitUsage {
		t.Fatalf("a file without PEM: code %d, want 2", code)
	}
}

// 空の信頼ストアを止めるのは、Go がファイルからルートを読む OS（darwin / windows でも SSL_CERT_FILE / SSL_CERT_DIR があるとき）。
// OS の検証器を使うときは、空かどうかが分からないので止めない（docs/cli-reference.md §4 の 2）。
func TestTheEmptyTrustStoreIsJudgedWhereGoReadsFiles(t *testing.T) {
	empty := func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }
	for _, c := range []struct {
		goos      string
		env       map[string]string
		wantUsage bool
	}{
		{"linux", nil, true},
		{"darwin", nil, false},
		{"windows", nil, false},
		{"darwin", map[string]string{"SSL_CERT_FILE": "/nowhere/ca.pem"}, true},
		{"windows", map[string]string{"SSL_CERT_DIR": `C:\nowhere`}, true},
		{"linux", map[string]string{"SSL_CERT_FILE": "/nowhere/ca.pem"}, true},
	} {
		getenv := func(k string) string { return c.env[k] }
		_, err := trustStore("", empty, usesPlatformVerifier(c.goos, getenv))
		var ue *usageError
		if got := errors.As(err, &ue); got != c.wantUsage {
			t.Errorf("%s %v: usage error %v (%v), want %v", c.goos, c.env, got, err, c.wantUsage)
		}
	}
}

// 信頼ストアが空なら始めない（すべてを「信頼できない」と言わない。CLAUDE.md §1）。--ca-file があれば、それだけで進む。
func TestAnEmptyTrustStoreIsAUsageErrorUnlessCAFileIsGiven(t *testing.T) {
	h := newHarness(t)
	u := h.serve(365*day, 300*day)
	h.pool = x509.NewCertPool()
	if code, out, errOut := h.run("--only", "cert", u); code != ExitUsage || out != "" || !strings.Contains(errOut, "trust store") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	pemFile := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(pemFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.ca.Root.Raw}), 0o600)
	if code, _, errOut := h.run("--only", "cert", "--ca-file", pemFile, u); code != ExitOK {
		t.Fatalf("with --ca-file: code %d %s", code, errOut)
	}
}

func TestSubjectIsHostPortWithTheDefaultPort(t *testing.T) {
	// IPv6 は角括弧（RFC 3986 §3.2.2）
	for raw, want := range map[string]string{"https://example.test/": "example.test:443", "https://example.test:8443/x": "example.test:8443", "https://[::1]:444/": "[::1]:444", "https://[2001:db8::1]/": "[2001:db8::1]:443"} {
		u, _ := url.Parse(raw)
		if got := hostPort(u); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

// 既定と名前の正本は docs/cli-reference.md §3。コードの表（flagSpecs）と --help をそれに照らす（CLAUDE.md §3）。
func TestFlagsMatchCLISpec(t *testing.T) {
	b, err := os.ReadFile("../../docs/cli-reference.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	start, end := strings.Index(doc, "\n## 3. Flags"), strings.Index(doc, "\n## 4. ")
	if start < 0 || end < start {
		t.Fatal("docs/cli-reference.md has no § 3 Flags section — the cross-check would pass vacuously")
	}
	rows := regexp.MustCompile("(?m)^\\| `--([a-z-]+)[^`]*` \\| ([^|]+) \\|").FindAllStringSubmatch(doc[start:end], -1)
	if len(rows) == 0 {
		t.Fatal("no flag rows found in docs/cli-reference.md § 3")
	}
	seen := map[string]bool{}
	help := helpText()
	for _, r := range rows {
		name, def := r[1], strings.TrimSpace(r[2])
		seen[name] = true
		f, ok := lookupFlag(name)
		if !ok {
			t.Errorf("docs/cli-reference.md § 3 has --%s and the code does not", name)
			continue
		}
		if strings.HasPrefix(def, "`") { // 既定が値として書かれている行だけ照らす（（なし）などは除く）
			want := strings.Trim(def, "`")
			if strings.Contains(want, "<") { // `<名前>/<version> (+<リポジトリの URL>)` は形の説明
				continue
			}
			if f.def != want {
				t.Errorf("--%s: docs/cli-reference.md § 3 says the default is %q, the code says %q", name, want, f.def)
			}
			line := helpLine(help, name)
			if !strings.Contains(line, "(default "+want+")") {
				t.Errorf("--help's line for --%s does not say (default %s): %q", name, want, line)
			}
		}
	}
	for _, f := range flagSpecs {
		if !seen[f.name] && f.name != "help" && f.name != "version" { // --help / --version は §1 と §3.6
			t.Errorf("the code has --%s and docs/cli-reference.md § 3 does not", f.name)
		}
		if helpLine(help, f.name) == "" {
			t.Errorf("--help has no line for --%s", f.name)
		}
	}
}

// helpLine は --help の中の、そのフラグの行。
func helpLine(help, name string) string {
	for _, l := range strings.Split(help, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "--"+name+" ") || strings.TrimSpace(l) == "--"+name {
			return l
		}
	}
	return ""
}

// 人に見せる文に、ライブラリや OS の文を入れない（CLAUDE.md §4）。
func TestUsageSentencesCarryNoLibraryText(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		args    []string
		mustNot string
		must    string
	}{
		{[]string{"--only", "cert", "--exclude", "[", "https://example.test/"}, "error parsing regexp", "regular expression"},
		{[]string{"--only", "cert", "--ignore", "cert.expired@(", "https://example.test/"}, "error parsing regexp", "regular expression"},
		{[]string{"--only", "cert", "--ca-file", filepath.Join(t.TempDir(), "none.pem"), "https://example.test/"}, "no such file", "does not exist"},
		{[]string{"--only", "cert", "--ca-file", t.TempDir(), "https://example.test/"}, "is a directory", "could not be read"},
		{[]string{"--only", "cert", "--version", "--nope"}, "needs a value", "unknown flag"},
		{[]string{"--only", "cert", "https:///x"}, "scheme", "has no host"},
	} {
		code, _, errOut := h.run(c.args...)
		if code != ExitUsage || strings.Contains(errOut, c.mustNot) || !strings.Contains(errOut, c.must) {
			t.Errorf("%v: code %d, stderr %q (must not contain %q, must contain %q)", c.args, code, errOut, c.mustNot, c.must)
		}
	}
}

func TestStderrOnExit3NamesTheURLOnce(t *testing.T) {
	h := newHarness(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := "https://" + l.Addr().String() + "/"
	l.Close()
	_, _, errOut := h.run("--only", "cert", u)
	if strings.Count(errOut, u) != 1 {
		t.Fatalf("stderr names the URL %d times: %q", strings.Count(errOut, u), errOut)
	}
}

func TestSkipInsteadOfOnlySelectsTheSame(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("--skip", "redirects,links,headers,sitemap", "--format", "json", h.serve(365*day, 300*day))
	if code != ExitOK || !strings.Contains(out, `"code": "not-selected"`) {
		t.Fatalf("code %d: %s", code, out)
	}
}

// docs/cli-reference.md §4: 開始 URL（またはそのリダイレクト先）の 4xx / 5xx は links.broken で 1。巡回のチェックは start-page-broken。
func TestBrokenStartPageIsExit1(t *testing.T) {
	h := newHarness(t)
	broken := h.serveWith(statusHandler(500), 365*day, 300*day)
	code, out, _ := h.run("--only", "cert,redirects,links", "--format", "json", broken)
	if code != ExitFindings {
		t.Fatalf("code %d: %s", code, out)
	}
	m := decode(t, out)
	f := findings(m)[0]
	if ruleIDs(m) != "links.broken" || f["subject"] != broken || f["detail"].(map[string]any)["status"] != 500.0 {
		t.Fatalf("%s", out)
	}
	if ch := f["detail"].(map[string]any)["chain"].([]any); len(ch) != 0 {
		t.Fatalf("chain without redirects must be empty: %v", ch)
	}
	if fo := f["found_on"].([]any)[0].(map[string]any); fo["kind"] != "start" || fo["url"] != broken {
		t.Fatalf("found_on = %v", fo)
	}
	if !strings.Contains(out, `"code": "start-page-broken"`) {
		t.Fatalf("crawl checks must be skipped as start-page-broken: %s", out)
	}
	// 経由して壊れている: subject は壊れた URL、chain に経路、found_on は開始 URL
	start := h.serveWith(redirectTo(301, broken), 365*day, 300*day)
	_, out, _ = h.run("--only", "cert,redirects,links", "--format", "json", start)
	f = findings(decode(t, out))[0]
	if f["rule"] != "links.broken" || f["subject"] != broken || len(f["detail"].(map[string]any)["chain"].([]any)) != 2 || f["found_on"].([]any)[0].(map[string]any)["url"] != start {
		t.Fatalf("%s", out)
	}
	// links を選ばなければ出ない（利用者の選択）
	if code, out, _ := h.run("--only", "cert,redirects", "--format", "json", broken); code != ExitOK || ruleIDs(decode(t, out)) != "" {
		t.Fatalf("without links: code %d %s", code, out)
	}
}

// docs/test-perspectives.md §3.2 を CLI から。
func TestStartRedirectsThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	ok := h.serveWith(statusHandler(200), 365*day, 300*day)
	temp := h.serveWith(redirectTo(302, ok), 365*day, 300*day)
	code, out, _ := h.run("--only", "cert,redirects,links", "--format", "json", temp)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "redirects.temporary" || findings(m)[0]["severity"] != "info" || strings.Contains(out, "start-page-broken") {
		t.Fatalf("temporary: code %d %s", code, out)
	}
	// ループ: 1 で、巡回のチェックは start-page-broken
	var loop string
	loopSrv := h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, loop, 301) }), 365*day, 300*day)
	loop = loopSrv + "/"
	code, out, _ = h.run("--only", "cert,redirects,links", "--format", "json", loop)
	if code != ExitFindings || ruleIDs(decode(t, out)) != "redirects.loop" || !strings.Contains(out, "start-page-broken") {
		t.Fatalf("loop: code %d %s", code, out)
	}
	// 段数: --max-redirects 1 で 2 段は too-many（warn → 0）
	two := h.serveWith(redirectTo(301, temp), 365*day, 300*day)
	code, out, _ = h.run("--only", "cert,redirects,links", "--format", "json", "--max-redirects", "1", two)
	if code != ExitOK || !strings.Contains(ruleIDs(decode(t, out)), "redirects.too-many") || !strings.Contains(out, "start-page-broken") {
		t.Fatalf("too many: code %d %s (warn, and the crawl checks skipped as start-page-broken)", code, out)
	}
	// ダウングレード: https → http。cert は最後の https のホスト（開始 URL）
	plain := testsupport.NewHTTPServer(t, statusHandler(200)).URL
	down := h.serveWith(redirectTo(301, plain), 90*day, -day) // 開始 URL の証明書は期限切れ
	code, out, _ = h.run("--only", "cert,redirects,links", "--format", "json", down)
	if code != ExitFindings || ruleIDs(decode(t, out)) != "cert.expired,redirects.downgrade" {
		t.Fatalf("downgrade: code %d %s", code, out)
	}
	if fs := findings(decode(t, out)); fs[0]["subject"] != strings.TrimPrefix(down, "https://") {
		t.Fatalf("cert must be checked on the last https host: %v", fs[0]["subject"])
	}
	// リダイレクト後のホストの証明書を点検する（docs/cli-reference.md §2.1）: 期限切れのサーバーへ飛ぶ
	expired := h.serveWith(statusHandler(200), 90*day, -day)
	start := h.serveWith(redirectTo(308, expired), 365*day, 300*day)
	code, out, _ = h.run("--only", "cert,redirects,links", "--format", "json", start)
	if code != ExitFindings || ruleIDs(decode(t, out)) != "cert.expired" || findings(decode(t, out))[0]["subject"] != strings.TrimPrefix(expired, "https://") {
		t.Fatalf("cert after redirect: code %d %s", code, out)
	}
	// 壊れた Location
	bad := h.serveWith(redirectTo(301, "http://[::1"), 365*day, 300*day)
	if code, out, _ := h.run("--only", "redirects", "--format", "json", bad); code != ExitFindings || ruleIDs(decode(t, out)) != "redirects.bad-location" {
		t.Fatalf("bad location: code %d %s", code, out)
	}
}

// docs/cli-reference.md §2.1: 開始 URL が https なら、同じホストの http:// に 1 回だけ。https に飛ばなければ redirects.no-https。
func TestHTTPProbeFindsNoHTTPS(t *testing.T) {
	h := newHarness(t)
	site := h.serveWith(statusHandler(200), 365*day, 300*day)
	h.probe = testsupport.NewHTTPServer(t, statusHandler(200)).URL + "/" // http で 200 を返し、https に飛ばない
	code, out, _ := h.run("--only", "redirects", "--format", "json", site)
	m := decode(t, out)
	if code != ExitOK || ruleIDs(m) != "redirects.no-https" {
		t.Fatalf("code %d %s", code, out)
	}
	f := findings(m)[0]
	if f["severity"] != "warn" || f["subject"] != h.probe || f["found_on"].([]any)[0].(map[string]any)["kind"] != "probe" {
		t.Fatalf("%v", f)
	}
	// https に飛べば出ない
	h.probe = testsupport.NewHTTPServer(t, redirectTo(301, site)).URL + "/"
	if _, out, _ := h.run("--only", "redirects", "--format", "json", site); ruleIDs(decode(t, out)) != "" {
		t.Fatalf("redirects to https: %s", out)
	}
	// http が届かなければ、確かめられなかったと数える（skipped の probe-unavailable）。終了コードは 0
	h.probe = ""
	code, out, _ = h.run("--only", "redirects", "--format", "json", site)
	if code != ExitOK || ruleIDs(decode(t, out)) != "" || !strings.Contains(out, `"code": "probe-unavailable"`) || !strings.Contains(out, "could not be reached") || strings.Contains(out, "could not be checked") {
		t.Fatalf("unreachable probe: code %d %s", code, out)
	}
	// probe が飛ばした先に届かない: 届かなかった URL を名指し、probe 自身を「届かない」と言わない（cli-reference.md §6）
	h.probe = testsupport.NewHTTPServer(t, redirectTo(301, "http://127.0.0.1:1/")).URL + "/"
	_, out, _ = h.run("--only", "redirects", "--format", "json", site)
	if !strings.Contains(out, `"code": "probe-unavailable"`) || !strings.Contains(out, "redirected to http://127.0.0.1:1/, which could not be reached") {
		t.Fatalf("probe redirecting to a dead URL: %s", out)
	}
	// probe が https へ飛んでいれば、その先に届かなくても「飛ぶ」ことは観測できている: 指摘も skipped も無い
	h.probe = testsupport.NewHTTPServer(t, redirectTo(301, "https://127.0.0.1:1/")).URL + "/"
	if _, out, _ := h.run("--only", "redirects", "--format", "json", site); strings.Contains(out, "probe-unavailable") || ruleIDs(decode(t, out)) != "" {
		t.Fatalf("probe redirecting to an unreachable https URL: %s", out)
	}
	// やり直しても 429 も同じ
	h.probe = testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	})).URL + "/"
	if code, out, _ := h.run("--only", "redirects", "--format", "json", site); code != ExitOK || !strings.Contains(out, `"code": "probe-unavailable"`) || !strings.Contains(out, "answered 429") {
		t.Fatalf("429 probe: code %d %s", code, out)
	}
	// 開始 URL が http なら確かめに行かず、開始 URL 自身が https に飛ばなければ no-https（found_on は start）
	h.probe = testsupport.NewHTTPServer(t, redirectTo(301, site)).URL + "/" // 行けば https に飛ぶ = 使われていれば no-https は出ない
	httpStart := testsupport.NewHTTPServer(t, statusHandler(200)).URL
	_, out, _ = h.run("--only", "redirects", "--format", "json", httpStart)
	m = decode(t, out)
	if ruleIDs(m) != "redirects.no-https" || findings(m)[0]["found_on"].([]any)[0].(map[string]any)["kind"] != "start" || findings(m)[0]["subject"] != httpStart {
		t.Fatalf("http start: %s", out)
	}
	httpToHTTPS := testsupport.NewHTTPServer(t, redirectTo(301, site)).URL
	if _, out, _ := h.run("--only", "redirects", "--format", "json", httpToHTTPS); ruleIDs(decode(t, out)) != "" {
		t.Fatalf("http start redirecting to https: %s", out)
	}
}

// docs/cli-reference.md §4: 開始 URL（またはそのリダイレクト先）がやり直しても 429 なら 3（rate-limited）。1 回のやり直しで答えれば点検する。
func TestStartRateLimitedIsExit3(t *testing.T) {
	h := newHarness(t)
	always := h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
	}), 365*day, 300*day)
	code, out, errOut := h.run("--only", "cert,redirects,links", "--format", "json", always)
	m := decode(t, out)
	if code != ExitCouldNotCheck || m["could_not_check"].(map[string]any)["code"] != "rate-limited" || !strings.Contains(errOut, "429") {
		t.Fatalf("code %d %s %s", code, out, errOut)
	}
	n := 0
	once := h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}), 365*day, 300*day)
	if code, out, _ := h.run("--only", "cert,redirects,links", "--format", "json", once); code != ExitOK {
		t.Fatalf("429 then 200: code %d %s", code, out)
	}
	// リダイレクト先が 429 でも同じ
	viaRedirect := h.serveWith(redirectTo(301, always), 365*day, 300*day)
	if code, out, _ := h.run("--only", "redirects", "--format", "json", viaRedirect); code != ExitCouldNotCheck || !strings.Contains(out, `"rate-limited"`) {
		t.Fatalf("429 after redirect: code %d %s", code, out)
	}
}

func TestUnreachableRedirectTargetIsExit3WithThatURL(t *testing.T) {
	h := newHarness(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "https://" + l.Addr().String() + "/"
	l.Close()
	start := h.serveWith(redirectTo(301, closed), 365*day, 300*day)
	code, out, _ := h.run("--only", "cert,redirects,links", "--format", "json", start)
	m := decode(t, out)
	cnc, _ := m["could_not_check"].(map[string]any)
	if code != ExitCouldNotCheck || cnc == nil || cnc["code"] != "connect" || !strings.Contains(cnc["reason"].(string), closed) {
		t.Fatalf("code %d %s", code, out)
	}
}

func TestHelpIsOnStdoutAndExitsZero(t *testing.T) {
	code, out, errOut := newHarness(t).run("--help")
	if code != ExitOK || !strings.Contains(out, "Usage:") || errOut != "" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// 429 の 3 の文は、やり直したかどうかで変わる（やり直していないのに「2 回」と言わない。evidence.md）。
func TestRateLimitedSentenceSaysWhetherARetryHappened(t *testing.T) {
	h := newHarness(t)
	long := h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}), 365*day, 300*day)
	_, out, errOut := h.run("--only", "redirects", "--format", "json", long)
	if !strings.Contains(errOut, `with Retry-After "3600", longer than tenken waits (30s), so it was not asked again`) || strings.Contains(errOut, "twice") || !strings.Contains(out, `"rate-limited"`) {
		t.Fatalf("stderr %q", errOut)
	}
	with := func(header string) string {
		return h.serveWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if header != "" {
				w.Header().Set("Retry-After", header)
			}
			w.WriteHeader(429)
		}), 365*day, 300*day)
	}
	for header, want := range map[string]string{
		"0":    "twice (the second time after waiting 0s, as its Retry-After asked)",
		"":     "twice (the second time after waiting 1s; it sent no Retry-After)",
		"soon": `twice (the second time after waiting 1s; its Retry-After "soon" could not be read as seconds or a date)`,
	} {
		if _, _, errOut := h.run("--only", "redirects", with(header)); !strings.Contains(errOut, want) {
			t.Errorf("Retry-After %q: stderr %q, want %q", header, errOut, want)
		}
	}
}

// text の「見つかった場所」の行は、種類ごとに中身がある（空の "on " を出さない。CLAUDE.md §4）。
func TestTextLocationsAreNotEmpty(t *testing.T) {
	h := newHarness(t)
	broken := h.serveWith(statusHandler(404), 365*day, 300*day)
	h.probe = testsupport.NewHTTPServer(t, statusHandler(200)).URL + "/"
	_, out, _ := h.run("--only", "redirects,links", broken)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "on ") && (strings.TrimSpace(l) == "on" || strings.Contains(l, "on  ")) {
			t.Fatalf("empty location line: %q\n%s", l, out)
		}
	}
	if !strings.Contains(out, "on "+broken+"  (the start URL)") || !strings.Contains(out, "on "+h.probe+"  (checked by tenken)") {
		t.Fatalf("locations missing:\n%s", out)
	}
}

// 同じホストの http:// の確かめは、https に飛ぶかだけを見る（docs/cli-reference.md §5.2 の probe は no-https だけ）。
func TestTheProbeReportsOnlyNoHTTPS(t *testing.T) {
	h := newHarness(t)
	site := h.serveWith(statusHandler(200), 365*day, 300*day)
	var loop string
	l := testsupport.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, loop, 302) }))
	loop = l.URL + "/"
	h.probe = loop
	code, out, _ := h.run("--only", "redirects", "--format", "json", site)
	if code != ExitOK || ruleIDs(decode(t, out)) != "redirects.no-https" {
		t.Fatalf("a looping http probe: code %d %s (no loop / temporary from a URL the user did not give)", code, out)
	}
}

// crawl.Stop と redirects.Stop は同じ値の集合（check は crawl を import しないので写しがある。docs/design.md §2）。ずれたらここで止まる。
func TestRedirectStopValuesMatchCrawl(t *testing.T) {
	pairs := map[crawl.Stop]redirects.Stop{
		crawl.StopFinal: redirects.StopFinal, crawl.StopLoop: redirects.StopLoop, crawl.StopBadLocation: redirects.StopBadLocation,
		crawl.StopTooMany: redirects.StopTooMany, crawl.StopRateLimited: redirects.StopRateLimited, crawl.StopPrivate: redirects.StopPrivate,
		crawl.StopOverLimits: redirects.StopOverLimits,
	}
	for c, r := range pairs {
		if string(c) != string(r) {
			t.Errorf("crawl %q != redirects %q", c, r)
		}
	}
	if len(pairs) != len(crawl.Stops()) {
		t.Fatalf("crawl has %d Stop values, this table has %d: add the new one to redirects too", len(crawl.Stops()), len(pairs))
	}
	// crawl.Stops() 自体も手で保つ写しなので、follow.go の const の塊と数を照らす（`Stop = "` の行を数える）
	src, err := os.ReadFile("../crawl/follow.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), ` Stop = "`); n != len(crawl.Stops()) {
		t.Fatalf("follow.go declares %d Stop values, Stops() returns %d: add the new one to Stops() and to redirects", n, len(crawl.Stops()))
	}
}

// HTTP として読めない応答: 開始 URL なら 3 で code は malformed、リンクなら links.unreachable の reason が malformed（connect とは言わない）。
func TestMalformedResponseThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Read(make([]byte, 4096))
				c.Write([]byte("HTTP/1.1 ZZZ OK\r\n\r\n"))
			}(c)
		}
	}()
	bad := "http://" + l.Addr().String() + "/"
	code, out, _ := h.run("--only", "links", "--format", "json", bad)
	m := decode(t, out)
	reason := m["could_not_check"].(map[string]any)["reason"].(string)
	if code != ExitCouldNotCheck || m["could_not_check"].(map[string]any)["code"] != "malformed" || !strings.Contains(reason, "could read as HTTP") || strings.Contains(reason, "net/http") || strings.Contains(reason, "malformed HTTP") {
		t.Fatalf("code %d (the sentence must not carry the library's text)\n%s", code, out)
	}
	site := h.htmlSite(map[string]string{"/": fmt.Sprintf(`<a href="%s">b</a>`, bad)}, nil)
	code, out, _ = h.run("--only", "links", "--format", "json", site)
	m = decode(t, out)
	if code != ExitFindings || ruleIDs(m) != "links.unreachable" || findings(m)[0]["detail"].(map[string]any)["reason"] != "malformed" {
		t.Fatalf("code %d rules %s\n%s", code, ruleIDs(m), out)
	}
}
