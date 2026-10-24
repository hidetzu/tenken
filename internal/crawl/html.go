package crawl

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Ref は HTML の中の参照（docs/test-perspectives.md §3.1 のリンクの種類）。
type Ref struct {
	Element   string // a、img、script、link
	Attribute string // href、src、srcset
	Value     string // 属性の値そのまま（found_on の value）
	URL       string // 解決して正規化した URL（取りに行かないものは ""）
	// Subresource は、ページの一部として読み込まれるもの（src / srcset / link[href]）。Mixed Content の対象（a[href] は対象外）
	Subresource bool
}

// ExtractRefs は HTML から参照を拾う。壊れた HTML でも止まらず、拾えるものは拾う（x/net/html は寛容）。
// <base href> があれば、それを基準に解決する（WHATWG HTML）。
// ⚠ 入力は敵対的でありうる: 本文は fetch が --max-body で切っている。ここは 1 回の走査だけ。
//
//	まったく同じ参照（URL・要素・属性・値が同じ）は 1 つにする: 同じリンクを何十万も並べたページでも、持つのは異なる参照だけ
//	（found_on にとって同じ場所なので、何も失わない）。数は数えない: 上限ではなく重複の除去。
func ExtractRefs(page *url.URL, body []byte) []Ref {
	base := page
	var refs []Ref
	seen := map[Ref]bool{}
	add := func(r Ref) {
		if !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return refs
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if !hasAttr {
				continue
			}
			tag := string(name)
			attrs := map[string]string{}
			for {
				k, v, more := z.TagAttr()
				key := strings.ToLower(string(k))
				if _, dup := attrs[key]; !dup { // 同じ属性が 2 つあれば最初のもの（HTML の規則）
					attrs[key] = string(v)
				}
				if !more {
					break
				}
			}
			switch tag {
			case "base":
				if href, ok := attrs["href"]; ok && base == page {
					if b, err := url.Parse(strings.TrimSpace(href)); err == nil {
						base = page.ResolveReference(b)
					}
				}
			case "a":
				if v, ok := attrs["href"]; ok {
					add(Ref{Element: "a", Attribute: "href", Value: v, URL: Resolve(base, v)})
				}
			case "img":
				if v, ok := attrs["src"]; ok {
					add(Ref{Element: "img", Attribute: "src", Value: v, URL: Resolve(base, v), Subresource: true})
				}
				if v, ok := attrs["srcset"]; ok {
					for _, cand := range srcsetURLs(v) {
						add(Ref{Element: "img", Attribute: "srcset", Value: v, URL: Resolve(base, cand), Subresource: true})
					}
				}
			case "script":
				if v, ok := attrs["src"]; ok {
					add(Ref{Element: "script", Attribute: "src", Value: v, URL: Resolve(base, v), Subresource: true})
				}
			case "link":
				// rel=preconnect / dns-prefetch はオリジンの名前で、資源ではない（そのパスが 404 でも壊れていない）
				if v, ok := attrs["href"]; ok && !originHint(attrs["rel"]) {
					add(Ref{Element: "link", Attribute: "href", Value: v, URL: Resolve(base, v), Subresource: true})
				}
			}
		}
	}
}

// originHint は、link の rel がオリジンへの前もっての接続（preconnect / dns-prefetch）だけを言っているか。
// rel は空白区切りの集合で大文字小文字を区別しない（WHATWG HTML）。stylesheet などほかの語が混ざっていれば資源として拾う。
func originHint(rel string) bool {
	fields := strings.Fields(strings.ToLower(rel))
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if f != "preconnect" && f != "dns-prefetch" {
			return false
		}
	}
	return true
}

// srcsetURLs は srcset の候補の URL（"a.jpg 1x, b.jpg 2x" → a.jpg、b.jpg。HTML の srcset の文法の、URL の部分だけ）。
func srcsetURLs(v string) []string {
	var out []string
	for _, cand := range strings.Split(v, ",") {
		fields := strings.Fields(cand)
		if len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out
}
