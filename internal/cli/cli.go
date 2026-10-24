// Package cli はフラグの解釈、出力、終了コードを持つ。
// 人が読む文と終了コードを決めるのはこのパッケージだけ（docs/design.md §2、CLAUDE.md §3）。
package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/hidetzu/tenken/internal/check/cert"
	"github.com/hidetzu/tenken/internal/check/headers"
	"github.com/hidetzu/tenken/internal/check/links"
	"github.com/hidetzu/tenken/internal/check/redirects"
	"github.com/hidetzu/tenken/internal/check/sitemap"
	"github.com/hidetzu/tenken/internal/crawl"
	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/finding"
)

// Version はリリースのときに -ldflags で上書きする。空なら Go がバイナリに残したモジュールのバージョンを使う
// （go install …@v0.1.0 は -ldflags を通らない。docs/cli-reference.md §3.6）。
var Version = ""

// devVersion は、リリースの版もモジュールの版も無いときの版（go build や go run、go test）。
const devVersion = "0.0.0-dev"

// version は --version と User-Agent に出す版。順は -ldflags の値、モジュールの版（先頭の v を外す）、devVersion。
func version() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(Version, info, ok)
}

// resolveVersion は version の順を決める。テストで BuildInfo を差し替えられるように分けてある。
func resolveVersion(release string, info *debug.BuildInfo, ok bool) string {
	if release != "" {
		return release
	}
	// "(devel)" は Go が版を知らないときの値（作業木の go build のうち、VCS の情報が無いもの）
	if ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return devVersion
}

// 終了コード。値と意味の正本は docs/cli-reference.md §4。
const (
	ExitOK            = 0
	ExitFindings      = 1
	ExitUsage         = 2
	ExitCouldNotCheck = 3
)

// toolName は公開名。User-Agent の既定値と --version の <名前>（docs/cli-reference.md §3.5・§3.6）。
const toolName = "tenken"

// repoURL は --user-agent の既定に入れるリポジトリ（docs/cli-reference.md §3.5）。
const repoURL = "https://github.com/hidetzu/tenken"

// deps は、テストで差し替えるもの。時計と、接続を開く fetch の作り方（docs/design.md §2: 時計を読むのは cli だけ）。
type deps struct {
	now        func() time.Time
	newFetcher func(fetch.Options) fetch.Fetcher
	proxy      func(*url.URL) (*url.URL, error) // 本番は環境変数（docs/cli-reference.md §3.6）
	systemPool func() (*x509.CertPool, error)
	sleep      func(time.Duration) // 429 の待ち
	getenv     func(string) string // プロキシの環境変数の名前を言うためと、信頼ストアの場所の環境変数を見るため。本番は os.Getenv
	goos       string              // 信頼ストアを OS の検証器が持つか（trustStore）。本番は runtime.GOOS
	// probeURL は、開始 URL が https のときに確かめに行く同じホストの http:// の URL（docs/cli-reference.md §2.1）。
	// 本番は http://<host>/（ポート 80）。テストは 80 番を開けないので差し替える
	probeURL func(target *url.URL) string
}

func realDeps() deps {
	return deps{
		now:        time.Now,
		newFetcher: func(o fetch.Options) fetch.Fetcher { return fetch.New(o) },
		proxy:      fetch.EnvProxy,
		systemPool: x509.SystemCertPool,
		sleep:      time.Sleep,
		getenv:     os.Getenv,
		goos:       runtime.GOOS,
		probeURL:   httpProbe,
	}
}

// httpProbe は同じホストの http:// の URL（ポートは既定の 80）。
func httpProbe(target *url.URL) string {
	host := target.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return (&url.URL{Scheme: "http", Host: host, Path: "/"}).String()
}

// Run は引数を解釈して点検し、終了コードを返す。
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, realDeps())
}

func run(args []string, stdout, stderr io.Writer, d deps) int {
	o, err := parse(args, fmt.Sprintf("%s/%s (+%s)", toolName, version(), repoURL))
	if err != nil {
		return usageExit(stderr, err)
	}
	if o.version {
		fmt.Fprintf(stdout, "%s %s\n", toolName, version())
		return ExitOK
	}
	if o.help {
		fmt.Fprint(stdout, helpText())
		return ExitOK
	}
	target, err := parseTarget(o.target)
	if err != nil {
		return usageExit(stderr, err)
	}
	roots, err := trustStore(o.caFile, d.systemPool, usesPlatformVerifier(d.goos, d.getenv))
	if err != nil {
		return usageExit(stderr, err)
	}

	started := d.now()
	r := newReport(o, started)
	for _, id := range finding.Checks {
		if !o.selected(id) {
			r.skip(id, "not-selected", fmt.Sprintf("%s was left out by --only or --skip", id))
		}
	}
	certSelected := o.selected("cert") && target.Scheme == "https"
	if o.selected("cert") && target.Scheme != "https" {
		r.skip("cert", "not-https", fmt.Sprintf("%s is not https, so there is no certificate to check", o.target))
	}
	if !certSelected && !o.selected("redirects") && !o.selected("links") && !o.selected("headers") && !o.selected("sitemap") {
		return r.finish(stdout, stderr, d.now())
	}
	// プロキシを通るかは d.proxy（本番は Go の http.ProxyFromEnvironment）だけが決める。⚠ 環境変数を自分で読み直して決めない:
	// NO_PROXY の一覧や捨てられる値の解釈が Go とずれる（CLAUDE.md §3）。実際に通した要求があれば、終わりに 1 行言う
	var proxied proxyUse
	proxy := func(u *url.URL) (*url.URL, error) {
		p, err := d.proxy(u)
		if err == nil && p != nil {
			proxied.add(u.Scheme)
		}
		return p, err
	}
	defer func() {
		if s := proxyNotice(proxied.schemes(), d.getenv); s != "" {
			fmt.Fprintln(stderr, s) // 標準出力（text / JSON）と終了コードは変えない
		}
	}()
	f := d.newFetcher(fetch.Options{Timeout: o.timeout, MaxBody: o.maxBody, UserAgent: o.userAgent, RootCAs: roots, Now: d.now, Proxy: proxy})
	defer closeIfCloser(f)
	// 実行全体の上限（docs/cli-reference.md §3.2）。開始ページと probe と robots.txt は数えるだけで止めない
	budget := crawl.NewBudget(started, o.maxDuration, o.maxRequests, d.now)
	// プライベートアドレスへの接続の例外は、利用者が書いた URL のホストだけ（リダイレクトの後のホストではない。docs/cli-reference.md §5.2 の private）。
	// ローカル（プライベートアドレスだけのホスト）かは、開始 URL への最初の接続で 1 度だけ決まる。開始 URL の鎖も http:// の probe も、
	// 別のホストへ飛べば、そこからはプライベートアドレスへ接続しない
	start := crawl.NewStartHost(target.String())
	// startPrivate は、probe がプライベートアドレスの行き先で止まって取りに行かなかった URL（not_fetched.private。巡回と sitemap が
	// 同じ URL を数えれば二度数えない。開始 URL の鎖が止まれば点検できなかった: 3）
	startPrivate := map[string]bool{}
	follow := crawl.FollowOptions{MaxRedirects: o.maxRedirs, SkipVerify: true, Now: d.now, Sleep: d.sleep, Budget: budget, BudgetExempt: true, StartHost: start}

	// 開始 URL の鎖（docs/cli-reference.md §4）。⚠ 証明書は検証せずに取り、証明書の問題は cert が 1 回だけ言う（§2.1）
	chain, err := crawl.Follow(context.Background(), f, target.String(), follow)
	if err != nil {
		r.couldNotCheck = unreachable(err, o.timeout)
		return r.finish(stdout, stderr, d.now())
	}
	if chain.Stop == crawl.StopRateLimited {
		last := chain.Hops[len(chain.Hops)-1]
		r.couldNotCheck = &couldNotCheck{Code: "rate-limited", Reason: links.RateLimitedSentence(last.URL, rateLimitOf(chain), crawl.MaxRetryWait) + "; it is not answering now, so nothing was checked. Try again later"}
		return r.finish(stdout, stderr, d.now())
	}
	if chain.Stop == crawl.StopPrivate {
		// 開始 URL の鎖が別のホストのプライベートアドレスへ飛んだ: 安全のための決まりで接続しない。点検するページが無いので、
		// 点検できなかった（3、private-address。docs/cli-reference.md §4）。取らなかった URL は not_fetched.private に数える
		// 止めた URL が開始 URL のホストなら、最初の接続で公開のアドレスだったホストが実行の途中でプライベートアドレスに解決された
		// （同じホストへのリダイレクト、429 のやり直し。DNS rebinding）。別のホストへのリダイレクトとは文を分ける
		// 開始 URL のホストへの最初の接続がプロキシを通っていれば、ローカルかを確かめていない: 文を分ける
		var reason string
		switch {
		case (start.Is(chain.PrivateURL) || len(chain.Hops) == 0) && start.FirstConnectionProxied():
			reason = startHostProxiedSentence(chain.PrivateURL)
		case start.Is(chain.PrivateURL) || len(chain.Hops) == 0:
			reason = startHostRebindSentence(chain.PrivateURL)
		default:
			reason = privateStopSentence(chain.Hops[len(chain.Hops)-1].URL, chain.PrivateURL)
		}
		r.Summary.NotFetched.Private = 1
		r.couldNotCheck = &couldNotCheck{Code: "private-address", Reason: reason}
		return r.finish(stdout, stderr, d.now())
	}
	rc := toRedirects(chain)
	if o.selected("redirects") {
		r.add(redirects.Check(rc, o.maxRedirs, finding.Start(o.target)))
		if target.Scheme == "http" {
			// http の開始 URL 自身が https に飛ばないのも no-https（docs/cli-reference.md §2.1）
			r.add(redirects.NoHTTPS(rc, o.target, finding.Start(o.target)))
		}
	}
	if o.selected("links") && chain.Stop == crawl.StopFinal {
		last := chain.Hops[len(chain.Hops)-1]
		r.add(links.Start(o.target, last.URL, last.Status, redirects.DetailChain(rc)))
	}
	if startPageBroken(chain) {
		// 巡回が要るチェックは開始ページより先へ進めない（docs/cli-reference.md §2.3 の start-page-broken）
		for _, id := range []string{"redirects", "links", "headers"} {
			if o.selected(id) {
				r.skip(id, "start-page-broken", fmt.Sprintf("the start page did not answer with a page to crawl from (%s), so %s did not go beyond it", startPageWhy(chain), id))
			}
		}
	}
	if certSelected {
		// 点検するのはリダイレクト後のホスト（docs/cli-reference.md §2.1）: 鎖の最後の https の URL。
		// 接続するのは、その往復が確かめて接続した IP（名前を解決し直さない: 同じ名前がプライベートアドレスに変わっても握手しに行かない）
		certURL, certAt := lastHTTPS(chain, target)
		fs, cnc := checkCert(f, certURL, certAt, roots, o, d.now)
		if cnc != nil {
			r.couldNotCheck = cnc
			return r.finish(stdout, stderr, d.now())
		}
		r.add(fs)
	}
	if o.selected("redirects") && target.Scheme == "https" {
		// 同じホストの http:// に 1 回だけ、https に飛ぶかだけを見る（docs/cli-reference.md §2.1。§5.2 の probe の行は no-https だけ）。
		// 使える応答が無ければ（届かない、やり直しても 429）、確かめられなかったと数える（skipped の probe-unavailable）:
		// 接続の拒否も「http で答えていない」ではなく「確かめられなかった」に入れる
		probe := d.probeURL(target)
		pc, err := crawl.Follow(context.Background(), f, probe, follow)
		if err == nil && pc.Stop == crawl.StopPrivate {
			startPrivate[pc.PrivateURL] = true
		}
		prc := toRedirects(pc)
		const what = "(checked by tenken to see whether http redirects to https)"
		switch {
		case err != nil && !redirects.ReachesHTTPS(prc):
			// 届かなかった URL はエラーが持つ: probe 自身か、probe が飛ばした先か（飛ばした先が https なら、飛んだことは観測できている）
			u := asUnreachable(err)
			where := probe + " " + what
			if u.URL != probe {
				where = fmt.Sprintf("%s %s redirected to %s, which", probe, what, u.URL)
			}
			r.skip("redirects", "probe-unavailable", fmt.Sprintf("%s %s; whether http redirects to https was left unverified", where, unreachableWhy(err, o.timeout)))
		case err != nil:
			// probe は https へ飛んでいた（その先に届かなくても、https に飛ぶことは観測した）: 指摘なし
		case pc.Stop == crawl.StopRateLimited:
			r.skip("redirects", "probe-unavailable", fmt.Sprintf("%s %s answered 429; whether http redirects to https was left unverified", probe, what))
		default:
			r.add(redirects.NoHTTPS(prc, probe, finding.Probe(probe)))
		}
	}
	// robots.txt は巡回のために、sitemap を skip しても読む（docs/cli-reference.md §2.1）。sitemap は開始ページが壊れていても走る（§4）
	startURL := crawl.Normalize(chain.Hops[len(chain.Hops)-1].URL)
	origin := crawl.Origin(startURL)
	needsCrawl := (o.selected("links") || o.selected("headers") || o.selected("redirects")) && !startPageBroken(chain)
	var rr *crawl.RobotsResult
	if needsCrawl || o.selected("sitemap") {
		rr = crawl.ReadRobots(context.Background(), f, origin, crawl.Options{Product: productToken(o.userAgent), Now: d.now, Sleep: d.sleep, Budget: budget, StartHost: start})
	}
	// 巡回と sitemap は 1 つの番を使う: 429 を受けて広げた間隔は、その実行の終わりまで効く（docs/cli-reference.md §3.2）
	gate := crawl.NewHostGate(rr.Delay(o.delay), d.now, d.sleep)
	known := map[string]crawl.Link{startURL: {URL: startURL, Chain: chain}}
	var crawlNotFetched map[string]bool // 巡回が not_fetched に数えた URL。sitemap は二度数えない
	if needsCrawl {
		// 巡回（docs/cli-reference.md §2.1 の links・headers・redirects、§3.1・§3.2 の上限）。参照先（サブリソース・外部）の生存確認は
		// links が選ばれているときだけ。上限の外のページ候補はどの回でも取ってみて数える（TEST-PERSPECTIVES §2.2）
		res := crawl.Crawl(context.Background(), f, crawl.Page{URL: startURL, Resp: *chain.Final}, crawl.Options{
			MaxDepth: o.maxDepth, MaxPages: o.maxPages, MaxRedirects: o.maxRedirs, Concurrency: o.concurrency, Delay: o.delay,
			Exclude: o.exclude, NoExternal: o.noExternal, Product: productToken(o.userAgent), Now: d.now, Sleep: d.sleep, Robots: rr,
			FetchReferences: o.selected("links"), MaxLinksPerPage: o.maxLinks, Gate: gate, Budget: budget, StartHost: start,
		})
		r.crawled(res)
		crawlNotFetched = res.NotFetchedURLs
		chain.Final.Body = nil // 開始ページの本文は巡回が参照を拾った。以後は要らない（sitemap の Known はステータスだけ見る）
		for _, l := range res.Links {
			known[l.URL] = l
		}
		if res.Robots == crawl.RobotsBroken || res.Robots == crawl.RobotsUnreachable || res.Robots == crawl.RobotsRateLimited || res.Robots == crawl.RobotsBadRedirect {
			// 巡回が要るチェック全部に（start-page-broken と同じ組）。外部リンクについて言えるのは links だけ（ほかは外部を見ていない）
			for _, id := range []string{"redirects", "links", "headers"} {
				if !o.selected(id) {
					continue
				}
				external := ""
				switch {
				case id != "links":
				case o.noExternal:
					external = " (external links were not checked either: --no-external)"
				default:
					external = " (external links were still checked)"
				}
				r.skip(id, "robots-unavailable", fmt.Sprintf("%s %s, so the crawl stopped at the start page (RFC 9309 treats an unavailable robots.txt as a complete disallow); %s fetched no URL on this origin beyond the start page%s", res.RobotsURL, robotsWhy(res, o.timeout), id, external))
			}
		}
		if o.selected("links") {
			r.add(links.Check(toLinks(res, origin), links.Options{Timeout: o.timeout, MaxRetryWait: crawl.MaxRetryWait}))
			r.add(links.MixedContent(toRefs(res)))
		}
		if o.selected("headers") {
			// 巡回した全ページ（HTML の応答）のヘッダ（docs/cli-reference.md §2.1 の headers、TEST-PERSPECTIVES §3.4）
			pages := make([]headers.Page, 0, len(res.Pages))
			for _, p := range res.Pages {
				pages = append(pages, headers.Page{URL: p.URL, Header: p.Resp.Header})
			}
			r.add(headers.Check(pages, headers.Options{HSTSMinAge: o.hstsMinAge}))
		}
		if o.selected("redirects") {
			// 巡回で出会ったリダイレクト（docs/cli-reference.md §2.1）: 見つかった場所はそれを参照していた要素。
			// links を外していれば参照先は取りに行かないが、巡回したページへの鎖はあるので、それについては言う
			for _, l := range res.Links {
				// リダイレクトがあった鎖: 2 段以上か、1 段でもリダイレクトで止まった（自分へのループ、壊れた Location）
				if l.Err == nil && chainRedirected(l.Chain) {
					fs := redirects.Check(toRedirects(l.Chain), o.maxRedirs, l.Locations...)
					for i := range fs {
						fs[i].Truncated = l.MoreLocations > 0 // 巡回が場所を捨てていれば、この鎖のどの指摘も数を言えない
					}
					r.add(fs)
				}
			}
		}
	}
	for u := range startPrivate {
		if !crawlNotFetched[u] {
			r.Summary.NotFetched.Private++ // 巡回が同じ URL を数えていなければ（URL ごとに 1。docs/cli-reference.md §5.2）
		}
	}
	if o.selected("sitemap") {
		// sitemap（docs/cli-reference.md §2.1）: 巡回で取った URL は二度取りに行かない
		sres := crawl.ReadSitemaps(context.Background(), f, origin, crawl.SitemapOptions{
			MaxURLs: o.maxSitemap, MaxRedirects: o.maxRedirs, Robots: rr, Exclude: o.exclude, Delay: o.delay, Now: d.now, Sleep: d.sleep,
			Known: func(u string) (crawl.Chain, error, bool) {
				l, ok := known[u]
				return l.Chain, l.Err, ok
			},
			Counted:   func(u string) bool { return crawlNotFetched[u] || startPrivate[u] },
			Gate:      gate,
			Budget:    budget,
			StartHost: start,
		})
		r.sitemapRead(sres)
		r.add(sitemap.Check(toSitemapInput(rr, sres, o.timeout)))
		if o.selected("redirects") {
			// sitemap から辿ったリダイレクト（docs/cli-reference.md §5.2 の found_on: sitemap）
			for _, e := range sres.Entries {
				if e.Err == nil && e.URL != "" && !e.External && !e.Disallowed && !e.Excluded && !e.OverLimits && chainRedirected(e.Chain) {
					r.add(redirects.Check(toRedirects(e.Chain), o.maxRedirs, finding.InSitemap(e.Sitemap, e.Loc)))
				}
			}
		}
	}
	r.budgetHit(budget.Hit())
	return r.finish(stdout, stderr, d.now())
}

// chainRedirected は、鎖にリダイレクトがあったか: 2 段以上か、1 段でもリダイレクトで止まった（自分へのループ、壊れた Location）。
func chainRedirected(c crawl.Chain) bool {
	return len(c.Hops) > 1 || c.Stop != crawl.StopFinal && c.Stop != crawl.StopRateLimited
}

// toSitemapInput は robots.txt と sitemap の読み取りをチェックの形にする（check は crawl を import しない。docs/design.md §2）。
func toSitemapInput(rr *crawl.RobotsResult, sres crawl.SitemapResult, timeout time.Duration) sitemap.Input {
	in := sitemap.Input{Declared: sres.Declared, Missing: sres.Missing, Timeout: timeout}
	if rr != nil {
		rb := &sitemap.Robots{State: string(rr.State), URL: rr.URL, Err: rr.Err}
		if rr.Resp != nil {
			rb.Status = rr.Resp.Status
		}
		if rr.State == crawl.RobotsRateLimited && rr.Chain.RetryAfterReadable {
			ra := int(rr.Chain.RetryAfterAsked / time.Second)
			rb.RetryAfter = &ra
		}
		if rr.State == crawl.RobotsBadRedirect {
			rb.BadStop, rb.FirstURL, rb.PrivateURL = string(rr.Stop), rr.FirstURL, rr.PrivateURL
		}
		in.Robots = rb
	}
	for _, f := range sres.Files {
		in.Files = append(in.Files, sitemap.File{URL: f.URL, Status: f.Status, Err: f.Err, Problem: f.Problem, Excluded: f.Excluded, RateLimited: f.RateLimited, RetryAfter: f.RetryAfter})
	}
	for _, e := range sres.Entries {
		if e.OverLimits {
			continue // --max-duration / --max-requests で確かめきらなかった: 指摘にしない（not_fetched.over_limits が言う）
		}
		se := sitemap.Entry{Loc: e.Loc, URL: e.URL, Sitemap: e.Sitemap, External: e.External, Disallowed: e.Disallowed, Excluded: e.Excluded, Err: e.Err}
		if e.Err == nil && e.URL != "" && !e.External && !e.Disallowed && !e.Excluded {
			se.Chain = redirects.DetailChain(toRedirects(e.Chain))
			se.Final = e.Chain.Stop == crawl.StopFinal
			if se.Final {
				se.Status = e.Chain.Final.Status
			}
			se.Redirected = chainRedirected(e.Chain) // プライベートアドレスへ飛ぶ鎖も「リダイレクトする」ことは観測した（docs/cli-reference.md §2.2 に例外は無い）
			if e.Chain.Stop == crawl.StopRateLimited {
				rl := rateLimitOf(e.Chain)
				se.RateLimit = &sitemap.RateLimit{Retried: rl.Retried, Waited: rl.Waited, RetryAfterRaw: rl.RetryAfterRaw, RetryAfterReadable: rl.RetryAfterReadable, RetryAfterAsked: rl.RetryAfterAsked}
			}
		}
		in.Entries = append(in.Entries, se)
	}
	return in
}

// productToken は User-Agent の製品名（"tenken/0.1.0 (+…)" の tenken。robots.txt のグループを選ぶ。RFC 9309 §2.2.1）。
func productToken(ua string) string {
	if i := strings.IndexAny(ua, "/ "); i > 0 {
		return ua[:i]
	}
	return ua
}

func robotsWhy(res crawl.Result, timeout time.Duration) string {
	switch res.Robots {
	case crawl.RobotsBroken:
		return fmt.Sprintf("answered %d", res.RobotsResp.Status)
	case crawl.RobotsRateLimited:
		return "answered 429 and was not usable after the retry"
	case crawl.RobotsBadRedirect:
		switch res.RobotsStop {
		case crawl.StopLoop:
			return "redirected in a loop"
		case crawl.StopBadLocation:
			return "redirected to a Location that is not a usable URL"
		case crawl.StopPrivate:
			return sitemap.PrivateRedirectSentence(res.RobotsPrivateURL) // 文は check/sitemap の 1 か所
		default:
			return fmt.Sprintf("redirected more than %d times (counting from %s)", crawl.MaxRobotsRedirects, res.RobotsFirstURL)
		}
	default:
		return unreachableWhy(res.RobotsErr, timeout)
	}
}

// toLinks は巡回の結果をチェックの形にする（check は crawl を import しない。docs/design.md §2）。
func toLinks(res crawl.Result, origin string) []links.Link {
	out := make([]links.Link, 0, len(res.Links))
	for _, l := range res.Links {
		cl := links.Link{URL: l.URL, External: l.External, Err: l.Err, Locations: l.Locations, MoreLocations: l.MoreLocations}
		if l.Err == nil && len(l.Chain.Hops) > 0 {
			// forbidden か broken かは、401 / 403 を返した最後の応答のオリジンで決める（docs/cli-reference.md §2.2）
			cl.External = crawl.Origin(crawl.Normalize(l.Chain.Hops[len(l.Chain.Hops)-1].URL)) != origin
		}
		if l.Err == nil {
			cl.Chain = redirects.DetailChain(toRedirects(l.Chain))
			cl.Final = l.Chain.Stop == crawl.StopFinal
			if cl.Final {
				cl.Status = l.Chain.Final.Status
			}
			if l.Chain.Stop == crawl.StopRateLimited {
				rl := rateLimitOf(l.Chain)
				cl.RateLimit = &rl
			}
		}
		out = append(out, cl)
	}
	return out
}

func toRefs(res crawl.Result) []links.PageRef {
	var out []links.PageRef
	for _, p := range res.Pages {
		for _, ref := range p.Refs {
			if ref.URL != "" {
				out = append(out, links.PageRef{Page: p.URL, URL: ref.URL, Element: ref.Element, Attribute: ref.Attribute, Value: ref.Value, Subresource: ref.Subresource})
			}
		}
	}
	return out
}

// rateLimitOf は crawl の鎖の 429 の記録をチェックの形にする。
func rateLimitOf(c crawl.Chain) links.RateLimit {
	return links.RateLimit{Retried: c.Retried, Waited: c.Waited, RetryAfterRaw: c.RetryAfterRaw, RetryAfterReadable: c.RetryAfterReadable, RetryAfterAsked: c.RetryAfterAsked}
}

// toRedirects は crawl の鎖をチェックの形にする（check は crawl を import しない。docs/design.md §2）。
func toRedirects(c crawl.Chain) redirects.Chain {
	out := redirects.Chain{Stop: redirects.Stop(c.Stop)}
	for _, h := range c.Hops {
		out.Hops = append(out.Hops, redirects.Hop{URL: h.URL, Status: h.Status, Location: h.Location, Next: h.Next})
	}
	return out
}

// startPageBroken は、開始 URL（またはそのリダイレクト先）が 4xx / 5xx、ループ、壊れた Location、段数の超過で、
// 巡回の起点になるページが無いこと（docs/cli-reference.md §4）。⚠ プライベートアドレスで止まった鎖はここへ来ない（3）。
func startPageBroken(c crawl.Chain) bool {
	if c.Stop != crawl.StopFinal {
		return true
	}
	return c.Final.Status >= 400 && c.Final.Status != 429
}

func startPageWhy(c crawl.Chain) string {
	switch c.Stop {
	case crawl.StopLoop:
		return "its redirects loop"
	case crawl.StopBadLocation:
		return "a redirect has no usable Location"
	case crawl.StopTooMany:
		return "it redirects too many times"
	}
	return fmt.Sprintf("it answered %d", c.Final.Status)
}

// privateStopSentence は、開始 URL の鎖が from から別のホストのプライベートアドレス target へ飛んだので点検できなかったことの
// 1 文（could_not_check.reason。docs/cli-reference.md §4 の private-address）。止めた URL と、安全のための決まりで
// 取らなかったことを言う。プライベートアドレスへのリダイレクトの部分は check/sitemap の 1 か所の文。
func privateStopSentence(from, target string) string {
	return fmt.Sprintf("%s %s: tenken's safety policy lets only the start URL's host be a private address, so nothing was checked; to check that site, give its URL as the start URL", from, sitemap.PrivateRedirectSentence(target))
}

// startHostProxiedSentence は、開始 URL への最初の接続がプロキシを通ったので開始 URL のホストがローカルかを確かめておらず、
// そのホストのあとの URL がプロキシを通らずにプライベートアドレスに解決されたので target へ接続しなかったことの 1 文
// （could_not_check.reason。docs/cli-reference.md §4 の private-address）。プロキシの使い方をそろえるよう言う。
func startHostProxiedSentence(target string) string {
	return fmt.Sprintf("%s resolved to a private address, which tenken's safety policy does not connect to: the first connection to the start URL went through a proxy, so tenken did not check whether the start URL's host is local, and nothing was checked; use the proxy, or not, the same way for every URL of that host (HTTP_PROXY / HTTPS_PROXY / NO_PROXY)", target)
}

// startHostRebindSentence は、最初の接続で公開のアドレスだった開始 URL のホストが、実行の途中でプライベートアドレスに解決されたので
// target へ接続しなかったことの 1 文（could_not_check.reason。docs/cli-reference.md §4 の private-address）。
// 別のホストの文（privateStopSentence）の「開始 URL として渡す」は、すでに開始 URL なので言わない: 名前の解決が変わった理由を確かめる。
func startHostRebindSentence(target string) string {
	return fmt.Sprintf("%s resolved to a private address during the run; tenken's safety policy does not connect to a start URL host whose first connection found a public address, so nothing was checked; check why the host name's resolution changed", target)
}

// lastHTTPS は鎖の最後の https の URL（開始 URL が https なら少なくともそれ）と、その往復が確かめて接続した IP。
// ⚠ IP が無効な値なら（プロキシを通った、確かめずに接続したローカルな開始 URL のホスト、鎖に https の往復が無い）、
// cert は今までどおり名前で接続する: プロキシを通るなら接続するのはプロキシで、何も確かめない（docs/cli-reference.md §3.6）。
// ローカルな開始 URL のホストは、プライベートアドレスへの接続がもともと許されている
func lastHTTPS(c crawl.Chain, start *url.URL) (*url.URL, netip.Addr) {
	for i := len(c.Hops) - 1; i >= 0; i-- {
		if u, err := url.Parse(c.Hops[i].URL); err == nil && u.Scheme == "https" {
			return u, c.Hops[i].ConnectedAddr
		}
	}
	return start, netip.Addr{}
}

func usageExit(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "%s: %v\n", toolName, err)
	return ExitUsage
}

// parseTarget は <URL> の形を見る（docs/cli-reference.md §1: http か https、スキームが無ければ使い方の誤り、補わない）。
func parseTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil, usage("%q is not a URL with a scheme; write it as https://host/ (the scheme is not guessed)", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, usage("%q: only http:// and https:// URLs can be checked", raw)
	}
	if u.Hostname() == "" {
		return nil, usage("%q has no host", raw)
	}
	return u, nil
}

// usesPlatformVerifier は、x509.SystemCertPool が OS の検証器を使うか（そのときは空かどうかがここでは分からない）。
// darwin / windows でも、SSL_CERT_FILE か SSL_CERT_DIR が設定されていれば、Go はそこからファイルのルートを読む
// （GODEBUG x509sslcertoverrideplatform、Go 1.27 の既定は 1。go.mod の go の行で決まる）。そのときは空かどうかが分かる。
func usesPlatformVerifier(goos string, getenv func(string) string) bool {
	if goos != "darwin" && goos != "windows" {
		return false
	}
	return getenv("SSL_CERT_FILE") == "" && getenv("SSL_CERT_DIR") == ""
}

// trustStore は OS の信頼ストアに --ca-file を加える（docs/cli-reference.md §3.1: 置き換えない）。
// ⚠ 信頼ストアが空なら始めない: すべての証明書が「信頼できない」になり、壊れていないサイトを壊れていると言うことになる
//
//	（CLAUDE.md §1）。OS の検証器を使うとき（platformVerifier）は、空かどうかはここでは分からない。
func trustStore(caFile string, systemPool func() (*x509.CertPool, error), platformVerifier bool) (*x509.CertPool, error) {
	pool, err := systemPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !platformVerifier && len(pool.Subjects()) == 0 && caFile == "" { //nolint:staticcheck // Subjects は OS のファイルから読んだルートを数える唯一の手段
		return nil, usage("no CA certificates could be loaded from the OS trust store, so no certificate could be trusted; install the system CA bundle or pass --ca-file")
	}
	if caFile == "" {
		return pool, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		// ⚠ OS のエラー文は人に見せない（CLAUDE.md §4）
		if errors.Is(err, fs.ErrNotExist) {
			return nil, usage("--ca-file %s: the file does not exist", caFile)
		}
		return nil, usage("--ca-file %s: the file could not be read; check that it is a readable file", caFile)
	}
	pool = pool.Clone()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, usage("--ca-file %s: no PEM certificate was found in the file", caFile)
	}
	return pool, nil
}

func closeIfCloser(f fetch.Fetcher) {
	if c, ok := f.(interface{ Close() }); ok {
		c.Close()
	}
}

// checkCert は target（開始 URL のリダイレクト後の、鎖の最後の https の URL）のホストと TLS で接続し、証明書の指摘を返す
// （docs/cli-reference.md §2.1: 巡回しない）。届かなければ could_not_check（§4 の 3）。
func checkCert(f fetch.Fetcher, target *url.URL, at netip.Addr, roots *x509.CertPool, o *options, now func() time.Time) ([]finding.Finding, *couldNotCheck) {
	hostport := hostPort(target)
	chain, err := f.Chain(context.Background(), target.String(), at)
	if err != nil {
		return nil, unreachable(err, o.timeout)
	}
	fs, err := cert.Check(hostport, chain.Certificates, cert.Options{Roots: roots, Now: now, Warn: o.certWarn, Fail: o.certFail})
	if err != nil {
		// 鎖が空: TLS では起きないはずだが、起きたら「証明書を読むところまで行けなかった」（§4 の 3、tls）
		return nil, &couldNotCheck{Code: string(fetch.ReasonTLS), Reason: fmt.Sprintf("%s sent no certificate in the TLS handshake", hostport)}
	}
	return fs, nil
}

// hostPort は subject の host:port（docs/cli-reference.md §5.2）。https の既定のポートは 443。
// IPv6 は [...]:port（RFC 3986 §3.2.2 の形）。
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return host + ":" + port
}

// unreachable は fetch の到達不能を、人が読む 1 文と理由コードにする（docs/cli-reference.md §4、§6。CLAUDE.md §4-1）。
// 届かなかった URL はエラーが持つ（リダイレクト先で届かなかったなら、その URL）。
func unreachable(err error, timeout time.Duration) *couldNotCheck {
	u := asUnreachable(err)
	return &couldNotCheck{Code: string(u.Reason), Reason: fmt.Sprintf("%s %s", u.URL, unreachableWhy(err, timeout))}
}

func asUnreachable(err error) *fetch.UnreachableError {
	var u *fetch.UnreachableError
	if !errors.As(err, &u) {
		// fetch は到達不能か取り消ししか返さず、取り消しはしない。ここに来たら実装の誤りなので、隠さず止まる
		panic("cli: unexpected error from fetch: " + err.Error())
	}
	return u
}

// unreachableWhy は「届かなかった」の 1 文（URL の後ろに続く形）。文は links.UnreachableSentence が持つ。
func unreachableWhy(err error, timeout time.Duration) string {
	return links.UnreachableSentence(asUnreachable(err).Reason, timeout)
}
