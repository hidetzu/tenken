package links

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/finding"
)

// Link は、巡回で参照されていて取りに行った URL の結果（crawl.Link と同じ形。check は crawl を import しない。docs/design.md §2）。
type Link struct {
	URL       string
	External  bool             // 最後の応答のオリジンが開始 URL と別（401 / 403 を forbidden にするか broken にするかを分ける）
	Status    int              // 最後の応答のステータス（Unreachable でなく、鎖が最後まで辿れたとき）
	Final     bool             // 鎖がリダイレクトでない応答に着いた
	Chain     []map[string]any // 経路 [{url, status}]（最初の要求から最後の応答まで）
	Err       error            // *fetch.UnreachableError（届かなかった）
	RateLimit *RateLimit       // やり直しても 429 のとき（それ以外は nil）
	Locations []finding.Location
	// MoreLocations は、巡回が覚えなかった場所の数（0 でなければ指摘を Truncated にする。数は言わない）
	MoreLocations int
}

// RateLimit は 429 を受けたときの記録（crawl.Chain の同じ名前の値）。
type RateLimit struct {
	Retried            bool
	Waited             time.Duration
	RetryAfterRaw      string
	RetryAfterReadable bool
	RetryAfterAsked    time.Duration
}

// Options は文に要る設定。
type Options struct {
	Timeout      time.Duration // --timeout（届かなかった理由の文）
	MaxRetryWait time.Duration // 429 で待つ上限（crawl.MaxRetryWait）
}

// Check は巡回で確かめたリンクについて links.broken / links.unreachable / links.rate-limited を返す（docs/cli-reference.md §2.2）。
// 同じ URL は 1 件にまとまり、見つかった場所をすべて持つ（crawl が URL ごとに場所を集める）。
//   - broken: 4xx / 5xx（429 を除く）。subject は参照された URL（リダイレクトを経ていれば、経路を detail.chain に）
//   - unreachable: 届かない。reason は dns / connect / tls / timeout / malformed（応答は届いたが HTTP として読めない）
//   - rate-limited: やり直しても 429。retry_after_seconds は Retry-After が読めればその秒数、無ければ・読めなければ null
//
// リダイレクトの途中で止まった鎖（ループ、壊れた Location、段数の超過）は redirects のルールが言う。ここでは何も言わない。
func Check(links []Link, o Options) []finding.Finding {
	var out []finding.Finding
	for _, l := range links {
		switch {
		case l.Err != nil:
			u := asUnreachable(l.Err)
			out = append(out, finding.New("links.unreachable", l.URL,
				fmt.Sprintf("%s %s", u.URL, UnreachableSentence(u.Reason, o.Timeout)),
				map[string]any{"reason": string(u.Reason)}, l.Locations...))
		case l.RateLimit != nil:
			var ra any
			if l.RateLimit.RetryAfterReadable {
				ra = int(l.RateLimit.RetryAfterAsked / time.Second)
			}
			out = append(out, finding.New("links.rate-limited", l.URL,
				RateLimitedSentence(l.URL, *l.RateLimit, o.MaxRetryWait)+" (it is rate-limiting; not a broken link)",
				map[string]any{"retry_after_seconds": ra}, l.Locations...))
		case l.Final && l.Status >= 400 && l.Status != 429:
			chain := l.Chain
			if len(chain) <= 1 {
				chain = []map[string]any{}
			}
			msg := fmt.Sprintf("%s returned %d", l.URL, l.Status)
			if len(l.Chain) > 1 {
				msg = fmt.Sprintf("%s redirects to %s, which returned %d", l.URL, l.Chain[len(l.Chain)-1]["url"], l.Status)
			}
			rule := "links.broken"
			if l.External && (l.Status == 401 || l.Status == 403) {
				// 別オリジンが返した 401 / 403: 相手が拒んだ（bot 対策のことが多く、ブラウザでは開ける）。壊れているとは言わない。
				// リダイレクトで別オリジンへ出た先の 403 もここ（最後の応答のオリジンで決める）
				rule = "links.forbidden"
				msg += " (the other site refused the request; sites that block automated clients answer this way, and the page may open in a browser)"
			}
			out = append(out, finding.New(rule, l.URL, msg, map[string]any{"status": l.Status, "chain": chain}, l.Locations...))
		}
	}
	for i := range out {
		for _, l := range links {
			if l.URL == out[i].Subject && l.MoreLocations > 0 {
				out[i].Truncated = true
			}
		}
	}
	finding.Sort(out)
	return out
}

// asUnreachable は fetch の到達不能。crawl は到達不能しか Err に入れないので、ほかが来たら実装の誤り（隠さず止まる）。
func asUnreachable(err error) *fetch.UnreachableError {
	var u *fetch.UnreachableError
	if !errors.As(err, &u) {
		panic("links: unexpected error from crawl: " + err.Error())
	}
	return u
}

// UnreachableSentence は fetch.Reason.Sentence（文は fetch が 1 か所で持つ）。
func UnreachableSentence(reason fetch.Reason, timeout time.Duration) string {
	return reason.Sentence(timeout)
}

// RateLimitedSentence は、429 について観測したことだけを言う（evidence.md）: 何回要求したか、待ったならどれだけ、
// 相手の Retry-After は何だったか。開始 URL（cli）と巡回のリンクで同じ文。
func RateLimitedSentence(u string, r RateLimit, maxWait time.Duration) string {
	switch {
	case r.Retried && r.RetryAfterRaw == "":
		return fmt.Sprintf("%s answered 429 twice (the second time after waiting %s; it sent no Retry-After)", u, r.Waited)
	case r.Retried && !r.RetryAfterReadable:
		return fmt.Sprintf("%s answered 429 twice (the second time after waiting %s; its Retry-After %q could not be read as seconds or a date)", u, r.Waited, r.RetryAfterRaw)
	case r.Retried:
		return fmt.Sprintf("%s answered 429 twice (the second time after waiting %s, as its Retry-After asked)", u, r.Waited)
	default:
		return fmt.Sprintf("%s answered 429 with Retry-After %q, longer than tenken waits (%s), so it was not asked again", u, r.RetryAfterRaw, maxWait)
	}
}

// PageRef は、ページの中の 1 つの参照（Mixed Content の判定に要るものだけ）。
type PageRef struct {
	Page        string // ページの URL
	URL         string // 参照先（正規化済み）
	Element     string
	Attribute   string
	Value       string
	Subresource bool // src / srcset / link[href]。a[href] は対象外
}

// MixedContent は、https のページが http:// のサブリソースを参照していれば links.mixed-content を返す
// （docs/cli-reference.md §2.2、TEST-PERSPECTIVES §3.1: 静的な Mixed Content。subject は http:// のリソース）。取りに行かずに分かる。
func MixedContent(refs []PageRef) []finding.Finding {
	var out []finding.Finding
	for _, r := range refs {
		if !r.Subresource || !strings.HasPrefix(r.Page, "https://") || !strings.HasPrefix(r.URL, "http://") {
			continue
		}
		out = append(out, finding.New("links.mixed-content", r.URL,
			fmt.Sprintf("%s is loaded over http by an https page (mixed content)", r.URL),
			map[string]any{}, finding.Element(r.Page, r.Element, r.Attribute, r.Value)))
	}
	return finding.Merge(out)
}
