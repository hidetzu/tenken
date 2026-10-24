package crawl

import (
	"net/url"
	"sort"
	"strings"
)

// Normalize は、同じ資源を指す URL を同じ文字列にする（docs/test-perspectives.md §2.2: 同じ URL を二度取りに行かない）。
//   - スキームとホストは小文字、既定のポート（http の 80、https の 443）は消す
//   - フラグメントは消す（要求に乗らない）
//   - 空のパスは "/"
//   - クエリは名前で並べ直す（並びが違うだけの URL を 1 つにする。値の意味は変えない）
//
// パスの大文字小文字と末尾のスラッシュは変えない: 多くのサーバーで別の資源になる。
// http(s) 以外、ホストの無い URL は "" を返す（取りに行かない）。
func Normalize(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && !((u.Scheme == "http" && p == "80") || (u.Scheme == "https" && p == "443")) {
		host += ":" + p
	}
	u.Host = host
	u.User = nil // 資格情報は送らない（fetch）ので、同一性にも入れない
	u.Fragment = ""
	u.RawFragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	if u.RawQuery != "" {
		u.RawQuery = sortedQuery(u.RawQuery)
	}
	return u.String()
}

// sortedQuery はクエリを「名前、値」の順に並べ直す。解釈できない部分はそのまま残す。
func sortedQuery(q string) string {
	parts := strings.Split(q, "&")
	sort.SliceStable(parts, func(i, j int) bool { return parts[i] < parts[j] })
	return strings.Join(parts, "&")
}

// Origin はスキーム・ホスト・ポートがすべて同じかを見るための鍵（WHATWG URL。docs/cli-reference.md §6）。
func Origin(normalized string) string {
	u, err := url.Parse(normalized)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Resolve は base に対して ref を解決し、正規化する。取りに行かないもの（mailto: など）は ""。
func Resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return Normalize(base.ResolveReference(r).String())
}
