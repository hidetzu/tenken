// Package sitemap は sitemap / robots のチェック（docs/cli-reference.md §2.1 の sitemap、§2.2 の sitemap.* のルール）。
// 読んだ結果から指摘を返すだけで、取りに行かない（docs/design.md §2。読むのは crawl）。
package sitemap

import (
	"errors"
	"fmt"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/finding"
)

// Robots は robots.txt をどう読めたか（crawl.RobotsResult と同じ値。check は crawl を import しない）。
type Robots struct {
	State      string // ok / missing / broken / unreachable / rate-limited / bad-redirect（crawl.RobotsState の値）
	URL        string // 最後に要求した URL
	Status     int    // missing / broken のときのステータス
	Err        error  // unreachable のとき（*fetch.UnreachableError）
	RetryAfter *int   // rate-limited のとき: 読めた Retry-After の秒数（無ければ nil）
	BadStop    string // bad-redirect のとき、どう止まったか（loop / bad-location / too-many / private）
	PrivateURL string // BadStop が private のとき、取りに行かなかった行き先
	FirstURL   string // bad-redirect のとき、最初に要求した URL（too-many の文が言う）
}

// File は読んだ sitemap（crawl.SitemapFile と同じ値）。
type File struct {
	URL      string
	Status   int
	Err      error  // 届かなかった（*fetch.UnreachableError）
	Problem  string // XML として読めなかった理由の 1 文（"" なら読めた）
	Excluded bool   // --exclude に一致したので取りに行かなかった（指摘は出さない。not_fetched.excluded が言う）
	// RateLimited はやり直しても 429 で読めなかった（sitemap.rate-limited）。RetryAfter は読めた Retry-After の秒数（無ければ nil）
	RateLimited bool
	RetryAfter  *int
}

// Entry は sitemap に載っていた URL（crawl.SitemapEntry と同じ値）。
type Entry struct {
	Loc        string
	URL        string // 正規化した URL（"" なら取りに行けない形）
	Sitemap    string
	External   bool
	Disallowed bool
	Excluded   bool             // --exclude に一致したので取りに行かなかった（指摘は出さない）
	Status     int              // 最後の応答（Final のとき）
	Final      bool             // 鎖がリダイレクトでない応答に着いた
	Redirected bool             // 鎖に 1 段以上のリダイレクトがあった
	Chain      []map[string]any // [{url, status}]
	Err        error            // *fetch.UnreachableError
	RateLimit  *RateLimit       // やり直しても 429 のとき（それ以外は nil）
}

// RateLimit は 429 を受けたときの記録（crawl.Chain の同じ名前の値。links.RateLimit と同じ）。
type RateLimit struct {
	Retried            bool
	Waited             time.Duration
	RetryAfterRaw      string
	RetryAfterReadable bool
	RetryAfterAsked    time.Duration
}

// Input はチェックへの入力。
type Input struct {
	Robots   *Robots // nil なら robots についての指摘は出さない（sitemap を --skip したとき。docs/cli-reference.md §2.1）
	Declared bool    // robots.txt に Sitemap: の行があった
	Missing  bool    // Sitemap: が無く /sitemap.xml が 4xx（429 を除く）
	Files    []File
	Entries  []Entry
	Timeout  time.Duration // --timeout（届かなかった理由の文）
}

// Check は sitemap.* の指摘を返す（docs/cli-reference.md §2.2、§5.2）。
//   - robots-missing（4xx、info）/ robots-broken（5xx、warn、status）/ robots-unreachable（warn、reason。リダイレクトが使えないのは
//     reason: redirect）/ robots-rate-limited（warn、retry_after_seconds）。found_on は空
//   - sitemap.missing（info、found_on は空）、sitemap.invalid（fail、detail.problem、found_on は空）。宣言された sitemap が届かない・4xx / 5xx・
//     --max-body で切れた・URL にならない <loc> があるのも invalid、推測した /sitemap.xml の 5xx も invalid。
//     やり直しても 429 の sitemap は sitemap.rate-limited（warn）
//   - url-broken（4xx / 5xx、429 を除く。status、chain）、url-unreachable（reason）、url-rate-limited（retry_after_seconds）、
//     url-redirects（warn）、url-disallowed、url-external。found_on は sitemap（sitemap、loc）。同じ URL が複数の sitemap に載っていれば 1 件にまとまる
func Check(in Input) []finding.Finding {
	var out []finding.Finding
	if r := in.Robots; r != nil {
		switch r.State {
		case "missing":
			out = append(out, finding.New("sitemap.robots-missing", r.URL, fmt.Sprintf("%s answered %d; crawling as if there were no robots.txt (RFC 9309: unavailable means no restrictions)", r.URL, r.Status), map[string]any{}))
		case "broken":
			out = append(out, finding.New("sitemap.robots-broken", r.URL, fmt.Sprintf("%s answered %d; the crawl stopped at the start page (RFC 9309 treats a 5xx robots.txt as a complete disallow)", r.URL, r.Status), map[string]any{"status": r.Status}))
		case "unreachable":
			u := asUnreachable(r.Err)
			out = append(out, finding.New("sitemap.robots-unreachable", r.URL, fmt.Sprintf("%s %s; the crawl stopped at the start page", u.URL, u.Reason.Sentence(in.Timeout)), map[string]any{"reason": string(u.Reason)}))
		case "rate-limited":
			var ra any
			if r.RetryAfter != nil {
				ra = *r.RetryAfter
			}
			out = append(out, finding.New("sitemap.robots-rate-limited", r.URL, fmt.Sprintf("%s answered 429 and was not usable after the retry; the crawl stopped at the start page", r.URL), map[string]any{"retry_after_seconds": ra}))
		case "bad-redirect":
			why := "an unusable redirect counts as unreachable: RFC 9309 §2.3.1.2"
			if r.BadStop == "private" {
				why = "robots.txt is treated as unreachable, a complete disallow: RFC 9309 §2.3.1.4"
			}
			out = append(out, finding.New("sitemap.robots-unreachable", r.URL, fmt.Sprintf("%s %s; the crawl stopped at the start page (%s)", r.FirstURL, badRedirectSentence(r), why), map[string]any{"reason": "redirect"}))
		}
	}
	if in.Missing && len(in.Files) > 0 {
		f := in.Files[0]
		out = append(out, finding.New("sitemap.missing", f.URL, fmt.Sprintf("no sitemap was found: robots.txt names none and %s answered %d", f.URL, f.Status), map[string]any{}))
	}
	robotsUnusable := in.Robots != nil && in.Robots.State != "ok" && in.Robots.State != "missing"
	for _, f := range in.Files {
		switch {
		case f.Excluded:
		case f.RateLimited:
			var ra any
			if f.RetryAfter != nil {
				ra = *f.RetryAfter
			}
			out = append(out, finding.New("sitemap.rate-limited", f.URL, fmt.Sprintf("the sitemap %s answered 429 and was still rate-limiting after the retry, so it was not read (not a broken sitemap; try again later)", f.URL), map[string]any{"retry_after_seconds": ra}))
		case f.Err != nil:
			u := asUnreachable(f.Err)
			out = append(out, finding.New("sitemap.invalid", f.URL, fmt.Sprintf("the sitemap %s %s", u.URL, u.Reason.Sentence(in.Timeout)), map[string]any{"problem": "the sitemap could not be fetched (" + string(u.Reason) + ")"}))
		case f.Problem != "":
			out = append(out, finding.New("sitemap.invalid", f.URL, fmt.Sprintf("the sitemap %s could not be read: %s", f.URL, f.Problem), map[string]any{"problem": f.Problem}))
		case f.Status >= 400 && !in.Missing:
			// 宣言された sitemap（または index から辿った sitemap）が 4xx / 5xx: 宣言が壊れている。宣言が無くて /sitemap.xml が 4xx なのは sitemap.missing
			out = append(out, finding.New("sitemap.invalid", f.URL, fmt.Sprintf("the sitemap %s answered %d", f.URL, f.Status), map[string]any{"problem": fmt.Sprintf("the sitemap answered %d", f.Status)}))
		}
	}
	for _, e := range in.Entries {
		at := finding.InSitemap(e.Sitemap, e.Loc)
		switch {
		case e.URL == "", e.Excluded:
			continue // URL にならない <loc> は crawl が Entries に入れず、その sitemap の invalid の problem に数える。--exclude は数だけ
		case e.External:
			out = append(out, finding.New("sitemap.url-external", e.URL, fmt.Sprintf("%s is on another origin; it is listed in %s and was not fetched", e.URL, e.Sitemap), map[string]any{}, at))
		case e.Disallowed && robotsUnusable:
			// robots.txt が使えないときの全面禁止（RFC 9309）: Disallow の行があるのではない。そう言わない（CLAUDE.md §4-1）
			out = append(out, finding.New("sitemap.url-disallowed", e.URL, fmt.Sprintf("%s is listed in %s but was not fetched: robots.txt on this origin could not be used (see sitemap.robots-*), and RFC 9309 treats that as a complete disallow", e.URL, e.Sitemap), map[string]any{}, at))
		case e.Disallowed:
			out = append(out, finding.New("sitemap.url-disallowed", e.URL, fmt.Sprintf("%s is disallowed by robots.txt but listed in %s; it was not fetched", e.URL, e.Sitemap), map[string]any{}, at))
		case e.Err != nil:
			u := asUnreachable(e.Err)
			out = append(out, finding.New("sitemap.url-unreachable", e.URL, fmt.Sprintf("%s %s", u.URL, u.Reason.Sentence(in.Timeout)), map[string]any{"reason": string(u.Reason)}, at))
		case e.RateLimit != nil:
			var ra any
			if e.RateLimit.RetryAfterReadable {
				ra = int(e.RateLimit.RetryAfterAsked / time.Second)
			}
			out = append(out, finding.New("sitemap.url-rate-limited", e.URL, fmt.Sprintf("%s answered 429 and was still rate-limiting after the retry (not a broken URL; try again later)", e.URL), map[string]any{"retry_after_seconds": ra}, at))
		case e.Final && e.Status >= 400 && e.Status != 429:
			chain := e.Chain
			if len(chain) <= 1 {
				chain = []map[string]any{}
			}
			msg := fmt.Sprintf("%s returned %d", e.URL, e.Status)
			if len(e.Chain) > 1 {
				msg = fmt.Sprintf("%s redirects to %s, which returned %d", e.URL, e.Chain[len(e.Chain)-1]["url"], e.Status)
			}
			out = append(out, finding.New("sitemap.url-broken", e.URL, msg, map[string]any{"status": e.Status, "chain": chain}, at))
		case e.Redirected:
			out = append(out, finding.New("sitemap.url-redirects", e.URL, fmt.Sprintf("%s redirects (a sitemap should list the URL a page is served at)", e.URL), map[string]any{}, at))
		}
	}
	return finding.Merge(out)
}

// PrivateRedirectSentence は、robots.txt のリダイレクトの先がプライベートアドレスだったことの 1 文（URL の後ろに続く形）。
// ⚠ この文はここにだけ書く: cli の robotsWhy（skipped の robots-unavailable）もこれを呼ぶ（CLAUDE.md §3）。
func PrivateRedirectSentence(target string) string {
	return fmt.Sprintf("redirected to %s, a private address that tenken does not connect to", target)
}

// badRedirectSentence は、robots.txt のリダイレクトがどう使えなかったか（cli の robotsWhy と同じ 4 つ。文はここが持つ）。
func badRedirectSentence(r *Robots) string {
	switch r.BadStop {
	case "loop":
		return "redirected in a loop"
	case "bad-location":
		return "redirected to a Location that is not a usable URL"
	case "private":
		return PrivateRedirectSentence(r.PrivateURL)
	default:
		return fmt.Sprintf("redirected too many times (the last request was %s)", r.URL)
	}
}

// asUnreachable は fetch の到達不能。crawl は到達不能しか Err に入れないので、ほかが来たら実装の誤り（隠さず止まる）。
func asUnreachable(err error) *fetch.UnreachableError {
	var u *fetch.UnreachableError
	if errors.As(err, &u) {
		return u
	}
	panic("sitemap: unexpected error from crawl: " + err.Error())
}
