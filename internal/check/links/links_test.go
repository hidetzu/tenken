package links

import (
	"testing"

	"github.com/hidetzu/tenken/internal/finding"
)

// docs/cli-reference.md §4: 開始 URL（またはそのリダイレクト先）の 4xx / 5xx は links.broken。429 は除く（3 になる）。
func TestStartPageStatuses(t *testing.T) {
	for status, want := range map[int]bool{200: false, 301: false, 399: false, 400: true, 404: true, 410: true, 429: false, 500: true, 503: true} {
		fs := Start("https://a.test/", "https://a.test/", status, nil)
		if (len(fs) == 1) != want {
			t.Errorf("%d: got %d findings, want finding=%v", status, len(fs), want)
		}
	}
}

func TestBrokenStartCarriesStatusChainAndTheStartLocation(t *testing.T) {
	fs := Start("https://a.test/", "https://a.test/", 404, []map[string]any{{"url": "https://a.test/", "status": 404}})
	f := fs[0]
	if f.Rule != "links.broken" || f.Severity != finding.Fail || f.Subject != "https://a.test/" || f.Detail["status"] != 404 {
		t.Fatalf("%+v", f)
	}
	if chain, ok := f.Detail["chain"].([]map[string]any); !ok || len(chain) != 0 {
		t.Fatalf("chain without redirects must be an empty array: %v", f.Detail["chain"])
	}
	if len(f.FoundOn) != 1 || f.FoundOn[0].Kind != finding.KindStart || f.FoundOn[0].URL != "https://a.test/" {
		t.Fatalf("found_on = %+v", f.FoundOn)
	}
	// リダイレクトを経ていれば経路が入り、subject は壊れている URL、found_on は開始 URL
	chain := []map[string]any{{"url": "https://a.test/", "status": 301}, {"url": "https://a.test/old", "status": 410}}
	fs = Start("https://a.test/", "https://a.test/old", 410, chain)
	if fs[0].Subject != "https://a.test/old" || len(fs[0].Detail["chain"].([]map[string]any)) != 2 || fs[0].FoundOn[0].URL != "https://a.test/" {
		t.Fatalf("%+v", fs[0])
	}
}
