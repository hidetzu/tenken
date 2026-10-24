package crawl

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func refList(refs []Ref) string {
	var out []string
	for _, r := range refs {
		out = append(out, fmt.Sprintf("%s[%s]=%s", r.Element, r.Attribute, r.URL))
	}
	return strings.Join(out, " ")
}

// docs/test-perspectives.md §3.1: a[href]、img[src]、img[srcset]、script[src]、link[href] をすべて拾う。
func TestExtractRefsFindsEveryKind(t *testing.T) {
	page, _ := url.Parse("https://a.test/dir/")
	body := []byte(`<!doctype html><html><head>
<link rel="stylesheet" href="/css/site.css">
<script src="app.js"></script>
</head><body>
<a href="../about">About</a>
<img src="pic.png" srcset="pic-1x.png 1x, pic-2x.png 2x">
<a href="mailto:x@a.test">mail</a><a href="tel:+81">tel</a><a href="javascript:void(0)">js</a><a href="#top">top</a>
<img src="data:image/png;base64,AAAA">
</body></html>`)
	got := refList(ExtractRefs(page, body))
	want := "link[href]=https://a.test/css/site.css script[src]=https://a.test/dir/app.js a[href]=https://a.test/about " +
		"img[src]=https://a.test/dir/pic.png img[srcset]=https://a.test/dir/pic-1x.png img[srcset]=https://a.test/dir/pic-2x.png " +
		"a[href]= a[href]= a[href]= a[href]=https://a.test/dir/ img[src]="
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	refs := ExtractRefs(page, body)
	if refs[0].Subresource != true || refs[2].Subresource != false || refs[2].Value != "../about" {
		t.Fatalf("subresource / value: %+v", refs[2])
	}
}

// <base href> は後の参照の基準になる（WHATWG HTML）。
func TestBaseHref(t *testing.T) {
	page, _ := url.Parse("https://a.test/dir/page")
	refs := ExtractRefs(page, []byte(`<head><base href="https://b.test/x/"></head><a href="rel">r</a><a href="/abs">a</a>`))
	if got := refList(refs); got != "a[href]=https://b.test/x/rel a[href]=https://b.test/abs" {
		t.Fatalf("got %s", got)
	}
}

// 壊れた HTML でも止まらず、拾えるものは拾う（docs/test-perspectives.md §3.1）。
func TestBrokenHTMLStillYieldsRefs(t *testing.T) {
	page, _ := url.Parse("https://a.test/")
	refs := ExtractRefs(page, []byte(`<a href="/one"><div><a href=/two>no quotes<img src='/three' src="/dup"><p><a href="/four`))
	got := refList(refs)
	if !strings.Contains(got, "/one") || !strings.Contains(got, "/two") || !strings.Contains(got, "img[src]=https://a.test/three") {
		t.Fatalf("got %s", got)
	}
	if strings.Contains(got, "/dup") {
		t.Fatalf("the first of duplicate attributes wins: %s", got)
	}
	if refs := ExtractRefs(page, []byte("not html at all \x00\xff")); len(refs) != 0 {
		t.Fatalf("garbage: %v", refs)
	}
	if refs := ExtractRefs(page, nil); len(refs) != 0 {
		t.Fatalf("empty: %v", refs)
	}
}

func TestAttributeNamesAreCaseInsensitive(t *testing.T) {
	page, _ := url.Parse("https://a.test/")
	if got := refList(ExtractRefs(page, []byte(`<A HREF="/x"><IMG SRC="/y">`))); got != "a[href]=https://a.test/x img[src]=https://a.test/y" {
		t.Fatalf("got %s", got)
	}
}

// docs/test-perspectives.md §3.1: link の rel=preconnect / dns-prefetch は資源ではないので拾わない。ほかの rel（stylesheet、icon、無し）は拾う。
func TestPreconnectAndDNSPrefetchLinksAreNotReferences(t *testing.T) {
	html := `<link rel="preconnect" href="https://cdn.test"><link rel="DNS-Prefetch" href="//fonts.test">
<link rel="preconnect stylesheet" href="/both.css"><link rel="stylesheet" href="/a.css"><link href="/norel.css"><link rel="icon" href="/i.png">`
	page, _ := url.Parse("https://a.test/")
	refs := ExtractRefs(page, []byte(html))
	var got []string
	for _, r := range refs {
		got = append(got, r.URL)
	}
	if want := "https://a.test/both.css https://a.test/a.css https://a.test/norel.css https://a.test/i.png"; strings.Join(got, " ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
}
