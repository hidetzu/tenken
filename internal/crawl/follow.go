// Package crawl は「何を取りに行くか」を決める（docs/design.md §2）。取りに行くのは fetch、何を言うかはチェック。
// 開始 URL のリダイレクトの追跡（Follow）と、同じオリジンの巡回（Crawl）。
package crawl

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
)

// Hop はリダイレクトの鎖の 1 往復。
type Hop struct {
	URL      string
	Status   int
	Location string // Location ヘッダの値そのまま（無ければ ""）
	// Next は Location を URL に対して解決したもの（RFC 9110 §10.2.2）。解決できなければ ""
	Next string
	// ConnectedAddr は、この往復で確かめて接続した IP（fetch.Response.ConnectedAddr。確かめなかった接続なら無効な値）。
	// cli は cert の TLS の接続をこの IP へ向ける（名前を解決し直さない）
	ConnectedAddr netip.Addr
}

// Stop は、鎖がなぜそこで終わったか。
type Stop string

const (
	StopFinal       Stop = "final"        // リダイレクトでない応答に着いた（Final に入っている）
	StopLoop        Stop = "loop"         // Next がすでに要求した URL
	StopBadLocation Stop = "bad-location" // リダイレクトなのに Location が無い、または URL として解決できない
	StopTooMany     Stop = "too-many"     // 段数が MaxRedirects を超えた（最後の Hop もリダイレクト）
	StopRateLimited Stop = "rate-limited" // やり直しても 429（最後の Hop がそれ）
	StopPrivate     Stop = "private"      // Next がプライベートアドレス（取りに行かない。docs/cli-reference.md §5.2 の private）
	// StopOverLimits は、--max-duration か --max-requests に当たって次の要求を出さなかった（docs/cli-reference.md §3.2）。
	// ⚠ 鎖は観測しきっていない: 途中まで見えた Hops も指摘にしない（not_fetched.over_limits に数える。TEST-PERSPECTIVES §2.2）
	StopOverLimits Stop = "over-limits"
)

// Stops は Stop の値すべて（check/redirects の写しと照らすテストのため。値を足したらここにも）。
func Stops() []Stop {
	return []Stop{StopFinal, StopLoop, StopBadLocation, StopTooMany, StopRateLimited, StopPrivate, StopOverLimits}
}

// Chain は開始 URL から辿った鎖。
type Chain struct {
	Hops  []Hop
	Stop  Stop
	Final *fetch.Response // Stop が StopFinal のときだけ
	// PrivateURL は、Stop が StopPrivate のとき取りに行かなかった URL（リダイレクトの行き先か、名前を解決したらプライベート
	// アドレスだった URL。鎖の最初の URL のこともあり、そのとき Hops は空）。not_fetched.private はこの URL で数える
	PrivateURL string
	// 429 を受けたときの記録（文はこれを見て、観測したことだけを言う。evidence.md）:
	//   Retried は待ってからやり直したか。Waited は 429 を受けてからやり直すまでに実際に経った時間（やり直していなければ 0）。
	//   RetryAfterRaw は相手が送った Retry-After の値そのまま（無ければ ""）。RetryAfterReadable はそれを秒か日付として読めたか。
	// ⚠ Stop が StopRateLimited でも Retried でないことがある（求められた待ちが MaxRetryWait より長く、やり直さずに諦めた）
	Retried            bool
	Waited             time.Duration
	RetryAfterRaw      string
	RetryAfterReadable bool
	RetryAfterAsked    time.Duration // 読めたときに求められた待ち（上限より長くてもそのまま）
}

// FollowOptions は Follow の設定。
type FollowOptions struct {
	MaxRedirects int
	// SkipVerify は証明書を検証せずに取る（開始 URL の鎖と、同じホストの http:// から辿る鎖。証明書の問題は cert が 1 回だけ言う。docs/cli-reference.md §2.1）
	SkipVerify bool
	// SkipVerifyOrigin が空でなければ、SkipVerify はそのオリジンの URL にだけ効く（巡回: 鎖が外へ出ればそこからは検証する）
	SkipVerifyOrigin string
	// Now と Sleep は 429 の待ちと、同じホストへの間隔に使う。⚠ 時計は引数で受け取る（docs/design.md §2 § Walls）
	Now   func() time.Time
	Sleep func(time.Duration)
	// Method は GET（既定）か HEAD。リンクの生存確認は HEAD で足りることが多い（TEST-PERSPECTIVES §3.1）
	Method string
	// Gate は同じホストへの要求の間隔（nil なら待たない）
	Gate *HostGate
	// WantBody は、本文を読むか（fetch.Request.WantBody。nil なら読む）。鎖のどの応答にも渡す（3xx の本文も読まなくてよい）
	WantBody func(url string, status int, header map[string][]string) bool
	// StartHost は、プライベートアドレスへの接続の例外（開始 URL のホスト。docs/cli-reference.md §5.2 の private）。
	// リダイレクトの行き先がプライベートアドレスの直書きか localhost なら取りに行かず、名前を解決した結果がプライベートアドレスなら接続せず、
	// どちらも StopPrivate で止める（StartHost.Private、fetch.Request.Private）。nil なら例外は無い
	StartHost *StartHost
	// Budget は実行全体の上限（nil なら無い）。当たれば次の要求を出さず StopOverLimits で止める。
	// BudgetExempt なら数えるだけで止めない（開始ページ・probe・robots.txt。docs/cli-reference.md §3.2）
	Budget       *Budget
	BudgetExempt bool
}

// 429 の待ち（docs/test-perspectives.md §3.1 がこの 2 つの値を書く。TestRetryWaitsAreTheDocumentedValues が留める）:
// Retry-After に従い、無ければ DefaultRetryWait。MaxRetryWait を超えれば待たない
// （CI を何分も止めない。待ちきれなかった 429 は「やり直しても 429」と同じに扱う）。cli は文にこの値を使う。
const (
	DefaultRetryWait = time.Second
	MaxRetryWait     = 30 * time.Second
)

// Follow は start を取り（既定は GET）、リダイレクトを MaxRedirects 段まで追う。
// ⚠ 届かなければ fetch のエラー（*fetch.UnreachableError）をそのまま返す: 鎖のどこで届かなかったかは URL に入っている。
func Follow(ctx context.Context, f fetch.Fetcher, start string, o FollowOptions) (Chain, error) {
	var c Chain
	seen := map[string]bool{}
	target := start
	for {
		seen[target] = true
		resp, rl, err := getWithRetry(ctx, f, target, o)
		if errors.Is(err, errOverLimits) {
			c.Stop = StopOverLimits
			return c, nil
		}
		if errors.Is(err, fetch.ErrPrivateAddress) {
			// 名前を解決したらプライベートアドレスだった: 接続していない（到達不能ではない。docs/cli-reference.md §5.2 の private）
			c.Stop, c.PrivateURL = StopPrivate, target
			return c, nil
		}
		if err != nil {
			return c, err
		}
		if rl != nil {
			c.Retried, c.Waited, c.RetryAfterRaw, c.RetryAfterReadable, c.RetryAfterAsked = rl.retried, rl.waited, rl.raw, rl.readable, rl.asked
		}
		hop := Hop{URL: target, Status: resp.Status, Location: resp.Location(), ConnectedAddr: resp.ConnectedAddr}
		if resp.Status == 429 {
			c.Hops = append(c.Hops, hop)
			c.Stop = StopRateLimited
			return c, nil
		}
		if !isRedirect(resp.Status) {
			c.Hops = append(c.Hops, hop)
			c.Stop, c.Final = StopFinal, &resp
			return c, nil
		}
		hop.Next = resolve(target, hop.Location)
		c.Hops = append(c.Hops, hop)
		switch {
		case hop.Next == "":
			c.Stop = StopBadLocation
			return c, nil
		case seen[hop.Next]:
			c.Stop = StopLoop
			return c, nil
		case len(c.Hops) > o.MaxRedirects:
			// MaxRedirects 段は追い、その先もリダイレクトなら超えた（docs/cli-reference.md §3.2: 既定 3 なら 4 段目で警告）
			c.Stop = StopTooMany
			return c, nil
		case o.StartHost.Private(hop.Next):
			// CI の機械の中へは追わない
			c.Stop, c.PrivateURL = StopPrivate, hop.Next
			return c, nil
		}
		target = hop.Next
	}
}

// StartHost は、開始 URL のホスト（利用者が書いた URL のホスト。リダイレクトの後ではない）と、それがローカル（プライベートアドレス
// だけのホスト）か。プライベートアドレスへの接続の例外はこのホストだけ（docs/cli-reference.md §5.2 の private）。
// ホスト名は大文字と小文字を区別せず、末尾の点を除き、IPv6 は角括弧を外して比べる。ポートは含めない。
// ⚠ ローカルかは 1 度だけ、最初の接続の 1 回の解決で決め（fetch.PrivateIfOnlyPrivate）、その実行の終わりまで変えない:
//
//	最初にプライベートでないアドレスへ接続したホストが、あとで同じ名前のままプライベートアドレスだけに解決されても（DNS rebinding）、
//	そこへは接続しない。ホスト名が同じというだけではプライベートアドレスへの接続を許さない。
type StartHost struct {
	name string
	mu   sync.Mutex
	// decided は、ローカルかが決まったこと。local はその答え。proxied は、決めた最初の応答がプロキシを通っていた
	// （接続した先を確かめていない: ローカルではないとした）こと。cli が文を選ぶ（docs/cli-reference.md §4 の private-address）
	decided, local, proxied bool
}

// NewStartHost は、利用者が書いた URL の StartHost を作る。IP の直書きのプライベートアドレスと localhost は、解決し直しても
// 変わらないので、接続する前からローカル。
func NewStartHost(rawURL string) *StartHost {
	s := &StartHost{name: hostKey(hostnameOf(rawURL))}
	if fetch.IsPrivateHost(s.name) {
		s.decided, s.local = true, true
	}
	return s
}

func hostKey(h string) string { return strings.ToLower(strings.TrimSuffix(h, ".")) }

// Is は、URL のホストが開始 URL のホストか。
func (s *StartHost) Is(u string) bool { return s != nil && hostKey(hostnameOf(u)) == s.name }

// Local は、開始 URL のホストがローカル（プライベートアドレスだけのホスト）と決まったか。⚠ まだ決まっていなければ false。
func (s *StartHost) Local() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.local
}

// FirstConnectionProxied は、ローカルかを決めた最初の応答がプロキシを通っていたか（確かめていないのでローカルではない）。
// false なら、最初の接続は確かめたアドレスへ行った（またはまだ決まっていない、または IP の直書きか localhost）。
func (s *StartHost) FirstConnectionProxied() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxied
}

// Private は、取りに行く前に止める URL か: ホストがプライベートアドレスの直書きか localhost で、ローカルな開始 URL のホストでない
// （docs/cli-reference.md §5.2 の private）。名前を解決した結果は、接続の直前に fetch が確かめる（policy）。
func (s *StartHost) Private(u string) bool {
	return fetch.IsPrivateHost(hostnameOf(u)) && !(s.Is(u) && s.Local())
}

// policy は、URL への要求がプライベートアドレスへ接続してよいか（fetch.Request.Private）。開始 URL のホストだけが例外で、
// まだ決まっていなければ最初の接続で決める。
func (s *StartHost) policy(u string) fetch.PrivatePolicy {
	if !s.Is(u) {
		return fetch.PrivateDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.decided:
		return fetch.PrivateIfOnlyPrivate
	case s.local:
		return fetch.PrivateAllowed
	}
	return fetch.PrivateDenied
}

// observe は、開始 URL のホストへの最初の応答から、ローカルかを決める（接続した先が、確かめたプライベートアドレスだったか）。
// ⚠ プロキシを通った応答は確かめていないので、ローカルではない。
func (s *StartHost) observe(u string, resp fetch.Response) {
	if !s.Is(u) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.decided {
		// プロキシを通ったかは fetch が要求を送るときに決めた値（ViaProxy）で読む。ConnectedAddr が無いことから推し量らない
		s.decided, s.local, s.proxied = true, resp.ConnectedPrivate(), resp.ViaProxy
	}
}

// isRedirect は、Location に従うべきステータスか（RFC 9110 §15.4）。300 と 304 は従わない。
func isRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

// resolve は Location を base に対して解決する。空、解釈できない、http(s) でない、ホストが無い、なら ""。
func resolve(base, location string) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	l, err := url.Parse(location)
	if err != nil {
		return ""
	}
	u := b.ResolveReference(l)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	u.Fragment = "" // フラグメントは要求に乗らない
	return u.String()
}

// rateLimit は 429 を受けたときの記録。
type rateLimit struct {
	raw      string        // Retry-After の値そのまま（無ければ ""）
	readable bool          // 秒か日付として読めたか
	asked    time.Duration // 読めたときに求められた待ち
	retried  bool
	waited   time.Duration
}

// getWithRetry は GET し、429 なら Retry-After（無ければ既定）だけ待って 1 回だけやり直す。429 でなければ rl は nil。
func getWithRetry(ctx context.Context, f fetch.Fetcher, target string, o FollowOptions) (fetch.Response, *rateLimit, error) {
	method := o.Method
	if method == "" {
		method = "GET"
	}
	skip := o.SkipVerify && (o.SkipVerifyOrigin == "" || Origin(Normalize(target)) == o.SkipVerifyOrigin)
	// ⚠ Private は試すたびに決め直す（やり直しも）。開始 URL のホストがローカルかは、最初に届いた応答（429 も）で決まる:
	//   やり直しが同じ PrivateIfOnlyPrivate で解決し直すと、2 度目の答えで決めてしまう（DNS rebinding）
	req := fetch.Request{Method: method, URL: target, SkipVerify: skip, WantBody: o.WantBody, Private: o.StartHost.policy(target)}
	over := func() bool { return o.Budget.over(o.BudgetExempt) }
	turn, ok := o.Gate.acquireUnless(hostOf(target), false, over)
	if !ok {
		return fetch.Response{}, nil, errOverLimits
	}
	if !o.Budget.take(o.BudgetExempt) {
		turn.release()
		return fetch.Response{}, nil, errOverLimits
	}
	resp, err := f.Do(ctx, req)
	if err == nil {
		o.StartHost.observe(target, resp)
	}
	if err != nil || resp.Status != 429 {
		turn.release()
		return resp, nil, err
	}
	rl := retryAfter(resp, o.Now)
	wait := DefaultRetryWait
	if rl.readable {
		wait = rl.asked
	}
	// 429 を受けたホストへは、その実行の終わりまで間隔を広げる（やり直しが通っても）。番を手放す前に
	asked := time.Duration(0)
	if rl.readable {
		asked = rl.asked
	}
	turn.backoff(asked)
	turn.release()
	if wait > MaxRetryWait {
		return resp, rl, nil // 待ちきれない: やり直しても 429 と同じに扱う
	}
	if o.Budget.over(o.BudgetExempt) {
		// やり直す前に上限に当たっている: 待たない（やり直しを出さないので「やり直しても 429」とは言えない）
		return fetch.Response{}, nil, errOverLimits
	}
	got429 := o.Now()
	o.Sleep(wait)
	turn, ok = o.Gate.acquireUnless(hostOf(target), true, over)
	if !ok {
		return fetch.Response{}, nil, errOverLimits
	}
	if !o.Budget.take(o.BudgetExempt) {
		// やり直しを出さなかった: 「429 even after a retry」とは言えない（TEST-PERSPECTIVES §2.2）
		turn.release()
		return fetch.Response{}, nil, errOverLimits
	}
	// 待った時間は、429 を受けてからやり直すまでに実際に経った時間（文が言う。evidence.md）。Retry-After の分のほか、
	// 同じホストを待っていた要求が先に番を取って広げた間隔を寝ていれば、その分も入る。時計が進まない偽物でも wait を下回らない
	// （下でミリ秒に丸めるので、Retry-After の日付から求めた wait の ms 未満の端数の分だけは下回りうる）
	waited := o.Now().Sub(got429)
	if waited < wait {
		waited = wait
	}
	waited = waited.Round(time.Millisecond) // 文に出す: ns の端数を言わない（Retry-After の日付からの wait も）
	rl.retried, rl.waited = true, waited
	req.Private = o.StartHost.policy(target) // 最初の応答で決まったあと: ローカルなら PrivateAllowed、そうでなければ PrivateDenied
	resp, err = f.Do(ctx, req)
	if err == nil {
		o.StartHost.observe(target, resp)
	}
	turn.release()
	return resp, rl, err
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

// retryAfter は Retry-After（秒か HTTP の日付。RFC 9110 §10.2.3、§5.6.7）を読む。
// ⚠ 外からの数なので、掛け算の前に上限と比べる（溢れて負になると、待たずにやり直してしまう）。
//
//	読めた値は上限より大きくてもそのまま持つ（文はそれを言う）。int64 に収まらない数は「読めない」。
func retryAfter(resp fetch.Response, now func() time.Time) *rateLimit {
	v := resp.Header["Retry-After"]
	if len(v) == 0 {
		return &rateLimit{}
	}
	s := strings.TrimSpace(v[0])
	rl := &rateLimit{raw: s}
	if digits(s) {
		secs, err := strconv.ParseInt(s, 10, 64)
		if err != nil || secs > int64(maxRepresentableSeconds) {
			return rl // 大きすぎて時間として持てない: 読めないのと同じ
		}
		rl.readable, rl.asked = true, time.Duration(secs)*time.Second
		return rl
	}
	// HTTP の日付は 3 つの形を受け取る（RFC 9110 §5.6.7: IMF-fixdate のほか、古い RFC 850 と asctime の形も受け取らなければならない）
	for _, layout := range []string{time.RFC1123, time.RFC850, time.ANSIC} {
		if t, err := time.Parse(layout, s); err == nil {
			d := t.Sub(now())
			if d < 0 {
				d = 0
			}
			rl.readable, rl.asked = true, d
			return rl
		}
	}
	return rl
}

// maxRepresentableSeconds は time.Duration に収まる秒数（約 292 年）。
const maxRepresentableSeconds = int64(^uint64(0)>>1) / int64(time.Second)

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
