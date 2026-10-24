package crawl

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
)

// SitemapFile は読んだ 1 つの sitemap（または sitemap index）。
type SitemapFile struct {
	URL     string
	Status  int    // 届いたときのステータス（最後の応答）
	Err     error  // 届かなかったとき（*fetch.UnreachableError）
	Problem string // XML として読めなかったとき、何が壊れていたか（1 文。docs/cli-reference.md §5.2 の problem）
	// RateLimited は、やり直しても 429 だった（sitemap.rate-limited）。RetryAfter は読めた Retry-After の秒数
	RateLimited bool
	RetryAfter  *int
	// BadLocs は URL にならなかった <loc> の数、FirstBadLoc はその最初のもの（sitemap.invalid の problem）
	BadLocs     int
	FirstBadLoc string
	// Excluded は --exclude に一致したので取りに行かなかった（not_fetched.excluded に数える。指摘は出さない）
	Excluded bool
	// Private は、ファイルかそのリダイレクトの先がプライベートアドレスか localhost だったので取りに行かなかった
	// （not_fetched.private に数える。指摘は出さない。docs/test-perspectives.md §2.2）
	Private bool
	// OverLimits は、--max-duration / --max-requests に当たって取りに行かなかった（not_fetched.over_limits。指摘は出さない）
	OverLimits bool
	Index      bool // sitemapindex だった
	// Truncated は、本文が --max-body で切れていて最後まで読めなかったこと。Incomplete は相手が途中で接続を切った（時間切れ、切断）こと。
	// どちらも読めたところまでの URL は確かめる。文は別（前者は tenken の上限、後者は相手の都合。CLAUDE.md §4-1）
	Truncated  bool
	Incomplete bool
	// URLs はこのファイルに載っていた URL（<loc>）の数。index なら子の sitemap の数
	URLs int
}

// SitemapEntry は sitemap に載っていた URL。
type SitemapEntry struct {
	Loc     string // 書かれていたまま
	URL     string // 正規化した URL（"" なら取りに行けない形）
	Sitemap string // 載っていた sitemap の URL
	// 取りに行かなかった理由（docs/cli-reference.md §2.2 の url-external / url-disallowed、§3.1 の --exclude）。どれも false なら取りに行った
	External   bool
	Disallowed bool
	Excluded   bool
	// OverLimits は、--max-duration / --max-requests に当たって確かめきらなかった（not_fetched.over_limits。指摘は出さない）
	OverLimits bool
	// 取りに行った結果
	Chain Chain
	Err   error
}

// SitemapResult は sitemap の読み取りの結果。
type SitemapResult struct {
	Declared bool          // robots.txt に Sitemap: の行があった
	Files    []SitemapFile // 読もうとしたファイル（URL の順。--exclude と private で取りに行かなかったものも入る）
	Entries  []SitemapEntry
	Missing  bool // Sitemap: の行が無く、/sitemap.xml も 4xx（docs/cli-reference.md §2.2 の sitemap.missing）
	LimitHit bool // --max-sitemap-urls に当たった
	// NestingHit は、index の入れ子が MaxSitemapNesting より深くて読まなかった子があった（limits_hit の sitemap-nesting）
	NestingHit bool
	// 数（docs/cli-reference.md §5.2 の not_fetched。URL ごとに 1: 同じ URL が 2 度載っていても 1）
	External, Disallowed, Excluded int
	// Private は、プライベートアドレスか localhost だったので取りに行かなかった URL: sitemap のファイル自体と、
	// ファイルや載っている URL のリダイレクトの行き先（載っている URL 自体は、プライベートアドレスなら別オリジンなので External に数える）
	Private int
	// OverLimits は、--max-duration / --max-requests に当たって取りに行かなかった URL（ファイルと載っている URL）
	OverLimits int
}

// SitemapOptions は読み取りの設定。
type SitemapOptions struct {
	MaxURLs      int // --max-sitemap-urls
	MaxRedirects int
	Robots       *RobotsResult    // 読んであった robots.txt（Sitemap: の行と、禁止の判定）
	Exclude      []*regexp.Regexp // --exclude: 一致する URL（sitemap のファイルも、載っている URL も）は取りに行かない
	// Known は、すでに取りに行った URL の結果（巡回で取ったもの。二度取りに行かない）。nil なら無し
	Known func(url string) (Chain, error, bool)
	// Counted は、巡回がすでに not_fetched に数えた URL か（Result.NotFetchedURLs）。真なら数えない（URL ごとに 1）。nil なら無し
	Counted func(url string) bool
	Delay   time.Duration // 同じホストへの要求の間隔（--delay。Crawl-delay との長い方は RobotsResult.Delay）
	Now     func() time.Time
	Sleep   func(time.Duration)
	// Budget は実行全体の上限（nil なら無い。docs/cli-reference.md §3.2）。当たれば、そのファイルや URL は取りに行かず over_limits に数える
	Budget *Budget
	// StartHost はプライベートアドレスへの接続の例外（開始 URL のホスト。Options.StartHost と同じ）。nil なら origin のホスト
	StartHost *StartHost
	// Gate は同じホストへの要求の番（nil なら作る）。巡回と同じ番を渡せば、巡回で 429 を受けて広げた間隔と、
	// 巡回の最後の応答からの --delay が sitemap の要求にも効く
	Gate *HostGate
}

// MaxSitemapNesting は sitemap index の入れ子を追う深さの上限（ループや無限の入れ子で止まらなくならない）。
const MaxSitemapNesting = 10

// MaxSitemapDecompressed は gzip を展開した sitemap の大きさの上限（zip 爆弾を読み切らない）。
// sitemaps.org は展開後 50 MB までを許すので、それより大きい値。
const MaxSitemapDecompressed = 64 << 20

// ReadSitemaps は、robots.txt の Sitemap: の行（無ければ <origin>/sitemap.xml）から sitemap を読み、載っている URL を
// 確かめる（docs/cli-reference.md §2.1 の sitemap、TEST-PERSPECTIVES §3.5）。URL はステータスを見るだけで、ページとして解析・巡回しない。
func ReadSitemaps(ctx context.Context, f fetch.Fetcher, origin string, o SitemapOptions) SitemapResult {
	var res SitemapResult
	var starts []string
	if o.Robots != nil && o.Robots.Rules != nil && len(o.Robots.Rules.Sitemaps()) > 0 {
		res.Declared = true
		for _, s := range o.Robots.Rules.Sitemaps() {
			if u := Normalize(s); u != "" {
				starts = append(starts, u)
			}
		}
	}
	if len(starts) == 0 {
		starts = []string{origin + "/sitemap.xml"}
	}
	gate := o.Gate
	if gate == nil {
		gate = NewHostGate(o.Robots.Delay(o.Delay), o.Now, o.Sleep)
	}
	if o.StartHost == nil {
		o.StartHost = NewStartHost(origin)
	}
	r := &sitemapReader{ctx: ctx, f: f, origin: origin, o: o, res: &res, seen: map[string]bool{}, counted: map[string]bool{}, gate: gate}
	if o.Robots != nil && o.Robots.PrivateURL != "" {
		// robots.txt の鎖がプライベートアドレスの行き先で止まった: 取りに行かなかった URL（巡回が数えていれば二度数えない）
		r.countOnce(o.Robots.PrivateURL, &res.Private)
	}
	for _, s := range starts {
		r.read(s, 0)
	}
	if !res.Declared && len(res.Files) == 1 && res.Files[0].Err == nil && res.Files[0].Status >= 400 && res.Files[0].Status < 500 && res.Files[0].Status != 429 {
		res.Missing = true
	}
	r.checkEntries()
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].URL < res.Files[j].URL })
	sort.SliceStable(res.Entries, func(i, j int) bool {
		if res.Entries[i].Sitemap != res.Entries[j].Sitemap {
			return res.Entries[i].Sitemap < res.Entries[j].Sitemap
		}
		return res.Entries[i].Loc < res.Entries[j].Loc
	})
	return res
}

type sitemapReader struct {
	ctx     context.Context
	f       fetch.Fetcher
	origin  string
	o       SitemapOptions
	res     *SitemapResult
	seen    map[string]bool
	counted map[string]bool // not_fetched に数えた URL（URL ごとに 1 回）
	gate    *HostGate
	count   int // 読んだ URL の数（上限と比べる）
}

func (r *sitemapReader) read(u string, depth int) {
	if r.seen[u] {
		return
	}
	if depth > MaxSitemapNesting {
		r.res.NestingHit = true // 読まなかった子がある: 黙らない
		return
	}
	if r.limitReached() {
		r.res.LimitHit = true // 読む sitemap がまだあるのに上限: ファイルの間で当たっても言う
		return
	}
	r.seen[u] = true
	file := SitemapFile{URL: u}
	if r.excluded(u) {
		file.Excluded = true
		r.res.Files = append(r.res.Files, file)
		r.countOnce(u, &r.res.Excluded)
		return
	}
	if r.o.StartHost.Private(u) {
		// 公開サイトの robots.txt や index が CI の機械の中を指していても叩かない（参照と同じ決まり）
		file.Private = true
		r.res.Files = append(r.res.Files, file)
		r.countOnce(u, &r.res.Private)
		return
	}
	// ⚠ 自分のオリジンは検証せずに取る（証明書の問題は cert が言う）。sitemap が別のオリジンにあれば検証する
	ch, err := Follow(r.ctx, r.f, u, FollowOptions{MaxRedirects: r.o.MaxRedirects, Now: r.o.Now, Sleep: r.o.Sleep, SkipVerify: true, SkipVerifyOrigin: r.origin, Gate: r.gate, StartHost: r.o.StartHost, Budget: r.o.Budget})
	if err == nil && ch.Stop == StopOverLimits {
		file.OverLimits = true
		r.res.Files = append(r.res.Files, file)
		r.countOnce(u, &r.res.OverLimits)
		return
	}
	if err != nil {
		file.Err = err
		r.res.Files = append(r.res.Files, file)
		return
	}
	if ch.Stop == StopRateLimited {
		file.RateLimited = true // sitemap.rate-limited（check が文を書く）
		if ch.RetryAfterReadable {
			ra := int(ch.RetryAfterAsked / time.Second)
			file.RetryAfter = &ra
		}
		r.res.Files = append(r.res.Files, file)
		return
	}
	if ch.Stop == StopPrivate {
		file.Private = true
		r.res.Files = append(r.res.Files, file)
		r.countOnce(ch.PrivateURL, &r.res.Private) // 取りに行かなかった URL（行き先、または名前を解決したらプライベートアドレスだったファイル）で数える
		return
	}
	if ch.Stop != StopFinal {
		file.Problem = fmt.Sprintf("the sitemap could not be fetched: its redirects %s", stopWord(ch.Stop))
		r.res.Files = append(r.res.Files, file)
		return
	}
	file.Status = ch.Final.Status
	if file.Status < 200 || file.Status >= 300 {
		r.res.Files = append(r.res.Files, file)
		return
	}
	body := ch.Final.Body
	file.Truncated, file.Incomplete = ch.Final.Truncated, ch.Final.Incomplete
	if isGzip(body) {
		var gerr error
		body, gerr = gunzip(body)
		switch {
		case errors.Is(gerr, errDecompressedTooLarge):
			file.Problem = fmt.Sprintf("the sitemap is gzip-compressed and larger than %d MiB when decompressed, so tenken did not read it (sitemaps.org allows 50 MB uncompressed)", MaxSitemapDecompressed>>20)
		case gerr != nil && (file.Truncated || file.Incomplete):
			// 本文が途中で切れている（tenken の --max-body か、相手の切断）: 展開できたところまでを読む（下の文になる）
			gerr = nil
		case gerr != nil:
			file.Problem = "the sitemap starts with the gzip magic bytes but could not be decompressed as gzip"
		}
		if gerr != nil {
			r.res.Files = append(r.res.Files, file)
			return
		}
	}
	locs, index, problem := parseSitemap(body)
	file.Index, file.URLs = index, len(locs)
	switch {
	case file.Truncated:
		// 本文が切れていれば XML も途中で終わるが、それは tenken の上限のせいで、サイトの XML が壊れているのではない（CLAUDE.md §4-1）
		file.Problem = "the sitemap is larger than --max-body, so only its first part was read (raise --max-body to read it all)"
	case file.Incomplete:
		// 相手が途中で切った: --max-body を上げても直らない。何が起きたかを言う
		file.Problem = fmt.Sprintf("the connection ended before the whole sitemap arrived (%s), so only its first part was read; try again", incompleteWord(ch.Final.IncompleteReason))
	case problem != "":
		file.Problem = problem
	}
	fi := len(r.res.Files)
	r.res.Files = append(r.res.Files, file)
	// URL にならない <loc> の文は、途中で止まっても（上限）、ほかの問題があっても、必ず付ける（数えたものは言う）
	defer func() {
		f := &r.res.Files[fi]
		if f.BadLocs == 0 {
			return
		}
		bad := fmt.Sprintf("%d of %d <loc> values are not URLs (the first is %s)", f.BadLocs, f.URLs, short(f.FirstBadLoc))
		others := "; the others were checked"
		if f.Index {
			others = "; the others were read as sitemaps"
		}
		if f.Problem == "" {
			f.Problem = bad + others
		} else {
			f.Problem += "; also " + bad
		}
	}()
	for _, loc := range locs {
		n := Normalize(loc)
		if n == "" {
			// URL にならない <loc>: その sitemap の sitemap.invalid（problem に数と例）。ほかの URL は確かめる
			if r.res.Files[fi].BadLocs == 0 {
				r.res.Files[fi].FirstBadLoc = loc
			}
			r.res.Files[fi].BadLocs++
			continue
		}
		if index {
			r.read(n, depth+1)
			continue
		}
		if r.limitReached() {
			r.res.LimitHit = true
			return
		}
		r.count++
		r.res.Entries = append(r.res.Entries, SitemapEntry{Loc: loc, URL: n, Sitemap: u})
	}
}

// incompleteWord は本文が途中で切れた理由の語（fetch.Response.IncompleteReason: timeout / connect）。
func incompleteWord(r fetch.Reason) string {
	if r == fetch.ReasonTimeout {
		return "the server stopped sending within --timeout"
	}
	return "the server closed the connection"
}

// short は人に見せる値を短くする（敵対的な <loc> をそのまま文に入れない）。
func short(v string) string {
	if n := 0; len(v) > 60 {
		for i := range v {
			if n++; n > 60 {
				return fmt.Sprintf("%q…", v[:i])
			}
		}
	}
	return fmt.Sprintf("%q", v)
}

func (r *sitemapReader) limitReached() bool { return r.o.MaxURLs > 0 && r.count >= r.o.MaxURLs }

func (r *sitemapReader) excluded(u string) bool {
	for _, re := range r.o.Exclude {
		if re.MatchString(u) {
			return true
		}
	}
	return false
}

// countOnce は not_fetched の数を URL ごとに 1 回だけ増やす（同じ URL が複数の sitemap に載っていても 1。cli-reference.md §5.2 "The number of URLs not fetched"）。
func (r *sitemapReader) countOnce(u string, n *int) {
	if r.counted[u] {
		return
	}
	r.counted[u] = true
	if r.o.Counted != nil && r.o.Counted(u) {
		return // 巡回が数えた。同じ URL を二度数えない（docs/test-perspectives.md §3）
	}
	*n++
}

// checkEntries は載っていた URL を確かめる: --exclude、別オリジン、robots.txt の禁止は取りに行かず、それ以外は HEAD（4xx / 5xx なら本文を読まない GET でやり直す: followForLiveness）。
func (r *sitemapReader) checkEntries() {
	done := map[string]bool{}
	for i := range r.res.Entries {
		e := &r.res.Entries[i]
		switch {
		case e.URL == "":
			continue // read は URL にならない <loc> を Entries に入れない（その sitemap の problem に数える）。念のため
		case r.excluded(e.URL):
			e.Excluded = true
			r.countOnce(e.URL, &r.res.Excluded)
			continue
		case Origin(e.URL) != r.origin:
			e.External = true
			r.countOnce(e.URL, &r.res.External)
			continue
		case r.o.Robots != nil && !r.o.Robots.CrawlAllowed(), r.o.Robots != nil && r.o.Robots.Rules != nil && !r.o.Robots.Rules.Allowed(pathAndQuery(e.URL)):
			e.Disallowed = true
			r.countOnce(e.URL, &r.res.Disallowed)
			continue
		}
		if done[e.URL] {
			continue // 同じ URL が 2 度載っていれば、最初のだけ取りに行く（指摘は Merge がまとめる）
		}
		done[e.URL] = true
		if r.o.Known != nil {
			if ch, err, ok := r.o.Known(e.URL); ok {
				e.Chain, e.Err = ch, err
				continue
			}
		}
		noBody := func(string, int, map[string][]string) bool { return false } // 生存確認: ステータスだけ見る（本文は読まない）
		opts := FollowOptions{MaxRedirects: r.o.MaxRedirects, Now: r.o.Now, Sleep: r.o.Sleep, Method: "HEAD", SkipVerify: true, SkipVerifyOrigin: r.origin, Gate: r.gate, WantBody: noBody, StartHost: r.o.StartHost, Budget: r.o.Budget}
		e.Chain, e.Err = followForLiveness(r.ctx, r.f, e.URL, opts)
		if e.Err == nil && e.Chain.Stop == StopOverLimits {
			e.OverLimits, e.Chain = true, Chain{}
			r.countOnce(e.URL, &r.res.OverLimits)
			continue
		}
		if e.Err == nil && e.Chain.Stop == StopPrivate {
			r.countOnce(e.Chain.PrivateURL, &r.res.Private) // 取りに行かなかった URL（行き先）ごとに 1
		}
	}
	// 同じ URL の 2 つ目以降に、1 つ目の結果を写す
	first := map[string]*SitemapEntry{}
	for i := range r.res.Entries {
		e := &r.res.Entries[i]
		if e.URL == "" || e.External || e.Disallowed || e.Excluded {
			continue
		}
		if f, ok := first[e.URL]; ok {
			e.Chain, e.Err, e.OverLimits = f.Chain, f.Err, f.OverLimits
		} else {
			first[e.URL] = e
		}
	}
}

func stopWord(s Stop) string {
	switch s {
	case StopLoop:
		return "loop"
	case StopBadLocation:
		return "point to a Location that is not a usable URL"
	case StopRateLimited:
		return "end in 429"
	}
	return "go on too many times"
}

// parseSitemap は urlset か sitemapindex を読み、<loc> を返す（sitemaps.org の protocol）。
// ⚠ 入力は敵対的でありうる: トークン単位で読み、要素の木を作らない。壊れていれば問題の 1 文を返す（読めたところまでの loc も返す）。
func parseSitemap(body []byte) (locs []string, index bool, problem string) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = true
	// sitemap は UTF-8 でなければならない（sitemaps.org の protocol）。別の encoding を名乗る XML は読まず、そう言う
	var declared string
	dec.CharsetReader = func(charset string, in io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "utf-8", "utf8", "us-ascii", "ascii":
			return in, nil
		}
		declared = charset
		return nil, errors.New("not utf-8")
	}
	var root string
	var stack []string
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			var se *xml.SyntaxError
			if errors.As(err, &se) {
				return locs, index, fmt.Sprintf("the XML is not well-formed at line %d: %s", se.Line, se.Msg)
			}
			if declared != "" {
				return locs, index, fmt.Sprintf("the XML declares encoding %q; a sitemap must be UTF-8 (sitemaps.org protocol)", declared)
			}
			return locs, index, "the XML could not be read as a sitemap"
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if root == "" {
				root = t.Name.Local
				switch root {
				case "urlset":
				case "sitemapindex":
					index = true
				default:
					return nil, false, fmt.Sprintf("the root element is <%s>, not <urlset> or <sitemapindex>", root)
				}
			}
			stack = append(stack, t.Name.Local)
			text.Reset()
		case xml.CharData:
			if len(stack) > 0 && stack[len(stack)-1] == "loc" {
				text.Write(t)
			}
		case xml.EndElement:
			if len(stack) > 0 && stack[len(stack)-1] == "loc" && len(stack) >= 2 {
				parent := stack[len(stack)-2]
				if (index && parent == "sitemap") || (!index && parent == "url") {
					if loc := strings.TrimSpace(text.String()); loc != "" {
						locs = append(locs, loc)
					}
				}
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == "" {
		return nil, false, "the file has no XML element (is it a sitemap?)"
	}
	return locs, index, ""
}

func isGzip(b []byte) bool { return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b }

var errDecompressedTooLarge = errors.New("decompressed sitemap is larger than MaxSitemapDecompressed")

// gunzip は gzip を展開する。⚠ 展開後の大きさに上限を置く（zip 爆弾）: MaxSitemapDecompressed まで。
// 圧縮率で区切らない。
func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	// ⚠ 途中で切れた gzip（--max-body で切った本文）は、展開できたところまでを返す（呼ぶ側が Truncated を見て、切れたのは tenken だと言う）
	out, err := io.ReadAll(io.LimitReader(zr, MaxSitemapDecompressed+1))
	if err != nil {
		return out, err
	}
	if len(out) > MaxSitemapDecompressed {
		return nil, errDecompressedTooLarge
	}
	return out, nil
}
