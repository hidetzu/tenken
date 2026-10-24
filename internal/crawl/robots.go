package crawl

import (
	"strconv"
	"strings"
	"time"
)

// MaxRobotsBytes は robots.txt の読む長さの上限（RFC 9309 §2.5 の 500 KiB）。
const MaxRobotsBytes = 500 * 1024

// MaxCrawlDelay は受け付ける Crawl-delay の上限。これ以上（や 0 以下、読めない値）は無視する（1 時間待つ巡回は CI では意味が無い）。
const MaxCrawlDelay = time.Hour

// Robots は robots.txt の、tenken に当てはまるグループ（RFC 9309）。
type Robots struct {
	rules      []robotsRule
	crawlDelay time.Duration
	sitemaps   []string
	matched    bool // 名前に一致するグループがあった（無ければ * のグループ、それも無ければ制限なし）
}

type robotsRule struct {
	allow   bool
	pattern string
}

// ParseRobots は robots.txt を読み、product（User-Agent の製品名。docs/cli-reference.md §3.5）にいちばん具体的に一致する
// グループの規則を返す。一致が無ければ `*` のグループ（RFC 9309 §2.2.1）。
// ⚠ 壊れた行は読み飛ばす（§2.2.4: 解釈できない行は無視する）。入力は敵対的でありうる: 行の数に比例する以上の仕事をしない。
func ParseRobots(body []byte, product string) *Robots {
	if len(body) > MaxRobotsBytes {
		body = body[:MaxRobotsBytes] // RFC 9309 §2.5: 500 KiB までを読めばよい。それより後ろは見ない（敵対的な大きさ）
	}
	product = strings.ToLower(product)
	type group struct {
		agents []string
		rules  []robotsRule
		delay  time.Duration
	}
	var groups []*group
	var cur *group
	var sitemaps []string
	lastWasAgent := false
	for _, line := range strings.Split(string(body), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field, value = strings.ToLower(strings.TrimSpace(field)), strings.TrimSpace(value)
		switch field {
		case "user-agent":
			// 連続する user-agent 行は 1 つのグループ（RFC 9309 §2.1）
			if cur == nil || !lastWasAgent {
				cur = &group{}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(value))
			lastWasAgent = true
			continue
		case "allow", "disallow":
			if cur != nil {
				cur.rules = append(cur.rules, robotsRule{allow: field == "allow", pattern: value})
			}
		case "crawl-delay":
			if cur != nil {
				if secs, err := strconv.ParseFloat(value, 64); err == nil && secs > 0 && secs < MaxCrawlDelay.Seconds() {
					cur.delay = time.Duration(secs * float64(time.Second))
				}
			}
		case "sitemap":
			if value != "" {
				sitemaps = append(sitemaps, value)
			}
		}
		lastWasAgent = false
	}
	r := &Robots{sitemaps: sitemaps}
	// いちばん具体的に一致するグループ: 製品名がエージェント名で始まる（または同じ）もののうち、いちばん長い名前。
	// 同じ名前（同じ長さで一致する名前）のグループが複数あれば規則を合わせる（RFC 9309 §2.2.1）。無ければ * のグループ（複数なら合わせる）
	bestLen := -1
	for _, g := range groups {
		for _, a := range g.agents {
			if a != "*" && a != "" && strings.HasPrefix(product, a) && len(a) > bestLen {
				bestLen = len(a)
			}
		}
	}
	var chosen []*group
	for _, g := range groups {
		for _, a := range g.agents {
			if (bestLen >= 0 && a != "*" && strings.HasPrefix(product, a) && len(a) == bestLen) || (bestLen < 0 && a == "*") {
				chosen = append(chosen, g)
				break
			}
		}
	}
	r.matched = bestLen >= 0
	for _, g := range chosen {
		r.rules = append(r.rules, g.rules...)
		if g.delay > r.crawlDelay {
			r.crawlDelay = g.delay
		}
	}
	return r
}

// Allowed は、パス（クエリを含む）を取りに行ってよいか（RFC 9309 §2.2.2: いちばん長く一致する規則が勝ち、
// 同じ長さなら allow が勝つ）。規則が無ければ許可。
func (r *Robots) Allowed(pathAndQuery string) bool {
	if r == nil {
		return true
	}
	if pathAndQuery == "" {
		pathAndQuery = "/"
	}
	bestLen := -1
	allowed := true
	for _, rule := range r.rules {
		if rule.pattern == "" {
			continue // 空の Disallow は「すべて許可」、空の Allow は意味を持たない
		}
		if !patternMatch(rule.pattern, pathAndQuery) {
			continue
		}
		l := len(rule.pattern)
		if l > bestLen || (l == bestLen && rule.allow) {
			bestLen, allowed = l, rule.allow
		}
	}
	return allowed
}

// CrawlDelay は Crawl-delay（RFC 9309 には無いが広く使われる）。無ければ 0。
func (r *Robots) CrawlDelay() time.Duration {
	if r == nil {
		return 0
	}
	return r.crawlDelay
}

// Sitemaps は Sitemap: の行（グループに関係なく全体）。
func (r *Robots) Sitemaps() []string {
	if r == nil {
		return nil
	}
	return r.sitemaps
}

// patternMatch は RFC 9309 §2.2.3 のパターン（`*` は任意の列、末尾の `$` は終わり）。
// ⚠ 入力は敵対的でありうる: `*` が何個あっても、パターンの長さ × パスの長さを超える仕事をしない（貪欲でない単純な探索）。
func patternMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	for _, part := range parts[1:] {
		i := strings.Index(path[pos:], part)
		if i < 0 {
			return false
		}
		pos += i + len(part)
	}
	if anchored {
		// 最後の部分が終わりで一致しなければならない。`*` の後ろなら、末尾に合わせ直す
		last := parts[len(parts)-1]
		if len(parts) == 1 {
			return pos == len(path)
		}
		return strings.HasSuffix(path, last) && len(path)-len(last) >= pos-len(last)
	}
	return true
}
