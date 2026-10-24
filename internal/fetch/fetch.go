// Package fetch はネットワークに出る唯一の場所（docs/design.md §2）。
//
// HTTP の 1 往復（リダイレクトを追わない）と、TLS の証明書の鎖の取り出しだけを持つ。
// ⚠ 判断はしない: 「応答があった（ステータスとヘッダと本文）」か「使える応答が無かった（理由）」かを返すだけで、
//
//	それを指摘にするか「点検できなかった」にするかはチェックと cli が決める。
//
// ⚠ net/http や crypto/tls の型と値を外に出さない。呼び出し側はこのパッケージの型だけを見る（design.md §2 § Walls）。
package fetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Reason は、要求を送ったのに使える応答が返らなかった理由（docs/cli-reference.md §5.2 の reason の列挙値）。
type Reason string

const (
	ReasonDNS     Reason = "dns"
	ReasonConnect Reason = "connect"
	ReasonTLS     Reason = "tls"
	ReasonTimeout Reason = "timeout"
	// ReasonMalformed は、応答は届いたが HTTP として読めなかった（壊れたステータス行・ヘッダ、ヘッダの途中で切れた）。
	// 「届かない」の 4 つのどれでもない（接続はでき、最初のバイトは来た。docs/cli-reference.md §5.2）
	ReasonMalformed Reason = "malformed"
)

// IsPrivateHost は、URL のホストがプライベートアドレス（isPrivateAddr）か localhost か
// （docs/cli-reference.md §5.2 の not_fetched.private）。⚠ DNS は引かない: IP リテラルと localhost だけで決める。
// 名前を解決した結果は、接続の直前に Client が確かめる（Request.Private。docs/cli-reference.md §5.2 の private）。
func IsPrivateHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h, "]") && strings.Count(h, ":") == 1 {
		h = h[:i] // host:port（IPv6 は [..]:port なので ] を見る）
	}
	if strings.HasPrefix(h, "[") {
		if j := strings.Index(h, "]"); j >= 0 {
			h = h[1:j]
		}
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	return isPrivateAddr(ip)
}

// isPrivateAddr は、IsPrivateHost と接続の直前の確かめ（guardedDial）が共有する、プライベートアドレスの唯一の定義
// （docs/cli-reference.md §5.2 の not_fetched.private）: IANA の特殊用途アドレスの表で Globally Reachable が False の範囲
// （notGlobal）から、その中で True の範囲（globalInsideNotGlobal）を除き、マルチキャスト（224.0.0.0/4、ff00::/8）を足したもの。
// 限定ブロードキャスト（255.255.255.255）は notGlobal の 1 行。
func isPrivateAddr(ip netip.Addr) bool {
	// [::ffff:0.0.0.0] も 0.0.0.0 として見る（接続するのは中の IPv4 アドレス。notGlobal の ::ffff:0:0/96 の注）。⚠ ゾーン付きのアドレス（fe80::1%eth0）は netip.Prefix.Contains に一致しないので外す
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() {
		return false
	}
	if ip.IsMulticast() {
		return true
	}
	for _, p := range globalInsideNotGlobal {
		if p.Contains(ip) {
			return false
		}
	}
	for _, p := range notGlobal {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// notGlobal は、IANA の特殊用途アドレスの表で Globally Reachable が False の行（行ごとに表の名前と RFC）。
// ⚠ 出どころ: IANA IPv4 Special-Purpose Address Registry と IANA IPv6 Special-Purpose Address Registry
// （どちらも Last Updated 2025-10-09。2026-10-24 に読んだ）。
// ⚠ 表で N/A か値の無い行の扱い:
//   - 192.88.99.0/24（Deprecated 6to4 Relay Anycast、RFC 7526。値が無い）と 2002::/16（6to4、RFC 3056。N/A）は、
//     False の範囲の外なのでプライベートアドレスに入れない（取りに行く）。2002::/16 に埋め込まれた IPv4 アドレスは読まない。
//     ただし 192.88.99.0/24 の中の 192.88.99.2/32（6a44-relay anycast address）は False なので入れる。
//   - 2001::/32（TEREDO、RFC 4380、RFC 8190。表では N/A [2]「See Section 5 of RFC 4380 for details.」）と
//     2001:10::/28（Deprecated ORCHID、RFC 4843。値が無い）は、False の 2001::/23 の中にあり True の例外でもないので、
//     tenken の扱いとして 2001::/23 に従い、プライベートアドレスに入れる（TEREDO のアドレスは NAT の内側の機械へ届きうるので、
//     取りに行かない側に倒す）。
//
// ⚠ ::ffff:0:0/96（IPv4-mapped Address、RFC 4291）は表では False だが、ここには書かない: 接続するのは中の IPv4 アドレスなので、
// isPrivateAddr は Unmap して IPv4 の行で見る（[::ffff:8.8.8.8] はプライベートアドレスでない）。
var notGlobal = mustPrefixes(
	// IPv4
	"0.0.0.0/8",          // "This network"、RFC 791
	"10.0.0.0/8",         // Private-Use、RFC 1918
	"100.64.0.0/10",      // Shared Address Space、RFC 6598
	"127.0.0.0/8",        // Loopback、RFC 1122
	"169.254.0.0/16",     // Link Local、RFC 3927
	"172.16.0.0/12",      // Private-Use、RFC 1918
	"192.0.0.0/24",       // IETF Protocol Assignments、RFC 6890（中の 192.0.0.0/29、192.0.0.8/32、192.0.0.170/32、192.0.0.171/32 も False）
	"192.0.2.0/24",       // Documentation (TEST-NET-1)、RFC 5737
	"192.88.99.2/32",     // 6a44-relay anycast address、RFC 6751（192.88.99.0/24 の中。その /24 は値が無いので入れない）
	"192.168.0.0/16",     // Private-Use、RFC 1918
	"198.18.0.0/15",      // Benchmarking、RFC 2544
	"198.51.100.0/24",    // Documentation (TEST-NET-2)、RFC 5737
	"203.0.113.0/24",     // Documentation (TEST-NET-3)、RFC 5737
	"240.0.0.0/4",        // Reserved、RFC 1112
	"255.255.255.255/32", // Limited Broadcast、RFC 8190、RFC 919
	// IPv6
	"::/128",         // Unspecified Address、RFC 4291
	"::1/128",        // Loopback Address、RFC 4291
	"64:ff9b:1::/48", // IPv4-IPv6 Translat.（Local-Use）、RFC 8215
	"100::/64",       // Discard-Only Address Block、RFC 6666
	"100:0:0:1::/64", // Dummy IPv6 Prefix、RFC 9780
	"2001::/23",      // IETF Protocol Assignments、RFC 2928（中の 2001:2::/48 Benchmarking、RFC 5180 も False）
	"2001:db8::/32",  // Documentation、RFC 3849
	"3fff::/20",      // Documentation、RFC 9637
	"5f00::/16",      // Segment Routing (SRv6) SIDs、RFC 9602
	"fc00::/7",       // Unique-Local、RFC 4193
	"fe80::/10",      // Link-Local Unicast、RFC 4291
)

// globalInsideNotGlobal は、notGlobal の範囲の中にあって、表で Globally Reachable が True の行（出どころは notGlobal と同じ）。
// 64:ff9b::/96（IPv4-IPv6 Translat.、RFC 6052）、192.31.196.0/24（AS112-v4、RFC 7535）、192.52.193.0/24（AMT、RFC 7450）、
// 192.175.48.0/24 と 2620:4f:8000::/48（Direct Delegation AS112 Service、RFC 7534）も True だが、False の範囲の外なので要らない。
var globalInsideNotGlobal = mustPrefixes(
	"192.0.0.9/32",    // Port Control Protocol Anycast、RFC 7723
	"192.0.0.10/32",   // Traversal Using Relays around NAT Anycast、RFC 8155
	"2001:1::1/128",   // Port Control Protocol Anycast、RFC 7723
	"2001:1::2/128",   // Traversal Using Relays around NAT Anycast、RFC 8155
	"2001:1::3/128",   // DNS-SD Service Registration Protocol Anycast、RFC 9665
	"2001:3::/32",     // AMT、RFC 7450
	"2001:4:112::/48", // AS112-v6、RFC 7535
	"2001:20::/28",    // ORCHIDv2、RFC 7343
	"2001:30::/28",    // Drone Remote ID Protocol Entity Tags (DETs) Prefix、RFC 9374
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Sentence は「届かなかった」の 1 文（URL の後ろに続く形。docs/cli-reference.md §6 の unreachable。CLAUDE.md §4: 何が起きて何をすればよいか）。
// 人が読む 5 つの文はここにだけ書く（links、sitemap、cli が使う。CLAUDE.md §3）。
func (r Reason) Sentence(timeout time.Duration) string {
	switch r {
	case ReasonDNS:
		return "could not be reached: the host name could not be resolved; check the URL"
	case ReasonConnect:
		return "could not be reached: no connection could be made to the host (refused, reset, or no route); check the host and port, or try again"
	case ReasonTLS:
		return "could not be reached: the TLS handshake failed before a certificate could be read; check that the port speaks TLS"
	case ReasonTimeout:
		return fmt.Sprintf("could not be reached: no answer within %s; raise --timeout or try again", timeout)
	case ReasonMalformed:
		return "answered, but not with something tenken could read as HTTP (a broken status line or header, or the headers were cut off); check what the server sends on that port"
	default:
		return fmt.Sprintf("could not be reached (%s)", r) // 理由を足したら、ここも足す
	}
}

// UnreachableError は「到達不能」（docs/cli-reference.md §6）。errors.As で取り出して Reason を読む。
// ⚠ Error() の文は Go のエラーの文を含む。人に見せる文は cli が Reason から作る（CLAUDE.md §4）。
type UnreachableError struct {
	URL    string
	Reason Reason
	Err    error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("%s: unreachable (%s): %v", e.URL, e.Reason, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// ErrCanceled は、呼び出し側が自分で取り消したこと。⚠ 到達不能ではない（相手のせいにしない。CLAUDE.md §4-1）。
var ErrCanceled = errors.New("fetch: canceled by the caller")

// ErrPrivateAddress は、名前を解決したアドレスがどれもプライベートアドレスだったので接続しなかったこと（errors.Is で見る）。
// ⚠ 到達不能ではない: 相手は何も答えていない。tenken が取りに行かないと決めた（not_fetched.private。docs/cli-reference.md §5.2）。
var ErrPrivateAddress = errors.New("fetch: the host resolves only to private addresses")

// PrivateAddressError は ErrPrivateAddress の中身: どの URL の、どのホストだったか。
type PrivateAddressError struct {
	URL  string
	Host string
}

func (e *PrivateAddressError) Error() string {
	return fmt.Sprintf("fetch: %s: %s resolves only to private addresses; not connected", e.URL, e.Host)
}

func (e *PrivateAddressError) Is(target error) bool { return target == ErrPrivateAddress }

// Response は 1 往復の応答。リダイレクトは追わない（3xx はそのまま返る）。
type Response struct {
	URL    string
	Status int
	// Header は正規化した名前（Strict-Transport-Security など）→ 値の並び。同じヘッダが複数あれば並びに入る。
	Header map[string][]string
	// Body は MaxBody までの本文。
	Body []byte
	// Truncated は、本文が MaxBody を超えたので、それ以上を読まなかったこと。
	Truncated bool
	// Incomplete は、ステータスとヘッダは届いたが、本文が途中で切れた（接続が切れた、時間切れ）こと。
	// ⚠ 届いたステータスは捨てない: 届いた 404 は「壊れている」であって「到達不能」ではない（docs/cli-reference.md §6）。
	//   本文の欠けた応答をどう扱うかはチェックが決める。
	Incomplete bool
	// IncompleteReason は、本文が切れた理由（timeout か connect）。Incomplete でなければ ""。
	IncompleteReason Reason
	// ConnectedAddr は、接続の直前に確かめて接続した IP（使い回した接続ならそれを開いたときの IP）。
	// ⚠ 確かめなかった接続（PrivateAllowed、プロキシを通る要求）では無効な値: どこへ接続したかを tenken は知らない。
	// ⚠ 無効な値から「プロキシを通った」と推し量らない: それを言うのは ViaProxy。
	// cert の TLS の接続はこの IP へ接続する（Chain の at。名前を解決し直さない）
	ConnectedAddr netip.Addr
	// ViaProxy は、この要求がプロキシを通ったこと（Options.Proxy がプロキシを返した）。
	// ⚠ 要求ごとに Options.Proxy を 1 度だけ呼んで決め（clientFor）、Transport はその決定に従う（proxyFromDecision。決め直さない）
	ViaProxy bool
}

// ConnectedPrivate は、接続した先が、確かめたプライベートアドレスだったこと（PrivateIfOnlyPrivate で答えがどれもプライベートアドレスだったとき）。
// ConnectedAddr から決まる（同じ問いに答える値を 2 つ持たない）。確かめなかった接続では false。
func (r Response) ConnectedPrivate() bool {
	return r.ConnectedAddr.IsValid() && isPrivateAddr(r.ConnectedAddr)
}

// Location は Location ヘッダの値（無ければ ""）。
func (r Response) Location() string {
	if v := r.Header["Location"]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Request は 1 往復の要求。
type Request struct {
	Method string // "GET" か "HEAD"
	URL    string
	// SkipVerify は証明書を検証しない。開始 URL の鎖（リダイレクト先を含む）と、同じホストの http:// から辿る鎖にだけ使う
	// （証明書の問題は cert チェックが 1 回だけ報告する。docs/cli-reference.md §2.1）。巡回で集めたリンクには使わない。
	SkipVerify bool
	// WantBody は、ステータスとヘッダが届いたあとに本文を読むかを決める（nil なら読む）。false なら Body は nil で、
	// Truncated / Incomplete も立たない。⚠ 生存確認（リンク、sitemap の URL）は本文を要らない: 読むと、リンク先が
	// 巨大なファイルなら --max-body まで全部ダウンロードする
	WantBody func(url string, status int, header map[string][]string) bool
	// Private は、名前を解決した結果がプライベートアドレスのとき接続するか（docs/cli-reference.md §5.2 の private）。
	// ⚠ 既定（PrivateDenied）は接続しない（ErrPrivateAddress）。例外は開始 URL のホストだけで、決めるのは crawl.StartHost。
	// プロキシを通る要求には効かない: 接続するのはプロキシで、宛先は確かめられない（docs/cli-reference.md §3.6、§5.2）。
	Private PrivatePolicy
}

// PrivatePolicy は、名前を解決した結果がプライベートアドレスのとき接続するか。
type PrivatePolicy int

const (
	// PrivateDenied（既定）: 名前を 1 度だけ解決し、プライベートでないアドレスにだけ接続する。どれもプライベートアドレスなら接続しない（ErrPrivateAddress）
	PrivateDenied PrivatePolicy = iota
	// PrivateAllowed: 確かめずに接続する（開始 URL のホストが、プライベートアドレスだけのホストだと決まったあと）
	PrivateAllowed
	// PrivateIfOnlyPrivate: 開始 URL のホストへの最初の接続。名前を 1 度だけ解決し、答えがどれもプライベートアドレスならそこへ接続して
	// Response.ConnectedPrivate が真になる。プライベートでないアドレスが 1 つでもあれば、PrivateDenied と同じくプライベートでないアドレスにだけ接続する。
	// ⚠ 確かめた答えと接続する先は同じ 1 回の解決から決まる（確かめてから接続するまでに解決し直しが入らない）
	PrivateIfOnlyPrivate
	privatePolicies // 数（Client の http.Client の並び）。⚠ これ以上の値（と負の値）は PrivateDenied として扱う（閉じる側へ倒す）
)

// Chain は TLS の握手で相手が送った証明書の鎖。検証はしていない（検証に失敗しても中身を読むため。docs/design.md §1）。
// ⚠ Do と同じ http.Transport を通して取る（プロキシ、タイムアウト、取り消しの扱いを 2 か所に持たない。CLAUDE.md §3）。
// ⚠ ただし HTTP の要求は送らない: 相手の証明書を受け取った時点で握手を中断する（docs/cli-reference.md §2.1: cert は TLS で接続するだけ）。
type Chain struct {
	Certificates []*x509.Certificate
}

// Fetcher は、crawl とチェックが使うインターフェース。テストでは偽物に差し替える。
type Fetcher interface {
	Do(ctx context.Context, req Request) (Response, error)
	// Chain の at は接続する IP（Response.ConnectedAddr）。無効な値なら名前を解決する（プロキシを通るときは at を使わない）
	Chain(ctx context.Context, url string, at netip.Addr) (Chain, error)
}

// Options は Client の設定。値の意味と既定は docs/cli-reference.md §3。
type Options struct {
	Timeout   time.Duration  // 1 要求あたり（--timeout）。0 より大きくなければならない
	MaxBody   int64          // 読む本文の上限（--max-body）。0 より大きく、math.MaxInt64 より小さくなければならない
	UserAgent string         // --user-agent
	RootCAs   *x509.CertPool // 信頼する CA（OS の信頼ストアに --ca-file を加えたもの）。nil なら OS の信頼ストア
	// Now は証明書を検証するときの時刻。⚠ 必須: nil のまま渡すと crypto/tls が実際の時計を読む（docs/design.md §2 § Walls）。
	Now func() time.Time
	// Proxy は、相手の URL に対して使うプロキシを返す（nil ならプロキシを使わない）。nil の関数なら環境変数
	// （HTTP_PROXY / HTTPS_PROXY / NO_PROXY。docs/cli-reference.md §3.6）に従う。テストでは差し替える。
	Proxy func(target *url.URL) (*url.URL, error)
	// DialContext は接続を開く関数。nil なら net.Dialer。テストで DNS の失敗などを再現するために差し替える。
	// ⚠ プライベートアドレスを確かめる口（guardedDial）は、Resolve で解決した IP を addr に入れてこれを呼ぶ。
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// Resolve はホスト名を IP に解決する関数。nil なら net.DefaultResolver。テストで偽の DNS に差し替える。
	// 失敗は *net.DNSError で返す（ReasonDNS になる）。
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// EnvProxy は、環境変数（HTTP_PROXY / HTTPS_PROXY / NO_PROXY。docs/cli-reference.md §3.6）に従ってプロキシを返す。
// Options.Proxy が nil のときの既定。⚠ Go は URL として壊れた値を黙って捨てて直接つなぐ（golang.org/x/net/http/httpproxy）。
// 拒まずに Go に任せる（docs/cli-reference.md §3.6）。
func EnvProxy(target *url.URL) (*url.URL, error) {
	return http.ProxyFromEnvironment(&http.Request{URL: target})
}

// Client は Fetcher の実装。使い終わったら Close する（接続と goroutine を残さない。.claude/rules/go.md）。
type Client struct {
	opts Options
	// shared は、https:// のプロキシを通らない要求に使う http.Client（握手は相手との 1 回だけ）。
	// [検証するか][PrivatePolicy]。⚠ 扱いの違う要求は Transport（接続の使い回し）を分ける:
	// 例外として開いたプライベートアドレスへの接続を、確かめるべき要求が使い回さないように。
	shared [2][privatePolicies]*http.Client
}

const verifiedIdx, unverifiedIdx = 0, 1

var _ Fetcher = (*Client)(nil)

// New は Client を作る。⚠ Options の誤りは呼び出し側（cli）の実装の誤りなので panic する（外からの入力ではない）。
func New(o Options) *Client {
	switch {
	case o.Timeout <= 0:
		panic("fetch: Options.Timeout must be positive")
	case o.MaxBody <= 0 || o.MaxBody == math.MaxInt64:
		panic("fetch: Options.MaxBody must be in (0, math.MaxInt64)")
	case o.Now == nil:
		panic("fetch: Options.Now is required (a nil clock makes crypto/tls read the wall clock)")
	}
	if o.DialContext == nil {
		o.DialContext = (&net.Dialer{}).DialContext
	}
	if o.Proxy == nil {
		o.Proxy = EnvProxy
	}
	if o.Resolve == nil {
		o.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	c := &Client{opts: o}
	for p := PrivatePolicy(0); p < privatePolicies; p++ {
		c.shared[verifiedIdx][p] = c.httpClient(&tls.Config{RootCAs: o.RootCAs, Time: o.Now}, true, p)
		c.shared[unverifiedIdx][p] = c.httpClient(&tls.Config{InsecureSkipVerify: true, Time: o.Now}, true, p) //nolint:gosec // 自分のオリジン、開始 URL の鎖と http の probe の鎖にだけ（Request.SkipVerify）
	}
	return c
}

// httpClient は http.Client を作る。PrivateAllowed でなければ、接続の直前にプライベートアドレスを確かめる（guardedDial）。
func (c *Client) httpClient(cfg *tls.Config, keepAlive bool, p PrivatePolicy) *http.Client {
	dial := c.opts.DialContext
	if p != PrivateAllowed {
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return c.guardedDial(ctx, network, addr, p)
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               proxyFromDecision,
			DialContext:         dial,
			TLSClientConfig:     cfg,
			TLSHandshakeTimeout: c.opts.Timeout,
			ForceAttemptHTTP2:   true,
			DisableKeepAlives:   !keepAlive,
		},
	}
}

// proxyKey は、1 回の要求がプロキシを通るかの決定を要求の context に載せる鍵（値は proxyDecision）。
type proxyKey struct{}

// proxyDecision は、Do（clientFor）と Chain が Options.Proxy を 1 度だけ呼んで決めた、その要求のプロキシ（nil なら直接）。
// exchange が要求の context に載せ、Transport（proxyFromDecision）はそれに従う。⚠ Transport が Options.Proxy を呼び直すと、
// Response.ViaProxy と実際の経路が別々の答えになりうる（同じ問いに答える値を 2 つ持たない。CLAUDE.md §3）。
type proxyDecision struct{ proxy *url.URL }

// errNoProxyDecision は、exchange を通らずに Transport に渡った要求（実装の誤り）。⚠ 直接つながずに失敗する（閉じる側へ倒す）。
var errNoProxyDecision = errors.New("fetch: the request carries no proxy decision")

// proxyFromDecision は http.Transport の Proxy: 要求の context に載った決定を返すだけで、自分では決めない。
func proxyFromDecision(r *http.Request) (*url.URL, error) {
	d, ok := r.Context().Value(proxyKey{}).(proxyDecision)
	if !ok {
		return nil, errNoProxyDecision
	}
	return d.proxy, nil
}

// target は、相手の握手をどう扱うか。
type target int

const (
	verifyTarget  target = iota // 検証する
	skipTarget                  // 検証しない（Request.SkipVerify: 開始 URL の鎖と http の probe の鎖）
	captureTarget               // 鎖を受け取って、そこで握手を中断する（Chain）
)

// errGotChain は、Chain が鎖を受け取ったあと、要求を送らないために握手を中断する印。
var errGotChain = errors.New("fetch: got the certificate chain")

// oneShot は 1 回の要求のための http.Client を作る。⚠ https:// のプロキシを通るときと Chain に使う。
// https:// のプロキシを通ると握手が 2 回ある（プロキシ、相手の順）。共有の Transport では二つを見分けられず、
// 検証を外す設定がプロキシにまで効き、プロキシの鎖を相手の鎖と取り違える。
// ここでは握手を数え、プロキシとの握手は必ず検証し、相手との握手だけを policy で扱う。名前は分かっているので自分で検証する。
// pin が有効で、プロキシを通らないなら、名前を解決せずに pin へ接続する（SNI と名前の確かめは URL のホスト名のまま）。
func (c *Client) oneShot(u *url.URL, proxy *url.URL, policy target, capture func([]*x509.Certificate), pin netip.Addr) *http.Client {
	httpsProxy := proxy != nil && proxy.Scheme == "https"
	var n atomic.Int32
	cfg := &tls.Config{InsecureSkipVerify: true, Time: c.opts.Now, VerifyConnection: func(cs tls.ConnectionState) error { //nolint:gosec // 検証は VerifyConnection が自分で行う
		if httpsProxy && n.Add(1) == 1 {
			return c.verifyChain(cs.PeerCertificates, proxy.Hostname())
		}
		switch policy {
		case captureTarget:
			capture(cs.PeerCertificates)
			return errGotChain
		case skipTarget:
			return nil
		}
		return c.verifyChain(cs.PeerCertificates, u.Hostname())
	}}
	hc := c.httpClient(cfg, false, PrivateAllowed) // プロキシを通る要求と、pin の無い Chain: 確かめない
	if pin.IsValid() && proxy == nil {
		// 鎖で確かめて接続した IP に接続する: 同じ名前を解決し直さない（DNS rebinding でプライベートアドレスへ握手しに行かない）
		hc.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return c.dialSerial(ctx, network, port, []netip.Addr{pin})
		}
	}
	return hc
}

// checkedConn は、確かめた IP（addr）に開いた接続（dialSerial が作る。Response.ConnectedAddr）。
type checkedConn struct {
	net.Conn
	addr netip.Addr
}

// guardedDial は、ホスト名を 1 度だけ解決し、プライベートでない IP にだけ、その IP を指定して接続する。
// ⚠ 確かめた IP に接続するので、確かめてから接続するまでの間に解決し直しが入らない（DNS rebinding）。
// どの IP もプライベートアドレスなら、PrivateIfOnlyPrivate ならそれらに接続し、そうでなければ接続せず *PrivateAddressError を返す。
func (c *Client) guardedDial(ctx context.Context, network, addr string, p PrivatePolicy) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = c.opts.Resolve(ctx, host); err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		// 答えが空: 得られなかったのであって、プライベートアドレスだったのではない（evidence.md）
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var allowed []netip.Addr
	for _, ip := range ips {
		if !isPrivateAddr(ip) {
			allowed = append(allowed, ip.Unmap())
		}
	}
	if len(allowed) == 0 {
		if p != PrivateIfOnlyPrivate {
			return nil, &PrivateAddressError{Host: host}
		}
		// どの答えもプライベートアドレス: 開始 URL のホストはプライベートアドレスだけのホスト（ローカル）。この 1 回の解決の答えに接続する
		for _, ip := range ips {
			allowed = append(allowed, ip.Unmap())
		}
	}
	return c.dialAddrs(ctx, network, port, allowed) // 接続は checkedConn（どの IP に接続したかを持つ）
}

// fallbackDelay は net.Dialer の既定の FallbackDelay と同じ値（RFC 8305 の Happy Eyeballs: 先に試す家族が
// この時間で繋がらなければ、もう一方の家族も始める）。
const fallbackDelay = 300 * time.Millisecond

// dialAddrs は、確かめた IP に net.Dialer と同じ形で接続する: IPv6 と IPv4 を競わせ（先に試すのは答えの最初の家族）、
// 同じ家族の中は 1 つずつ試す。⚠ 名前のまま net.Dialer に渡すと解決し直されるので、IP を 1 つずつ渡す。
// ⚠ 1 つ目の家族が黙っても、もう一方に繋がれば到達不能と言わない（CLAUDE.md §1）。
// ⚠ 始めた goroutine は、返る前にすべて待つ（.claude/rules/go.md § Concurrency）。遅れて繋がった接続は閉じる。
func (c *Client) dialAddrs(ctx context.Context, network, port string, ips []netip.Addr) (net.Conn, error) {
	var primaries, fallbacks []netip.Addr
	for _, ip := range ips {
		if len(primaries) == 0 || ip.Is4() == primaries[0].Is4() {
			primaries = append(primaries, ip)
		} else {
			fallbacks = append(fallbacks, ip)
		}
	}
	if len(fallbacks) == 0 {
		return c.dialSerial(ctx, network, port, primaries)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn    net.Conn
		err     error
		primary bool
	}
	results := make(chan result, 2)
	dial := func(list []netip.Addr, primary bool) {
		conn, err := c.dialSerial(ctx, network, port, list)
		results <- result{conn, err, primary}
	}
	go dial(primaries, true)
	pending, fallbackStarted := 1, false
	startFallback := func() {
		if !fallbackStarted {
			fallbackStarted = true
			pending++
			go dial(fallbacks, false)
		}
	}
	timer := time.NewTimer(fallbackDelay)
	defer timer.Stop()
	var primaryErr, fallbackErr error
	for pending > 0 {
		select {
		case <-timer.C:
			startFallback()
		case r := <-results:
			pending--
			if r.err == nil {
				cancel()
				for ; pending > 0; pending-- {
					if late := <-results; late.conn != nil {
						late.conn.Close()
					}
				}
				return r.conn, nil
			}
			if r.primary {
				primaryErr = r.err
			} else {
				fallbackErr = r.err
			}
			startFallback() // 先の家族が早く失敗した: 待たずにもう一方を始める
		}
	}
	// net.Dialer と同じく、先に試した家族（答えの最初の IP）の失敗を返す。どちらが先に返ったかでは決めない
	if primaryErr != nil {
		return nil, primaryErr
	}
	return nil, fallbackErr
}

// minDialShare は、1 つの IP に与える時間の下限（net.Dialer の partialDeadline の saneMinimum と同じ値）。
const minDialShare = 2 * time.Second

// dialSerial は、同じ家族の IP を 1 つずつ試す。1 つの IP には --timeout を残りの数で割った時間を与える（下限 minDialShare。
// 要求全体の期限は ctx が持つ）。⚠ 時計は読まない（docs/design.md §2 § Walls）: 残り時間ではなく --timeout から割る。
func (c *Client) dialSerial(ctx context.Context, network, port string, ips []netip.Addr) (net.Conn, error) {
	var firstErr error
	for i, ip := range ips {
		share := c.opts.Timeout / time.Duration(len(ips)-i)
		if share < minDialShare {
			share = minDialShare
		}
		actx, cancel := context.WithTimeout(ctx, share)
		conn, err := c.opts.DialContext(actx, network, net.JoinHostPort(ip.String(), port))
		cancel()
		if err == nil {
			return &checkedConn{Conn: conn, addr: ip}, nil
		}
		if firstErr == nil {
			firstErr = err // net.Dialer と同じく最初の IP のエラーを返す（reason はそれで決まる）
		}
		if ctx.Err() != nil {
			break // 要求全体の期限か取り消し: 残りは試さない
		}
	}
	return nil, firstErr
}

// verifyChain は、crypto/tls が自分で行う検証と同じことをする（信頼する CA、名前、渡された時計）。
func (c *Client) verifyChain(certs []*x509.Certificate, name string) error {
	if len(certs) == 0 {
		return errors.New("fetch: no certificate")
	}
	inter := x509.NewCertPool()
	for _, ct := range certs[1:] {
		inter.AddCert(ct)
	}
	// Roots が nil なら OS の信頼ストア（crypto/tls と同じ）
	_, err := certs[0].Verify(x509.VerifyOptions{Roots: c.opts.RootCAs, Intermediates: inter, DNSName: name, CurrentTime: c.opts.Now()})
	return err
}

// Close は、開いたままの接続（とそれを待つ goroutine）を閉じる。
func (c *Client) Close() {
	for _, byPolicy := range c.shared {
		for _, hc := range byPolicy {
			hc.CloseIdleConnections()
		}
	}
}

// handshake は、1 回の要求の中で TLS の握手が失敗したかを記録する。
type handshake struct {
	failed   atomic.Bool                // ⚠ トレースのコールバックは接続を開く goroutine から呼ばれうる
	gotBytes atomic.Bool                // 応答の最初のバイトが届いた（そのあとの失敗は「読めない」であって「届かない」ではない）
	addr     atomic.Pointer[netip.Addr] // 使った接続の、確かめた IP（checkedConn.addr）。確かめなかった接続なら nil
}

// exchange は 1 往復を始める。⚠ Do と Chain が共有する唯一の往復の実装。proxy は、その要求について決めたプロキシ（nil なら直接）。
func (c *Client) exchange(ctx context.Context, hc *http.Client, method, target string, proxy *url.URL, h *handshake) (*http.Response, error) {
	ctx = context.WithValue(ctx, proxyKey{}, proxyDecision{proxy: proxy})
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: building the request for %s: %w", target, err)
	}
	if c.opts.UserAgent != "" {
		req.Header.Set("User-Agent", c.opts.UserAgent)
	}
	// TLS の握手の中で失敗したかを記録する。crypto/tls は型の無い失敗も返すので、型ではなく段階で見分ける
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err != nil {
				h.failed.Store(true)
			}
		},
		GotFirstResponseByte: func() { h.gotBytes.Store(true) },
		GotConn: func(info httptrace.GotConnInfo) {
			conn := info.Conn
			if tc, ok := conn.(*tls.Conn); ok {
				conn = tc.NetConn()
			}
			if cc, ok := conn.(*checkedConn); ok {
				a := cc.addr
				h.addr.Store(&a)
			}
		},
	}))
	// ⚠ Client.Do ではなく Transport に直接渡す（リダイレクトは追わない。1 段ずつ見る）: Client.Do はリダイレクトを
	//   追わない設定でも Location を先に解釈し、壊れた Location（例: "http://[::1"）を要求の失敗にする。相手は答えているので、
	//   それは到達不能ではない（docs/cli-reference.md §6。redirects.bad-location はチェックが言う）。
	//   Client.Do がしていて Transport がしないこと: URL の userinfo（user:pass@）を Authorization ヘッダにする。
	//   tenken は資格情報を送らない（送るかどうかは docs/cli-reference.md が決めていない）。
	return hc.Transport.RoundTrip(req)
}

// Do は 1 往復する。使える応答が無ければ *UnreachableError、呼び出し側が取り消した・呼び出し側の期限が切れたなら ErrCanceled を返す。
func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	hc, proxy, err := c.clientFor(r)
	if err != nil {
		return Response{}, failure(parent, r.URL, err, false, false)
	}
	if !c.isShared(hc) {
		defer hc.CloseIdleConnections() // 1 回限りの client
	}
	var h handshake
	resp, err := c.exchange(ctx, hc, r.Method, r.URL, proxy, &h)
	if err != nil {
		return Response{}, failure(parent, r.URL, err, h.failed.Load(), h.gotBytes.Load())
	}
	defer resp.Body.Close()
	out := Response{URL: r.URL, Status: resp.StatusCode, Header: map[string][]string{}, ViaProxy: proxy != nil}
	if a := h.addr.Load(); a != nil {
		out.ConnectedAddr = *a
	}
	for k, v := range resp.Header {
		out.Header[textproto.CanonicalMIMEHeaderKey(k)] = append([]string(nil), v...)
	}
	if r.WantBody != nil && !r.WantBody(r.URL, out.Status, out.Header) {
		return out, nil // 本文は読まない（Close が接続を返すか閉じる）
	}
	// ⚠ 本文は上限つきで読む（.claude/rules/go.md: 入力は敵意がありうる）
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxBody+1))
	if err != nil {
		if f := failure(parent, r.URL, err, false, false); errors.Is(f, ErrCanceled) {
			return Response{}, f
		} else {
			// ステータスとヘッダは届いている。捨てずに返し、本文が欠けたことと理由を伝える
			var ue *UnreachableError
			if errors.As(f, &ue) {
				out.Incomplete, out.IncompleteReason = true, ue.Reason
			} else {
				out.Incomplete, out.IncompleteReason = true, ReasonConnect // 本文の読みは接続しないので、ここには来ない
			}
		}
	}
	if int64(len(body)) > c.opts.MaxBody {
		body, out.Truncated = body[:c.opts.MaxBody], true
	}
	out.Body = body
	return out, nil
}

func (c *Client) isShared(hc *http.Client) bool {
	for _, byPolicy := range c.shared {
		for _, s := range byPolicy {
			if s == hc {
				return true
			}
		}
	}
	return false
}

// clientFor は、要求がプロキシを通るかを決め（Options.Proxy を 1 度だけ呼ぶ。返すのはそのプロキシ、nil なら直接）、
// 要求に使う http.Client を選ぶ。https:// のプロキシを通るなら 1 回限りのものを作る（oneShot）。
// プライベートアドレスを確かめるのは、PrivateAllowed でなく、プロキシを通らない要求だけ。
func (c *Client) clientFor(r Request) (*http.Client, *url.URL, error) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch: %s: %w", r.URL, err)
	}
	proxy, err := c.opts.Proxy(u)
	if err != nil {
		return nil, nil, err
	}
	policy := verifyTarget
	if r.SkipVerify {
		policy = skipTarget
	}
	// ⚠ 相手が http:// でも、https:// のプロキシとの握手はある。検証を外す設定をプロキシに効かせない
	if proxy != nil && proxy.Scheme == "https" {
		return c.oneShot(u, proxy, policy, nil, netip.Addr{}), proxy, nil // https:// のプロキシを通る: 接続するのはプロキシ
	}
	v, p := verifiedIdx, r.Private
	if r.SkipVerify {
		v = unverifiedIdx
	}
	if p < 0 || p >= privatePolicies {
		p = PrivateDenied // 知らない値は確かめる側へ倒す（プライベートアドレスへ接続しない）
	}
	if proxy != nil {
		p = PrivateAllowed // プロキシを通る要求: 接続するのはプロキシ（プロキシ自身は確かめない）
	}
	return c.shared[v][p], proxy, nil
}

// Chain は target（https:// の URL）に TLS で接続して、相手が送った証明書の鎖を返す。⚠ 検証はしない（中身を読むため）。
// ⚠ HTTP の要求は送らない: 鎖を受け取った時点で握手を中断する。SNI は URL のホスト名。プロキシの設定に従う。
// 呼び出し側の期限が鎖を受け取ったあとに切れても、受け取った鎖は返す（相手は答え終わっている）。
// at が有効なら、名前を解決せずに at へ接続する（開始 URL の鎖が確かめて接続した IP。docs/cli-reference.md §2.1 の cert）。
func (c *Client) Chain(ctx context.Context, target string, at netip.Addr) (Chain, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		// ⚠ 呼び出し側の誤り。相手が到達不能なのではない
		return Chain{}, fmt.Errorf("fetch: Chain needs an https:// URL, got %q", target)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	var mu sync.Mutex
	var certs []*x509.Certificate
	proxy, err := c.opts.Proxy(u) // この要求がプロキシを通るかの決定（1 度だけ。Transport はこれに従う）
	if err != nil {
		return Chain{}, failure(parent, target, err, false, false)
	}
	hc := c.oneShot(u, proxy, captureTarget, func(cs []*x509.Certificate) { mu.Lock(); certs = cs; mu.Unlock() }, at)
	defer hc.CloseIdleConnections()
	var h handshake
	resp, err := c.exchange(ctx, hc, http.MethodGet, target, proxy, &h)
	if resp != nil {
		resp.Body.Close()
	}
	mu.Lock()
	got := certs
	mu.Unlock()
	if got != nil {
		return Chain{Certificates: got}, nil
	}
	if err == nil {
		err = errors.New("fetch: the handshake ended without a certificate")
	}
	return Chain{}, failure(parent, target, err, h.failed.Load(), h.gotBytes.Load())
}

// failure は、使える応答が無かったときの返り値を決める唯一の場所。
// ⚠ 呼び出し側の取り消しも、呼び出し側の期限切れも、相手のせいではない（CLAUDE.md §4-1）。どの段階でも同じに扱う。
func failure(parent context.Context, target string, err error, inHandshake, gotBytes bool) error {
	if parent.Err() != nil {
		return ErrCanceled
	}
	var pe *PrivateAddressError
	if errors.As(err, &pe) {
		return &PrivateAddressError{URL: target, Host: pe.Host} // 到達不能ではない（ErrPrivateAddress）
	}
	return &UnreachableError{URL: target, Reason: classify(err, inHandshake, gotBytes), Err: err}
}

// classify は、使える応答が無かった理由を決める。⚠ 文字列で比べない（.claude/rules/go.md）。
// inHandshake は、失敗が TLS の握手の中で起きたこと、gotBytes は応答の最初のバイトが届いていたこと（どちらも httptrace が知っている）。
// 届いたあとの失敗（時間切れでない）は malformed: net/http の「読めない」のエラー型は公開されていないので、段階で見分ける
func classify(err error, inHandshake, gotBytes bool) Reason {
	var dnsErr *net.DNSError
	var ne net.Error
	var recErr tls.RecordHeaderError
	var certErr *tls.CertificateVerificationError
	var alert tls.AlertError
	switch {
	case errors.As(err, &dnsErr):
		return ReasonDNS
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return ReasonTimeout
	case inHandshake, errors.As(err, &certErr), errors.As(err, &recErr), errors.As(err, &alert):
		return ReasonTLS
	case gotBytes:
		return ReasonMalformed
	}
	return ReasonConnect
}
