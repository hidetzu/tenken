// Package redirects はリダイレクトのチェック（docs/cli-reference.md §2.1 の redirects、§2.2 の redirects.* のルール）。
// 辿った鎖を受け取って指摘を返すだけで、取りに行かない（docs/design.md §2。辿るのは crawl）。
package redirects

import (
	"fmt"
	"net/url"

	"github.com/hidetzu/tenken/internal/finding"
)

// Hop はリダイレクトの鎖の 1 往復（crawl.Hop と同じ形。check は crawl を import しない）。
type Hop struct {
	URL      string
	Status   int
	Location string // Location ヘッダの値そのまま
	Next     string // Location を解決した URL。解決できなければ ""
}

// Stop は鎖の終わり方（crawl.Stop の値と同じ文字列）。
type Stop string

const (
	StopFinal       Stop = "final"
	StopLoop        Stop = "loop"
	StopBadLocation Stop = "bad-location"
	StopTooMany     Stop = "too-many"
	StopRateLimited Stop = "rate-limited"
	StopPrivate     Stop = "private" // 行き先がプライベートアドレスで止めた: 鎖の指摘はそこまで。ルールは無い（docs/cli-reference.md §5.2 の private が数える）
	// StopOverLimits は --max-duration / --max-requests で止めた鎖。観測しきっていないので cli はここへ渡さない（docs/cli-reference.md §5.2 の over_limits）
	StopOverLimits Stop = "over-limits"
)

// Chain は辿った鎖。
type Chain struct {
	Hops []Hop
	Stop Stop
}

// Check は鎖について redirects.* の指摘を返す。at は鎖を辿るきっかけ（開始 URL なら finding.Start、tenken が確かめに行った
// URL なら finding.Probe）。maxRedirects は --max-redirects。
//   - too-many: 段数が maxRedirects を超えた（鎖の最初の URL について）
//   - loop: 同じ URL に戻った（戻った先の URL について）
//   - downgrade: https の URL が http へ飛ばす（その URL について）
//   - temporary: 302 / 307 を返した URL について（1 つずつ）
//   - bad-location: リダイレクトなのに Location が無い・解決できない URL について
func Check(c Chain, maxRedirects int, at ...finding.Location) []finding.Finding {
	if len(c.Hops) == 0 {
		return nil
	}
	var out []finding.Finding
	chain := DetailChain(c)
	add := func(rule, subject, msg string) {
		out = append(out, finding.New(rule, subject, msg, map[string]any{"chain": chain}, at...))
	}
	for _, h := range c.Hops {
		if h.Next == "" {
			continue // リダイレクトでない、または bad-location（下で見る）
		}
		if h.Status == 302 || h.Status == 307 {
			add("redirects.temporary", h.URL, fmt.Sprintf("%s → %d → %s (a temporary redirect; make it 301 or 308 if it is meant to stay)", h.URL, h.Status, h.Next))
		}
		if scheme(h.URL) == "https" && scheme(h.Next) == "http" {
			add("redirects.downgrade", h.URL, fmt.Sprintf("%s → %d → %s (https redirects to http)", h.URL, h.Status, h.Next))
		}
	}
	last := c.Hops[len(c.Hops)-1]
	switch c.Stop {
	case StopTooMany:
		first := c.Hops[0]
		add("redirects.too-many", first.URL, fmt.Sprintf("%s redirects more than %d times (%d so far, still redirecting)", first.URL, maxRedirects, len(c.Hops)))
	case StopLoop:
		add("redirects.loop", last.Next, fmt.Sprintf("%s redirects back to %s (a loop)", last.URL, last.Next))
	case StopBadLocation:
		if last.Location == "" {
			add("redirects.bad-location", last.URL, fmt.Sprintf("%s answers %d without a Location header", last.URL, last.Status))
		} else {
			add("redirects.bad-location", last.URL, fmt.Sprintf("%s answers %d with a Location that is not a usable URL: %q", last.URL, last.Status, last.Location))
		}
	}
	finding.Sort(out)
	return out
}

// ReachesHTTPS は、鎖のどこかで https に着く（飛ぶ）か。
func ReachesHTTPS(c Chain) bool {
	for _, h := range c.Hops {
		if scheme(h.URL) == "https" || scheme(h.Next) == "https" {
			return true
		}
	}
	return false
}

// NoHTTPS は、http:// の URL から辿った鎖について、https に飛ばなければ指摘を返す（docs/cli-reference.md §2.1、§2.2 の redirects.no-https）。
// 鎖のどこかで https に着けば飛んでいる。at は、開始 URL が https のときに同じホストの http:// を 1 回確かめたなら finding.Probe、
// 開始 URL 自身が http ならその finding.Start。
func NoHTTPS(c Chain, probe string, at finding.Location) []finding.Finding {
	if len(c.Hops) == 0 || ReachesHTTPS(c) {
		return nil
	}
	return []finding.Finding{finding.New("redirects.no-https", probe,
		fmt.Sprintf("%s does not redirect to https (it answers %d on http)", probe, c.Hops[len(c.Hops)-1].Status),
		map[string]any{"chain": DetailChain(c)}, at)}
}

// DetailChain は detail.chain: [{url, status}]（docs/cli-reference.md §5.2）。最初の要求から最後の応答まで。
func DetailChain(c Chain) []map[string]any {
	out := make([]map[string]any, 0, len(c.Hops))
	for _, h := range c.Hops {
		out = append(out, map[string]any{"url": h.URL, "status": h.Status})
	}
	return out
}

func scheme(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme
}
