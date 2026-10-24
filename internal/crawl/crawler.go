package crawl

import (
	"context"
	"mime"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/finding"
)

// Page は巡回した HTML ページ（docs/cli-reference.md §5.2 の pages_crawled の 1 つ）。
type Page struct {
	URL   string // 正規化した URL（リダイレクト後）
	Depth int
	Resp  fetch.Response
	Refs  []Ref // 本文から拾った参照。本文が --max-body で切れていれば空（Truncated）
	// Truncated は、本文が --max-body を超えたのでリンクを拾わなかったこと（not_fetched.over_max_body）
	Truncated bool
}

// Link は、ページから参照されていて取りに行った URL の結果（ページ自身も含む）。
type Link struct {
	URL       string // 要求した URL（正規化）
	External  bool   // 開始 URL と別のオリジン
	Chain     Chain  // 辿った鎖（Err が nil のとき）。Final が最後の応答
	Err       error  // *fetch.UnreachableError（届かなかった）
	Locations []finding.Location
	// MoreLocations は、MaxLocationsPerURL を超えて覚えなかった場所の数
	MoreLocations int
	// Subresource は、参照が src / srcset / link[href] だけだったか（a[href] からも参照されていれば false）
	Subresource bool
	// HTTPSPage は、https のページから参照されていたか（Mixed Content の判定に使う）
	HTTPSPage bool
}

// NotFetched は取りに行かなかった URL の数（docs/cli-reference.md §5.2 の summary.not_fetched）。
type NotFetched struct {
	Excluded          int
	External          int
	RobotsDisallowed  int
	RobotsUnavailable int
	OverMaxBody       int
	OverLimits        int
	// Private は、プライベートアドレスか localhost への参照（ローカルな開始 URL のホストなら取る: StartHost。docs/cli-reference.md §5.2）
	Private int
}

// RobotsState は robots.txt をどう読めたか。
type RobotsState string

const (
	RobotsOK          RobotsState = "ok"          // 2xx で読めた（空でもよい）
	RobotsMissing     RobotsState = "missing"     // 4xx（429 を除く）: 制限なし（RFC 9309 unavailable）
	RobotsBroken      RobotsState = "broken"      // 5xx: すべて禁止
	RobotsUnreachable RobotsState = "unreachable" // 届かない: すべて禁止
	RobotsRateLimited RobotsState = "rate-limited"
	RobotsBadRedirect RobotsState = "bad-redirect" // リダイレクトがループした・Location が壊れていた・MaxRobotsRedirects を超えた: 届かないと同じ（すべて禁止）
	RobotsNotRead     RobotsState = "not-read"     // 巡回しなかったので読んでいない
)

// MaxRobotsRedirects は robots.txt のリダイレクトを追う回数（RFC 9309 §2.3.1.2 の 5）。
const MaxRobotsRedirects = 5

// MaxLocationsPerURL は 1 つの URL について覚える「見つかった場所」の上限。⚠ 入力は敵対的でありうる: 出力で切る前に、
// ここで持つ量を抑える。超えた分は数だけ Link.MoreLocations に持つ（出力は「ほかにもある」とだけ言う: 複数のリンクが
// 同じ場所を捨てていると数は足せない）。finding.MaxLocations（出力の 100）より大きいので、普通は Merge が正確に数える。
const MaxLocationsPerURL = 10000

// Result は巡回の結果。並びは決まっている（同じ入力なら同じ出力）。
type Result struct {
	Pages      []Page // 深さ、URL の順
	Links      []Link // URL の順。ページも入る
	NotFetched NotFetched
	// NotFetchedURLs は NotFetched に数えた、取りに行かなかった URL（excluded / external / robots_unavailable / robots_disallowed / private と、
	// --max-links-per-page で確かめなかった over_limits。取りに行った over_limits / over_max_body は Links にあり、sitemap は Known で見る）。
	// sitemap の読み取りが同じ URL を二度数えないため（docs/cli-reference.md §5.2 の not_fetched は「URL の数」: 巡回と sitemap をまたいでも URL ごとに 1）
	NotFetchedURLs map[string]bool
	LimitsHit      []string // "max-pages" / "max-depth" のうち当たったもの
	Robots         RobotsState
	RobotsResp     *fetch.Response // 読めたときの応答（sitemap チェックが Sitemap: の行を使う）
	RobotsErr      error
	RobotsURL      string // 最後に要求した robots.txt の URL（リダイレクトを追った先）
	RobotsFirstURL string // 最初に要求した robots.txt の URL（<origin>/robots.txt）
	RobotsStop     Stop   // Robots が bad-redirect のとき、どう止まったか（loop / bad-location / too-many / private）
	// RobotsPrivateURL は、RobotsStop が private のとき取りに行かなかった行き先
	RobotsPrivateURL string
	// RobotsRules は選ばれたグループ（Robots が ok のとき）
	RobotsRules *Robots
}

// Options は巡回の設定（docs/cli-reference.md §3）。
type Options struct {
	MaxDepth     int
	MaxPages     int
	MaxRedirects int
	Concurrency  int
	Delay        time.Duration // 同じホストへの要求の間隔（robots.txt の Crawl-delay の方が長ければそちら）
	Exclude      []*regexp.Regexp
	NoExternal   bool
	Product      string // robots.txt のグループを選ぶ製品名（User-Agent の先頭）
	Now          func() time.Time
	Sleep        func(time.Duration)
	// Robots は読んであった robots.txt（nil なら Crawl が読む。sitemap チェックと共有するため）
	Robots *RobotsResult
	// Budget は実行全体の上限（nil なら無い。docs/cli-reference.md §3.2）。巡回の要求は当たれば止める。robots.txt は数えるだけ
	Budget *Budget
	// StartHost はプライベートアドレスへの接続の例外（開始 URL のホスト。cli が作り、開始 URL の鎖で決まったもの）。
	// nil なら、巡回の起点のオリジンのホストを開始 URL のホストとする（まだ決まっていない: 最初の接続で決める）
	StartHost *StartHost
	// Gate は同じホストへの要求の番（nil なら Crawl が作る）。sitemap と共有すれば、429 で広げた間隔がその実行の終わりまで効く
	// （docs/cli-reference.md §3.2、TEST-PERSPECTIVES §2.2）。間隔（delay）は作った側が決める
	Gate *HostGate
	// MaxLinksPerPage は 1 ページから確かめる参照先（異なる URL）の上限（0 なら無し。--max-links-per-page）。
	// 超えた分は取りに行かず、ほかのページからも確かめられなければ not_fetched.over_limits、limits_hit に max-links-per-page
	MaxLinksPerPage int
	// FetchReferences は、ページでない参照（サブリソース、外部リンク）も取りに行って生存を確かめるか。
	// links が選ばれていないときは false: 取りに行かず、not_fetched にも数えない（それらは links の分母）。
	// ページの候補（上限の外も）は同じに扱うので、pages_crawled・limits_hit・over_limits は変わらない（TEST-PERSPECTIVES §2.2）
	FetchReferences bool
}

// RobotsResult は robots.txt をどう読めたか（Crawl の Result の Robots* と同じもの。sitemap チェックも使う）。
type RobotsResult struct {
	State    RobotsState
	Resp     *fetch.Response // 読めたときの応答
	Err      error           // 届かなかったとき
	URL      string          // 最後に要求した URL（リダイレクトを追った先）
	FirstURL string          // 最初に要求した URL（<origin>/robots.txt）
	Stop     Stop            // State が bad-redirect のとき、どう止まったか
	// PrivateURL は、Stop が private のとき取りに行かなかった行き先（プライベートアドレス。not_fetched.private に数える）
	PrivateURL string
	Rules      *Robots // State が ok のときの、選ばれたグループ
	Chain      Chain   // 辿った鎖（429 の記録を含む）
}

// ReadRobots は <origin>/robots.txt を読む（docs/cli-reference.md §2.1: sitemap を skip しても巡回のために読む）。
// リダイレクトは MaxRobotsRedirects 回まで追う（RFC 9309 §2.3.1.2）。
// ⚠ 自分のオリジンは証明書を検証せずに取る（証明書の問題は cert が 1 回だけ言う。docs/cli-reference.md §2.1）。
func ReadRobots(ctx context.Context, f fetch.Fetcher, origin string, o Options) *RobotsResult {
	u := origin + "/robots.txt"
	r := &RobotsResult{URL: u, FirstURL: u}
	// ⚠ 鎖の行き先がプライベートアドレス（IP の直書きも、名前を解決した結果も）なら、その前で止め、robots.txt を使えないものとして
	//   扱う（全体を不許可。RFC 9309 §2.3.1.4）。例外はローカルな開始 URL のホストだけ（StartHost）
	sh := o.StartHost
	if sh == nil {
		sh = NewStartHost(origin)
	}
	ch, err := Follow(ctx, f, u, FollowOptions{MaxRedirects: MaxRobotsRedirects, Now: o.Now, Sleep: o.Sleep, SkipVerify: true, SkipVerifyOrigin: origin, Budget: o.Budget, BudgetExempt: true, StartHost: sh})
	r.Chain = ch
	if len(ch.Hops) > 0 {
		r.URL = ch.Hops[len(ch.Hops)-1].URL
	}
	switch {
	case err != nil:
		r.State, r.Err = RobotsUnreachable, err
	case ch.Stop == StopRateLimited:
		r.State = RobotsRateLimited
	case ch.Stop != StopFinal:
		r.State, r.Stop, r.PrivateURL = RobotsBadRedirect, ch.Stop, ch.PrivateURL // ループ・壊れた Location・回数の超過・プライベートアドレスの行き先: 届かないと同じ
	case ch.Final.Status >= 500:
		r.State, r.Resp = RobotsBroken, ch.Final
	case ch.Final.Status >= 400:
		r.State, r.Resp = RobotsMissing, ch.Final
	default:
		r.State, r.Resp = RobotsOK, ch.Final
		r.Rules = ParseRobots(ch.Final.Body, o.Product)
	}
	return r
}

// Delay は同じホストへの要求の間隔: --delay と robots.txt の Crawl-delay の長い方（巡回と sitemap の確認で同じ）。
func (r *RobotsResult) Delay(flag time.Duration) time.Duration {
	if r != nil && r.Rules != nil && r.Rules.CrawlDelay() > flag {
		return r.Rules.CrawlDelay()
	}
	return flag
}

// CrawlAllowed は、robots.txt の状態で巡回（と同じオリジンの生存確認）をしてよいか。
// ok と 4xx（unavailable: 制限なし）だけ。5xx・届かない・429・壊れたリダイレクトはすべて禁止（RFC 9309 §2.3.1.3 / §2.3.1.4）
func (r *RobotsResult) CrawlAllowed() bool {
	return r != nil && (r.State == RobotsOK || r.State == RobotsMissing)
}

// Crawl は、開始ページ（取得済み。start.URL はリダイレクト後の正規化した URL、Depth 0）から同じオリジンを巡回し、
// FetchReferences なら参照された URL の生存も確かめる（docs/cli-reference.md §2.1 の links / headers / redirects、TEST-PERSPECTIVES §2.2）。
// 巡回の起点は start のオリジン（開始 URL がリダイレクトされていれば、行き先のオリジン）。
func Crawl(ctx context.Context, f fetch.Fetcher, start Page, o Options) Result {
	if !IsHTML(&start.Resp) {
		// HTML でない開始ページ（JSON、PDF…）からはリンクを拾わない: 巡回するページが無い
		res := Result{Robots: RobotsNotRead, NotFetchedURLs: map[string]bool{}}
		if o.Robots != nil && o.Robots.PrivateURL != "" {
			// cli が読んだ robots.txt の鎖がプライベートアドレスの行き先で止まっていれば、ここでも数える（取りに行かなかったことを黙らない）
			res.NotFetched.Private, res.NotFetchedURLs[o.Robots.PrivateURL] = 1, true
		}
		return res
	}
	if o.StartHost == nil {
		o.StartHost = NewStartHost(start.URL)
	}
	c := &crawler{ctx: ctx, f: f, o: o, origin: Origin(start.URL), seen: map[string]bool{start.URL: true}, cands: map[string]*candidate{}, capped: map[string][]finding.Location{}, private: map[string]bool{}, notFetched: map[string]bool{}, gate: o.Gate}
	var res Result
	res.NotFetchedURLs = c.notFetched
	rr := o.Robots
	if rr == nil {
		rr = ReadRobots(ctx, f, c.origin, o)
	}
	res.Robots, res.RobotsResp, res.RobotsErr, res.RobotsURL, res.RobotsFirstURL, res.RobotsStop, res.RobotsRules = rr.State, rr.Resp, rr.Err, rr.URL, rr.FirstURL, rr.Stop, rr.Rules
	if u := rr.PrivateURL; u != "" {
		// robots.txt の鎖がプライベートアドレスの行き先で止まった: 取りに行かなかった URL として数える（sitemap は Counted で二度数えない）
		res.RobotsPrivateURL = u
		c.private[u], c.notFetched[u] = true, true
		res.NotFetched.Private++
	}
	if c.gate == nil {
		c.gate = NewHostGate(rr.Delay(o.Delay), o.Now, o.Sleep)
	}
	crawlAllowed := rr.CrawlAllowed()

	start.Refs = c.refsOf(&start, &res)
	start.Resp.Body = nil
	res.Pages = append(res.Pages, start)
	pagesLeft := o.MaxPages - 1
	level := []Page{start}
	for depth := 1; len(level) > 0; depth++ {
		// このレベルのページが参照する URL を集める（重複は 1 つに、場所は全部）
		cands := c.collect(level, &res)
		var toFetch []*candidate
		for _, cd := range cands {
			if !o.FetchReferences && (cd.external || cd.subresource) {
				continue // リンクの生存確認をしない回: ページの候補だけを取る
			}
			switch {
			case c.excluded(cd.url):
				res.NotFetched.Excluded++
				c.notFetched[cd.url] = true
			case cd.external && o.NoExternal:
				res.NotFetched.External++
				c.notFetched[cd.url] = true
			case cd.external:
				toFetch = append(toFetch, cd)
			case !crawlAllowed:
				res.NotFetched.RobotsUnavailable++
				c.notFetched[cd.url] = true
			case res.RobotsRules != nil && !res.RobotsRules.Allowed(pathAndQuery(cd.url)):
				res.NotFetched.RobotsDisallowed++
				c.notFetched[cd.url] = true
			default:
				toFetch = append(toFetch, cd)
			}
		}
		// 深さの上限を超えるレベルのページ候補は辿らない（取ってみて HTML なら、辿らなかった数に入る）
		for _, cd := range toFetch {
			if !cd.external && !cd.subresource && depth > o.MaxDepth {
				cd.skipPage = true
			}
		}
		// 取りに行く。辿るページの候補は GET、それ以外は HEAD。上限で辿らないページ候補も取ってみる（links なら生存確認、
		// どの回でも HTML なら over_limits に数える。docs/cli-reference.md §5.2 の over_limits は
		// 「上限のために辿らなかった HTML のページ」で、links の有無で変わらない）
		c.fetchAll(toFetch)
		level = nil
		for _, cd := range toFetch {
			if cd.err == nil && cd.chain.Stop == StopOverLimits {
				// --max-duration / --max-requests で取りに行かなかった（鎖の途中で止めたものも）: 指摘にせず数える（docs/cli-reference.md §5.2 の over_limits）
				res.NotFetched.OverLimits++
				c.notFetched[cd.url] = true
				cd.chain = Chain{}
				continue
			}
			cd.fetched = true
			if cd.err == nil && cd.chain.Stop == StopPrivate {
				// リダイレクトの先か、名前を解決した先がプライベートアドレス: その前で止めた。
				// 数えるのは取りに行かなかった URL（行き先）
				if next := cd.chain.PrivateURL; !c.private[next] {
					c.private[next] = true
					c.notFetched[next] = true
					res.NotFetched.Private++
				}
			}
			if cd.err != nil || cd.chain.Stop != StopFinal || cd.external || cd.subresource || !IsHTML(cd.chain.Final) {
				continue
			}
			p := Page{URL: cd.chain.Hops[len(cd.chain.Hops)-1].URL, Depth: depth, Resp: *cd.chain.Final}
			if Origin(p.URL) != c.origin || c.seen[p.URL] && p.URL != cd.url {
				continue // リダイレクトで外へ出た、または既に見たページへ飛んだ
			}
			switch {
			case cd.skipPage:
				res.NotFetched.OverLimits++
				res.LimitsHit = addLimit(res.LimitsHit, "max-depth")
			case pagesLeft <= 0:
				res.NotFetched.OverLimits++
				res.LimitsHit = addLimit(res.LimitsHit, "max-pages")
			default:
				pagesLeft--
				c.seen[p.URL] = true
				p.Refs = c.refsOf(&p, &res)
				p.Resp.Body = nil // 参照を拾ったら本文は要らない（headers は Header を見る）
				res.Pages = append(res.Pages, p)
				level = append(level, p)
			}
		}
		// 読んだ本文を巡回の最後まで持たない（ページは上で参照を拾った。それ以外は要らない）
		for _, cd := range toFetch {
			if cd.chain.Final != nil {
				cd.chain.Final.Body = nil
			}
		}
		sort.Slice(level, func(i, j int) bool { return level[i].URL < level[j].URL })
	}
	// --max-links-per-page で確かめなかった URL のうち、ほかのページからも確かめられなかったもの（取りに行かないので HTML かは分からない）
	for u := range c.capped {
		if c.cands[u] == nil && !c.seen[u] {
			res.NotFetched.OverLimits++
			c.notFetched[u] = true
		}
	}
	// 取りに行った URL の結果。場所は巡回の最後まで集める（同じ壊れたリンクが複数のページにあれば 1 件に、場所は全部。TEST-PERSPECTIVES §3.1）
	for _, cd := range c.cands {
		if cd.fetched {
			res.Links = append(res.Links, Link{URL: cd.url, External: cd.external, Chain: cd.chain, Err: cd.err, Locations: cd.locations, MoreLocations: cd.moreLocations, Subresource: cd.subresource, HTTPSPage: cd.httpsPage})
		}
	}
	sort.Slice(res.Links, func(i, j int) bool { return res.Links[i].URL < res.Links[j].URL })
	sort.SliceStable(res.Pages, func(i, j int) bool {
		if res.Pages[i].Depth != res.Pages[j].Depth {
			return res.Pages[i].Depth < res.Pages[j].Depth
		}
		return res.Pages[i].URL < res.Pages[j].URL
	})
	sort.Strings(res.LimitsHit)
	return res
}

type crawler struct {
	ctx    context.Context
	f      fetch.Fetcher
	o      Options
	origin string
	seen   map[string]bool       // 見た URL（ページと候補）。二度取りに行かない
	cands  map[string]*candidate // 参照された URL → 候補（場所は最後まで集まる）
	gate   *HostGate
	// capped は --max-links-per-page で確かめなかった URL とその場所（同じ URL をほかから確かめれば場所は found_on に出る）、
	// private はプライベートアドレスで取らなかった URL（どちらも URL ごとに 1 回数える）
	capped  map[string][]finding.Location
	private map[string]bool
	// notFetched は NotFetched に数えた URL（Result.NotFetchedURLs）
	notFetched map[string]bool
}

type candidate struct {
	url           string
	external      bool
	subresource   bool // a[href] から参照されていない
	httpsPage     bool
	locations     []finding.Location
	moreLocations int
	skipPage      bool
	fetched       bool // 取りに行った（結果が Links に入る）
	chain         Chain
	err           error
}

// hostnameOf はポートを除いたホスト名（IPv6 の角括弧も外す）。
func hostnameOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}

// refsOf はページの参照を拾う。本文が切れていれば拾わず、数える。
func (c *crawler) refsOf(p *Page, res *Result) []Ref {
	if p.Resp.Truncated || p.Resp.Incomplete {
		p.Truncated = true
		res.NotFetched.OverMaxBody++
		return nil
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil
	}
	return ExtractRefs(u, p.Resp.Body)
}

// collect は、このレベルのページが参照する URL のうち、まだ見ていないものを URL 順に集める。
// すでに見た URL への参照も、場所だけは記録する（同じリンクが複数のページにあれば場所を全部持つ）。
func (c *crawler) collect(level []Page, res *Result) []*candidate {
	byURL := map[string]*candidate{}
	for _, p := range level {
		https := strings.HasPrefix(p.URL, "https://")
		distinct := map[string]bool{} // このページの異なる参照先（--max-links-per-page はこれで数える）
		for _, r := range p.Refs {
			if r.URL == "" {
				continue
			}
			if !c.o.FetchReferences && (Origin(r.URL) != c.origin || r.Subresource) {
				continue // links を選んでいない回: 参照先は見ない（数えもしない。private も上限も参照先の分）
			}
			if c.o.StartHost.Private(r.URL) {
				// 公開サイトの HTML が指す CI の機械の中を叩かない（docs/cli-reference.md §5.2 の private）
				if !c.private[r.URL] {
					c.private[r.URL] = true
					c.notFetched[r.URL] = true
					res.NotFetched.Private++
				}
				continue
			}
			loc := finding.Element(p.URL, r.Element, r.Attribute, r.Value)
			if !distinct[r.URL] {
				if c.o.MaxLinksPerPage > 0 && len(distinct) >= c.o.MaxLinksPerPage {
					// 上限を超えた分は取りに行かない。場所は覚える（同じ URL をほかから確かめていれば found_on に出る）。
					// ほかのページから確かめられなければ、最後に over_limits に数える
					res.LimitsHit = addLimit(res.LimitsHit, "max-links-per-page")
					if old := c.cands[r.URL]; old != nil {
						old.addLocation(loc)
					} else if !c.seen[r.URL] {
						c.capped[r.URL] = append(c.capped[r.URL], loc)
					}
					continue
				}
				distinct[r.URL] = true
			}
			if old := c.cands[r.URL]; old != nil && byURL[r.URL] == nil {
				old.addLocation(loc) // 前のレベルで取りに行った（または外した）URL
				continue
			}
			if c.seen[r.URL] && byURL[r.URL] == nil {
				continue // ページ（開始ページを含む）への参照: 生存は分かっている
			}
			cd := byURL[r.URL]
			if cd == nil {
				cd = &candidate{url: r.URL, external: Origin(r.URL) != c.origin, subresource: true}
				byURL[r.URL] = cd
				c.cands[r.URL] = cd
				c.seen[r.URL] = true
				for _, l := range c.capped[r.URL] { // 前に上限で見送った場所
					cd.addLocation(l)
				}
			}
			if !r.Subresource {
				cd.subresource = false
			}
			if https {
				cd.httpsPage = true
			}
			cd.addLocation(loc)
		}
	}
	out := make([]*candidate, 0, len(byURL))
	for _, cd := range byURL {
		out = append(out, cd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].url < out[j].url })
	return out
}

func (cd *candidate) addLocation(l finding.Location) {
	if len(cd.locations) >= MaxLocationsPerURL {
		cd.moreLocations++
		return
	}
	cd.locations = append(cd.locations, l)
}

func (c *crawler) excluded(u string) bool {
	for _, re := range c.o.Exclude {
		if re.MatchString(u) {
			return true
		}
	}
	return false
}

// fetchAll は候補を同時接続数の中で取りに行く。goroutine はこの呼び出しの中で全部終わる（.claude/rules/go.md）。
func (c *crawler) fetchAll(cands []*candidate) {
	n := max(c.o.Concurrency, 1)
	if c.o.Budget.countsRequests() {
		// --max-requests があれば 1 つずつ、候補の順（URL の順）に: どの URL が上限の中に入るかを、同じ入力で同じにする
		// （TEST-PERSPECTIVES §2.3。並行すると、最後の 1 回を取るのは先に番を取った goroutine になる）
		n = 1
	}
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for _, cd := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(cd *candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			c.fetchOne(cd)
		}(cd)
	}
	wg.Wait()
}

// fetchOne は 1 つの URL を取る。辿るページの候補（同じオリジンの a[href]で上限の中）は GET、それ以外は HEAD（4xx / 5xx なら本文を読まない GET でやり直す: followForLiveness）。
func (c *crawler) fetchOne(cd *candidate) {
	method := "HEAD"
	if !cd.external && !cd.subresource && !cd.skipPage {
		method = "GET"
	}
	// ⚠ 自分のオリジン（開始 URL のリダイレクト後のオリジン）の URL は証明書を検証せずに取る（証明書の問題は cert が 1 回だけ言う。
	//   docs/cli-reference.md §2.1）。鎖の途中で外へ出れば、そこからは検証する: 失敗は unreachable（tls）として出る（TEST-PERSPECTIVES §3.3）
	opts := FollowOptions{MaxRedirects: c.o.MaxRedirects, Now: c.o.Now, Sleep: c.o.Sleep, Method: method, Gate: c.gate, SkipVerify: true, SkipVerifyOrigin: c.origin, StartHost: c.o.StartHost, Budget: c.o.Budget}
	if method == "GET" {
		// 本文を読むのは、ページとして解析しうる応答だけ: 同じオリジンに着いた 2xx の HTML。
		// ⚠ 外へリダイレクトされた先や HTML でない応答の本文は読まない（巨大なファイルを --max-body まで読んでしまう）
		opts.WantBody = func(u string, status int, header map[string][]string) bool {
			return Origin(Normalize(u)) == c.origin && IsHTML(&fetch.Response{Status: status, Header: header})
		}
	}
	cd.chain, cd.err = followForLiveness(c.ctx, c.f, cd.url, opts)
}

// followForLiveness は生存確認のために辿る: HEAD が 4xx / 5xx なら GET でやり直し、GET の結果で判断する
// （405 / 501 だけでなく。HEAD にだけ 404 を返すサーバーがある。TEST-PERSPECTIVES §3.1）。
// やり直しの GET は本文を読まない（生存確認に本文は要らない）。
func followForLiveness(ctx context.Context, f fetch.Fetcher, u string, opts FollowOptions) (Chain, error) {
	ch, err := Follow(ctx, f, u, opts)
	if err == nil && opts.Method == "HEAD" && ch.Stop == StopFinal && ch.Final.Status >= 400 {
		opts.Method = "GET"
		opts.WantBody = func(string, int, map[string][]string) bool { return false } // 生存確認なので本文は読まない
		ch, err = Follow(ctx, f, u, opts)
	}
	return ch, err
}

// IsHTML は、応答が HTML のページか（2xx で Content-Type が HTML。無ければ HTML ではないとみなす: PDF や画像からリンクを拾わない）。
func IsHTML(r *fetch.Response) bool {
	if r == nil || r.Status < 200 || r.Status >= 300 {
		return false
	}
	for _, v := range r.Header["Content-Type"] {
		mt, _, err := mime.ParseMediaType(v)
		if err == nil && (mt == "text/html" || mt == "application/xhtml+xml") {
			return true
		}
	}
	return false
}

func pathAndQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "/"
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

func addLimit(hit []string, name string) []string {
	for _, h := range hit {
		if h == name {
			return hit
		}
	}
	return append(hit, name)
}

// HostGate は、同じホストへの要求の間隔を守る（docs/cli-reference.md §3.2 の --delay）。
type HostGate struct {
	mu    sync.Mutex
	hosts map[string]*hostSlot
	delay time.Duration
	now   func() time.Time
	sleep func(time.Duration)
}

// NewHostGate は、同じホストへの要求の間隔を delay にする番を作る（--delay と Crawl-delay の長い方: RobotsResult.Delay）。
func NewHostGate(delay time.Duration, now func() time.Time, sleep func(time.Duration)) *HostGate {
	return &HostGate{delay: delay, now: now, sleep: sleep}
}

// hostSlot は 1 つのホストの番: 同じホストへの要求は 1 つずつ（mu）、前の応答が返ってから delay 置いて次（last）。
// gap は 429 を受けて広げた間隔（0 なら delay のまま）。backoff だけが広げ、狭めない。
type hostSlot struct {
	mu   sync.Mutex
	last time.Time
	gap  time.Duration
}

// 429 を受けたホストへの間隔（docs/test-perspectives.md §2 がこの値を書く。TestBackoffValuesAreTheDocumentedValues が留める。
// 既定の間隔と同じホストへの直列化だけでは、429 を受けつづけるサイトがある）。下限は DefaultRetryWait と同じ 1 秒
const MaxBackoffGap = 10 * time.Second

// slot は host の番を返す（無ければ作る）。
func (g *HostGate) slot(host string) *hostSlot {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hosts == nil {
		g.hosts = map[string]*hostSlot{}
	}
	s := g.hosts[host]
	if s == nil {
		s = &hostSlot{}
		g.hosts[host] = s
	}
	return s
}

// widen は 429 を受けたことを s に記録し、その実行の終わりまで間隔を広げる（呼ぶ側が s.mu を持つ）:
// 今の間隔の 2 倍、asked（読めた Retry-After。0 なら無し）、DefaultRetryWait のうち最も長いもの、上限 MaxBackoffGap。
// ⚠ asked は外からの値: MaxRetryWait を超える値は間隔に使わない（そのときは上限まで広がる 2 倍の規則に任せる）。
func (g *HostGate) widen(s *hostSlot, asked time.Duration) {
	cur := s.gap
	if cur < g.delay {
		cur = g.delay
	}
	next := 2 * cur
	if asked > 0 && asked <= MaxRetryWait && asked > next {
		next = asked
	}
	if next < DefaultRetryWait {
		next = DefaultRetryWait
	}
	if next > MaxBackoffGap {
		next = MaxBackoffGap
	}
	s.gap = next
}

// hostTurn は 1 つの要求の番（acquire が返す）。nil なら何もしない（Gate が無い）。
type hostTurn struct {
	g        *HostGate
	s        *hostSlot
	released bool
}

// backoff は、この番の応答が 429 だったことを、番を持ったまま記録する。
// ⚠ 番を手放す前に広げる: 手放してからでは、同じホストを待っていた要求が広げる前の間隔で出てしまう。
//
//	手放した番では何もしない: 鍵を持たずに書かない（順を間違えれば間隔が広がらず、テストが落ちる）。
func (t *hostTurn) backoff(asked time.Duration) {
	if t != nil && !t.released {
		t.g.widen(t.s, asked)
	}
}

// release は番を手放す（応答のあとに呼ぶ）。次の要求の間隔はここから数える。
func (t *hostTurn) release() {
	if t != nil && !t.released {
		t.released = true
		t.s.last = t.g.now()
		t.s.mu.Unlock()
	}
}

// acquire は host への要求の番を取る。返った番の release を応答のあとに呼ぶ。
// ⚠ 同じホストへ同時に 2 つ以上つながない（既定で点検対象から 429 を受けた）。--concurrency は別のホストの並行
// retry は 429 のやり直し: 呼ぶ側が Retry-After（無ければ DefaultRetryWait）だけ待ったあとなので、広げた間隔は使わず --delay だけ守る
// （広げた間隔は次の要求から。ただし同じホストを待っていた要求が先に番を取れば、やり直しはその後になり、文が言う「待った時間」
// = 429 からやり直しまでの実際の経過にその分も入る。TEST-PERSPECTIVES §2）。
func (g *HostGate) acquire(host string, retry bool) *hostTurn {
	t, _ := g.acquireUnless(host, retry, nil)
	return t
}

// acquireUnless は acquire と同じだが、番を取ったあと、寝る前に stop を見る。真なら寝ずに番を手放して false を返す
// （実行全体の上限に当たった要求: 同じホストを待つ列に並んでいた要求が、順に間隔を寝てから断られないように。docs/cli-reference.md §3.2）。
func (g *HostGate) acquireUnless(host string, retry bool, stop func() bool) (*hostTurn, bool) {
	if g == nil {
		return nil, stop == nil || !stop()
	}
	s := g.slot(host)
	s.mu.Lock()
	if stop != nil && stop() {
		s.mu.Unlock()
		return nil, false
	}
	wait := g.delay
	if !retry && s.gap > wait {
		wait = s.gap
	}
	if wait > 0 && !s.last.IsZero() {
		if d := s.last.Add(wait).Sub(g.now()); d > 0 {
			g.sleep(d)
		}
	}
	return &hostTurn{g: g, s: s}, true
}
