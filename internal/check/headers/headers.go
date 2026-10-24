// Package headers はセキュリティヘッダのチェック（docs/cli-reference.md §2.1 の headers、§2.2 の headers.* のルール）。
// 巡回したページの応答ヘッダを受け取って指摘を返すだけで、取りに行かない（docs/design.md §2）。
package headers

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/hidetzu/tenken/internal/finding"
)

// Page は巡回した 1 ページ（HTML の応答）。Header は正規化した名前 → 値の並び（fetch.Response.Header と同じ形）。
type Page struct {
	URL    string
	Header map[string][]string
}

// Options はしきい値（docs/cli-reference.md §3.2）。
type Options struct {
	HSTSMinAge int // --hsts-min-age（秒）
}

// Check はページの応答ヘッダについて headers.* の指摘を返す。
// subject はオリジン（サイトとして 1 件にまとめる。docs/cli-reference.md §5.2）、found_on はページごとの header の場所
// （見たヘッダの名前と、そのページで見た値。無ければ null）。detail は空。
func Check(pages []Page, o Options) []finding.Finding {
	var out []finding.Finding
	for _, p := range pages {
		origin := originOf(p.URL)
		if origin == "" {
			continue
		}
		add := func(rule, header, msg string) {
			out = append(out, finding.New(rule, origin, msg, map[string]any{}, finding.Header(p.URL, header, firstValue(p.Header, header))))
		}
		if strings.HasPrefix(origin, "https://") {
			// HSTS は https の応答でだけ見る（http では無効。RFC 6797 §8.1）。複数あれば最初のヘッダだけ（§8.1）
			switch v, ok := first(p.Header, "Strict-Transport-Security"); {
			case !ok:
				add("headers.hsts-missing", "Strict-Transport-Security", fmt.Sprintf("%s does not send Strict-Transport-Security", origin))
			default:
				age, why := maxAge(v)
				if why != "" {
					add("headers.hsts-weak", "Strict-Transport-Security", fmt.Sprintf("%s sends a Strict-Transport-Security header a browser would ignore: %s (RFC 6797 §6.1)", origin, why))
				} else if age < o.HSTSMinAge {
					add("headers.hsts-weak", "Strict-Transport-Security", fmt.Sprintf("%s sends Strict-Transport-Security with max-age=%d, below %d", origin, age, o.HSTSMinAge))
				}
			}
		}
		// X-Content-Type-Options は Fetch の "determine nosniff": 値を , で分け、最初の要素が nosniff（大文字小文字を区別しない）
		switch vs, ok := all(p.Header, "X-Content-Type-Options"); {
		case !ok:
			add("headers.nosniff-missing", "X-Content-Type-Options", fmt.Sprintf("%s does not send X-Content-Type-Options", origin))
		case !strings.EqualFold(firstListMember(vs), "nosniff"):
			add("headers.nosniff-invalid", "X-Content-Type-Options", fmt.Sprintf("%s sends X-Content-Type-Options: %s (a browser applies it only when the first value is nosniff)", origin, short(strings.Join(vs, ", "))))
		}
		// CSP は複数のヘッダも、1 つのヘッダの中の , 区切りも、すべてのポリシーが効く（CSP Level 3 §4.1.1）
		csps, hasCSP := all(p.Header, "Content-Security-Policy")
		if _, hasXFO := first(p.Header, "X-Frame-Options"); !hasXFO && !(hasCSP && anyPolicyHas(csps, "frame-ancestors")) {
			add("headers.framing-missing", "X-Frame-Options", fmt.Sprintf("%s sends neither X-Frame-Options nor a Content-Security-Policy with frame-ancestors", origin))
		}
		if !hasCSP {
			add("headers.csp-missing", "Content-Security-Policy", fmt.Sprintf("%s does not send Content-Security-Policy", origin))
		}
		if _, ok := first(p.Header, "Referrer-Policy"); !ok {
			add("headers.referrer-policy-missing", "Referrer-Policy", fmt.Sprintf("%s does not send Referrer-Policy", origin))
		}
	}
	return finding.Merge(out)
}

// all は、名前の一致するヘッダの値をすべて（送られた順に）。名前の大文字小文字は区別しない。
// ⚠ map を名前順に見る（同じ名前の綴り違いが複数あっても、結果が実行ごとに変わらない）。
func all(h map[string][]string, name string) ([]string, bool) {
	if vs, ok := h[name]; ok && len(vs) > 0 {
		return vs, true // fetch は名前を正規化して渡す。この行で足りる
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if strings.EqualFold(k, name) {
			out = append(out, h[k]...)
		}
	}
	return out, len(out) > 0
}

// first は、ヘッダの最初の値（同じヘッダが複数あれば最初のもの）。
func first(h map[string][]string, name string) (string, bool) {
	vs, ok := all(h, name)
	if !ok {
		return "", false
	}
	return vs[0], true
}

// firstValue は found_on の value（無ければ nil = JSON の null）。
func firstValue(h map[string][]string, name string) *string {
	if v, ok := first(h, name); ok {
		return &v
	}
	return nil
}

// firstListMember は、複数の値を , でつないだ並びの最初の要素（Fetch の "get, decode, and split"）。
func firstListMember(vs []string) string {
	joined := strings.Join(vs, ",")
	member, _, _ := strings.Cut(joined, ",")
	return strings.TrimSpace(member)
}

// maxAge は Strict-Transport-Security の max-age（RFC 6797 §6.1: 指示は ; 区切り、名前は大文字小文字を区別しない、
// 同じ指示は 1 回だけ。§6.1.1: 値は数字の列で、引用符で囲んでもよい）。§6.1 に合わないヘッダはブラウザが無視する（§8.1）ので、
// 合わなければ理由を返す。値に上限は無い（§6.1.1）: int64 に収まらない数は「十分に長い」として読む。
func maxAge(v string) (age int, why string) {
	seen := map[string]bool{}
	found := false
	for _, d := range strings.Split(v, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name, val, hasValue := strings.Cut(d, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if seen[name] {
			return 0, fmt.Sprintf("the directive %s appears more than once", name)
		}
		seen[name] = true
		if name != "max-age" {
			continue
		}
		if !hasValue {
			return 0, "max-age has no value"
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		} else if strings.Contains(val, `"`) {
			return 0, "max-age has an unbalanced quote"
		}
		if val == "" || strings.Trim(val, "0123456789") != "" {
			return 0, fmt.Sprintf("max-age=%s is not a number of seconds", short(val))
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil || n > int64(^uint(0)>>1) {
			n = int64(^uint(0) >> 1) // 桁が多すぎて持てない: どんなしきい値よりも長い
		}
		found, age = true, int(n)
	}
	if !found {
		return 0, "max-age is missing"
	}
	return age, ""
}

// anyPolicyHas は、CSP の値（ヘッダごと、さらに , で区切ったポリシーごと）のどれかに指示があるか。
func anyPolicyHas(values []string, name string) bool {
	for _, v := range values {
		for _, policy := range strings.Split(v, ",") {
			if hasDirective(policy, name) {
				return true
			}
		}
	}
	return false
}

// hasDirective は 1 つのポリシーに指示（frame-ancestors など）があるか（指示は ; 区切り、名前は大文字小文字を区別しない）。
func hasDirective(csp, name string) bool {
	for _, d := range strings.Split(csp, ";") {
		fields := strings.Fields(d)
		if len(fields) > 0 && strings.EqualFold(fields[0], name) {
			return true
		}
	}
	return false
}

// short は人に見せる値を短くする（巨大なヘッダをそのまま文に入れない。値そのものは found_on にある）。
func short(v string) string {
	if n := 0; len(v) > 60 {
		for i := range v { // 文字の境目で切る（バイトで切ると壊れた UTF-8 になる）
			if n++; n > 60 {
				return v[:i] + "…"
			}
		}
	}
	return v
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
