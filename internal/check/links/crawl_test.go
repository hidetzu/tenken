package links

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hidetzu/tenken/internal/fetch"
	"github.com/hidetzu/tenken/internal/finding"
)

func ids(fs []finding.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule+"@"+f.Subject)
	}
	return strings.Join(out, " ")
}

func loc(page string) finding.Location { return finding.Element(page, "a", "href", "/x") }

// docs/test-perspectives.md §3.1: 200 は問題なし、404 / 410 / 500 / 503 は失敗、到達不能は理由を区別、429 は警告。
func TestCrawledLinks(t *testing.T) {
	links := []Link{
		{URL: "https://a.test/ok", Status: 200, Final: true, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/gone", Status: 410, Final: true, Chain: []map[string]any{{"url": "https://a.test/gone", "status": 410}}, Locations: []finding.Location{loc("https://a.test/"), loc("https://a.test/p2")}},
		{URL: "https://a.test/moved", Status: 404, Final: true, Chain: []map[string]any{{"url": "https://a.test/moved", "status": 301}, {"url": "https://a.test/new", "status": 404}}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://b.test/", Err: &fetch.UnreachableError{URL: "https://b.test/", Reason: fetch.ReasonDNS, Err: errors.New("x")}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/slow", Err: &fetch.UnreachableError{URL: "https://a.test/slow", Reason: fetch.ReasonTimeout, Err: errors.New("x")}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/busy", RateLimit: &RateLimit{Retried: true, Waited: 7 * time.Second, RetryAfterRaw: "7", RetryAfterReadable: true, RetryAfterAsked: 7 * time.Second}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/busy2", RateLimit: &RateLimit{Retried: true, Waited: time.Second}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/busy3", RateLimit: &RateLimit{Retried: false, RetryAfterRaw: "60", RetryAfterReadable: true, RetryAfterAsked: 60 * time.Second}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/busy4", RateLimit: &RateLimit{Retried: true, Waited: time.Second, RetryAfterRaw: "soon"}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/loop", Final: false, Locations: []finding.Location{loc("https://a.test/")}}, // ループなどは redirects が言う
		{URL: "https://a.test/429once", Status: 200, Final: true},
		{URL: "https://a.test/429final", Status: 429, Final: true}, // 429 は壊れているとは言わない（docs/cli-reference.md §2.2）
	}
	fs := Check(links, Options{Timeout: 10 * time.Second, MaxRetryWait: 30 * time.Second})
	want := "links.broken@https://a.test/gone links.broken@https://a.test/moved links.unreachable@https://a.test/slow links.unreachable@https://b.test/ links.rate-limited@https://a.test/busy links.rate-limited@https://a.test/busy2 links.rate-limited@https://a.test/busy3 links.rate-limited@https://a.test/busy4"
	if ids(fs) != want {
		t.Fatalf("got  %s\nwant %s", ids(fs), want)
	}
	gone := fs[0]
	if gone.Detail["status"] != 410 || len(gone.Detail["chain"].([]map[string]any)) != 0 || len(gone.FoundOn) != 2 {
		t.Fatalf("gone: %+v", gone)
	}
	moved := fs[1]
	if len(moved.Detail["chain"].([]map[string]any)) != 2 || !strings.Contains(moved.Message, "redirects to https://a.test/new, which returned 404") {
		t.Fatalf("moved: %+v", moved)
	}
	if fs[2].Detail["reason"] != "timeout" || fs[3].Detail["reason"] != "dns" || !strings.Contains(fs[2].Message, "no answer within 10s") || !strings.Contains(fs[3].Message, "host name could not be resolved") {
		t.Fatalf("reasons: %v %q / %v %q", fs[2].Detail, fs[2].Message, fs[3].Detail, fs[3].Message)
	}
	// 429: 文は観測したことだけ（evidence.md）。retry_after_seconds は読めた Retry-After の秒数、無ければ・読めなければ null
	if fs[4].Detail["retry_after_seconds"] != 7 || !strings.Contains(fs[4].Message, "twice (the second time after waiting 7s, as its Retry-After asked)") || fs[4].Severity != finding.Warn {
		t.Fatalf("busy: %v %q", fs[4].Detail, fs[4].Message)
	}
	if fs[5].Detail["retry_after_seconds"] != nil || !strings.Contains(fs[5].Message, "it sent no Retry-After") {
		t.Fatalf("busy2: %v %q", fs[5].Detail, fs[5].Message)
	}
	if fs[6].Detail["retry_after_seconds"] != 60 || !strings.Contains(fs[6].Message, `with Retry-After "60", longer than tenken waits (30s), so it was not asked again`) || strings.Contains(fs[6].Message, "twice") {
		t.Fatalf("busy3: %v %q", fs[6].Detail, fs[6].Message)
	}
	if fs[7].Detail["retry_after_seconds"] != nil || !strings.Contains(fs[7].Message, `"soon" could not be read`) {
		t.Fatalf("busy4: %v %q", fs[7].Detail, fs[7].Message)
	}
	for _, f := range fs {
		if strings.Contains(f.Message, "x509") || strings.Contains(f.Message, "dial tcp") {
			t.Fatalf("library text in %q", f.Message)
		}
	}
}

// docs/test-perspectives.md §3.1: https のページにある http:// の参照は警告。src / srcset / link[href] だけ、a[href] は対象外。
func TestMixedContent(t *testing.T) {
	refs := []PageRef{
		{Page: "https://a.test/", URL: "http://cdn.test/a.js", Element: "script", Attribute: "src", Value: "http://cdn.test/a.js", Subresource: true},
		{Page: "https://a.test/p2", URL: "http://cdn.test/a.js", Element: "script", Attribute: "src", Value: "http://cdn.test/a.js", Subresource: true},
		{Page: "https://a.test/", URL: "http://cdn.test/img.png", Element: "img", Attribute: "srcset", Value: "http://cdn.test/img.png 1x", Subresource: true},
		{Page: "https://a.test/", URL: "http://other.test/", Element: "a", Attribute: "href", Value: "http://other.test/", Subresource: false},
		{Page: "http://a.test/", URL: "http://cdn.test/b.js", Element: "script", Attribute: "src", Value: "http://cdn.test/b.js", Subresource: true},
		{Page: "https://a.test/", URL: "https://cdn.test/c.js", Element: "script", Attribute: "src", Value: "https://cdn.test/c.js", Subresource: true},
	}
	fs := MixedContent(refs)
	if ids(fs) != "links.mixed-content@http://cdn.test/a.js links.mixed-content@http://cdn.test/img.png" {
		t.Fatalf("got %s", ids(fs))
	}
	if len(fs[0].FoundOn) != 2 || fs[0].FoundOn[0].Kind != finding.KindElement || fs[0].Severity != finding.Warn {
		t.Fatalf("%+v", fs[0])
	}
}

// docs/test-perspectives.md §3.1: 別オリジンの 401 / 403 は links.forbidden（warn、status と chain）。同じオリジンの 403 と、別オリジンの 404 は links.broken。
func TestExternal401And403AreForbiddenNotBroken(t *testing.T) {
	links := []Link{
		{URL: "https://m.test/post", External: true, Status: 403, Final: true, Chain: []map[string]any{{"url": "https://m.test/post", "status": 403}}, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://m.test/auth", External: true, Status: 401, Final: true, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://m.test/gone", External: true, Status: 404, Final: true, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/private", Status: 403, Final: true, Locations: []finding.Location{loc("https://a.test/")}},
		{URL: "https://a.test/via", External: true, Status: 403, Final: true, Chain: []map[string]any{{"url": "https://a.test/via", "status": 302}, {"url": "https://m.test/x", "status": 403}}, Locations: []finding.Location{loc("https://a.test/")}}, // 別オリジンへ出た先の 403: External は最後の応答で決まる（cli）
		{URL: "https://m.test/back", External: false, Status: 403, Final: true, Chain: []map[string]any{{"url": "https://m.test/back", "status": 302}, {"url": "https://a.test/mine", "status": 403}}, Locations: []finding.Location{loc("https://a.test/")}},
	}
	fs := Check(links, Options{Timeout: 10 * time.Second, MaxRetryWait: 30 * time.Second})
	var got []string
	for _, f := range fs {
		got = append(got, f.Rule+"@"+f.Subject)
	}
	want := "links.broken@https://a.test/private links.broken@https://m.test/back links.broken@https://m.test/gone links.forbidden@https://a.test/via links.forbidden@https://m.test/auth links.forbidden@https://m.test/post"
	if strings.Join(got, " ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
	for _, f := range fs {
		if f.Rule == "links.forbidden" {
			if f.Severity != finding.Warn || f.Detail["status"] == nil || f.Detail["chain"] == nil || !strings.Contains(f.Message, "refused") {
				t.Fatalf("%+v", f)
			}
		}
	}
}
