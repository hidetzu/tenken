package crawl

import (
	"net/url"
	"testing"
)

// docs/test-perspectives.md §2.2: 同じ URL を二度取りに行かない（正規化）。
func TestNormalize(t *testing.T) {
	for raw, want := range map[string]string{
		"HTTPS://Example.COM":             "https://example.com/",
		"https://example.com:443/a":       "https://example.com/a",
		"http://example.com:80/a":         "http://example.com/a",
		"https://example.com:8443/a":      "https://example.com:8443/a",
		"https://example.com/a#frag":      "https://example.com/a",
		"https://example.com/a?b=2&a=1":   "https://example.com/a?a=1&b=2",
		"https://example.com/A/":          "https://example.com/A/", // パスの大文字小文字と末尾のスラッシュは変えない
		"https://example.com/A":           "https://example.com/A",
		"https://user:pw@example.com/":    "https://example.com/",
		"https://[2001:DB8::1]:8443/x":    "https://[2001:db8::1]:8443/x",
		"https://example.com/%E3%81%82":   "https://example.com/%E3%81%82",   // パーセントエンコードを壊さない
		"https://xn--r8jz45g.xn--zckzah/": "https://xn--r8jz45g.xn--zckzah/", // 国際化ドメイン名（punycode）を壊さない
		"mailto:x@example.com":            "",
		"tel:+81":                         "",
		"javascript:alert(1)":             "",
		"data:text/plain,hi":              "",
		"ftp://example.com/":              "",
		"https:///nohost":                 "",
		"":                                "",
		"  https://example.com/space  ":   "https://example.com/space",
		"https://example.com/a?":          "https://example.com/a?",
	} {
		if got := Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestResolveAgainstABase(t *testing.T) {
	base, _ := url.Parse("https://example.com/dir/page.html")
	for ref, want := range map[string]string{
		"other.html":          "https://example.com/dir/other.html",
		"../up":               "https://example.com/up",
		"/root":               "https://example.com/root",
		"//cdn.example.net/x": "https://cdn.example.net/x",
		"?q=1":                "https://example.com/dir/page.html?q=1",
		"#top":                "https://example.com/dir/page.html",
		"mailto:a@b":          "",
		"":                    "",
	} {
		if got := Resolve(base, ref); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", ref, got, want)
		}
	}
}

// オリジンはスキーム・ホスト・ポートがすべて同じ（docs/cli-reference.md §6、TEST-PERSPECTIVES §2.2）。
func TestOrigin(t *testing.T) {
	same := Origin(Normalize("https://example.com/a"))
	for raw, want := range map[string]bool{
		"https://example.com/b":     true,
		"https://example.com:443/c": true,
		"http://example.com/":       false, // スキームが違う
		"https://www.example.com/":  false, // サブドメインは外部
		"https://example.com:8443/": false, // ポートが違う
		"https://EXAMPLE.com/":      true,
	} {
		if got := Origin(Normalize(raw)) == same; got != want {
			t.Errorf("%s same origin as https://example.com/a: %v, want %v", raw, got, want)
		}
	}
}
