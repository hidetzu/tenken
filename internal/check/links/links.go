// Package links はリンク切れのチェック（docs/cli-reference.md §2.1 の links、§2.2 の links.* のルール）。
// 開始 URL（とそのリダイレクト先）の応答（Start）と、巡回で集めたリンクの結果（Check）、静的な Mixed Content（MixedContent）。
// 受け取った結果から指摘を返すだけで、取りに行かない（docs/design.md §2。取りに行くのは crawl）。
package links

import (
	"fmt"

	"github.com/hidetzu/tenken/internal/finding"
)

// Start は、開始 URL の鎖の最後の応答が 4xx / 5xx（429 を除く）なら links.broken を返す。
// chain は最初の要求から最後の応答までの経路（redirects.DetailChain の形）。リダイレクトを経ていなければ
// （応答が 1 つなら）detail.chain は空（docs/cli-reference.md §5.2）。それを決めるのはここだけ。
func Start(startURL, finalURL string, status int, chain []map[string]any) []finding.Finding {
	if status < 400 || status == 429 {
		return nil
	}
	if len(chain) <= 1 {
		chain = []map[string]any{}
	}
	msg := fmt.Sprintf("%s returned %d", finalURL, status)
	if finalURL != startURL {
		msg = fmt.Sprintf("%s redirects to %s, which returned %d", startURL, finalURL, status)
	}
	return []finding.Finding{finding.New("links.broken", finalURL, msg, map[string]any{"status": status, "chain": chain}, finding.Start(startURL))}
}
