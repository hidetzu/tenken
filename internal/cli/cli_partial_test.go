package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// docs/test-perspectives.md §2.1（「the exit code is decided by the findings alone」）: 点検が途中までだったとき、終了コードは変えず（0 は点検した範囲で --fail-on に届く指摘が無い）、
// 範囲は text と JSON の両方に残る（docs/cli-reference.md §4）。
// 上限（--max-pages、--max-duration、--max-requests）に当たった、robots.txt が使えない、開始 URL に届かない、のそれぞれ。
func TestAPartialRunKeepsItsRangeInTextAndJSON(t *testing.T) {
	links := map[string]http.HandlerFunc{
		"/":           page(`<a href="/a">a</a><a href="/b">b</a><a href="/c">c</a>`),
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow:\n") },
		"/a":          page(`<a href="/a2">a2</a>`),
		"/a2":         page("x"),
		"/b":          page("x"),
		"/c":          page("x"),
	}
	type result struct {
		code     int
		text     string
		json     map[string]any
		textCode int
	}
	runBoth := func(h *harness, d deps, args ...string) result {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(append([]string{"--format", "json"}, args...), &out, &errOut, d)
		m := decode(t, out.String())
		out.Reset()
		textCode := run(args, &out, &errOut, d)
		return result{code, out.String(), m, textCode}
	}
	cases := []struct {
		name      string
		args      []string
		clocked   bool   // --max-duration: 寝た分だけ進む時計
		limit     string // limits_hit に入る名前（"" なら無し）
		skipped   string // skipped に入る "<check> (<code>)"（"" なら無し）
		header    string // 1 行目の limits の部分
		robots500 bool
	}{
		{name: "max-pages", args: []string{"--only", "links", "--max-pages", "2"}, limit: "max-pages", header: "limits: max-depth 3, max-pages 2, max-duration 10m, max-requests none"},
		{name: "max-requests", args: []string{"--only", "links", "--max-requests", "3"}, limit: "max-requests", header: "max-requests 3"},
		{name: "max-duration", args: []string{"--only", "links", "--delay", "600ms", "--max-duration", "1s"}, clocked: true, limit: "max-duration", header: "max-duration 1s"},
		{name: "robots.txt unavailable", args: []string{"--only", "links"}, robots500: true, skipped: "links (robots-unavailable)", header: "max-duration 10m"},
	}
	for _, c := range cases {
		h := newHarness(t)
		handlers := map[string]http.HandlerFunc{}
		for k, v := range links {
			handlers[k] = v
		}
		if c.robots500 {
			handlers["/robots.txt"] = statusHandler(500).ServeHTTP
		}
		site, _ := h.limitedSite(handlers)
		d := h.deps()
		if c.clocked {
			var mu sync.Mutex
			clock := now
			d.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
			d.sleep = func(s time.Duration) { mu.Lock(); clock = clock.Add(s); mu.Unlock() }
		}
		r := runBoth(h, d, append(c.args, site)...)
		s := summaryOf(r.json)
		if r.code != ExitOK || r.textCode != ExitOK {
			t.Errorf("%s: exit %d (json) / %d (text), want 0: a partial run does not change the exit code", c.name, r.code, r.textCode)
		}
		lh := fmt.Sprint(s["limits_hit"])
		if c.limit != "" && !strings.Contains(lh, c.limit) || !strings.Contains(r.text, "limits hit: ") || c.limit != "" && !strings.Contains(r.text, "limits hit: "+c.limit) {
			t.Errorf("%s: limits_hit %s, text:\n%s", c.name, lh, r.text)
		}
		var sk []string
		for _, x := range r.json["skipped"].([]any) {
			m := x.(map[string]any)
			sk = append(sk, fmt.Sprintf("%s (%s)", m["check"], m["code"]))
		}
		if c.skipped != "" && (!strings.Contains(strings.Join(sk, ", "), c.skipped) || !strings.Contains(r.text, "skipped: ") || !strings.Contains(r.text, c.skipped)) {
			t.Errorf("%s: skipped %v, text:\n%s", c.name, sk, r.text)
		}
		if first := strings.SplitN(r.text, "\n", 2)[0]; !strings.Contains(first, c.header) {
			t.Errorf("%s: first line %q, want it to contain %q", c.name, first, c.header)
		}
		nf, ok := s["not_fetched"].(map[string]any)
		if !strings.Contains(r.text, "not fetched: ") || !ok || len(nf) == 0 {
			t.Errorf("%s: not_fetched %v, text:\n%s", c.name, s["not_fetched"], r.text)
		}
		if _, ok := s["limits_hit"].([]any); !ok {
			t.Errorf("%s: limits_hit %v is not a list", c.name, s["limits_hit"])
		}
		if c.robots500 && nf["robots_unavailable"] == 0.0 {
			t.Errorf("%s: not_fetched.robots_unavailable 0: the pages not followed must be counted", c.name)
		}
	}

	// 開始 URL に届かない: 3。JSON は could_not_check で理由を言い、text は標準出力に何も出さず標準エラーに 1 文
	h := newHarness(t)
	unreachable := "https://" + closedPort(t) + "/"
	var out, errOut bytes.Buffer
	code := run([]string{"--only", "links", "--format", "json", unreachable}, &out, &errOut, h.deps())
	m := decode(t, out.String())
	if code != ExitCouldNotCheck || m["could_not_check"] == nil {
		t.Errorf("unreachable (json): code %d\n%s", code, out.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"--only", "links", unreachable}, &out, &errOut, h.deps()); code != ExitCouldNotCheck || out.String() != "" || !strings.Contains(errOut.String(), "could not check") {
		t.Errorf("unreachable (text): code %d stdout %q stderr %q", code, out.String(), errOut.String())
	}
}
