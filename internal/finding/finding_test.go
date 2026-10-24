package finding

import (
	"encoding/json"
	"strings"
	"testing"
)

func str(s string) *string { return &s }

func TestLocationJSONHasOnlyItsKindsKeys(t *testing.T) {
	cases := []struct {
		loc  Location
		want string
	}{
		{Element("https://e.test/", "a", "href", "/x"), `{"kind":"element","page":"https://e.test/","element":"a","attribute":"href","value":"/x"}`},
		{Header("https://e.test/", "Strict-Transport-Security", nil), `{"kind":"header","page":"https://e.test/","header":"Strict-Transport-Security","value":null}`},
		{Header("https://e.test/", "X-Content-Type-Options", str("sniff")), `{"kind":"header","page":"https://e.test/","header":"X-Content-Type-Options","value":"sniff"}`},
		{InSitemap("https://e.test/sitemap.xml", "https://e.test/a"), `{"kind":"sitemap","sitemap":"https://e.test/sitemap.xml","loc":"https://e.test/a"}`},
		{Start("https://e.test/"), `{"kind":"start","url":"https://e.test/"}`},
		{Probe("http://e.test/"), `{"kind":"probe","url":"http://e.test/"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.loc)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.loc.Kind, b, c.want)
		}
	}
}

func TestEmptyDetailAndFoundOnAreNotNull(t *testing.T) {
	b, err := json.Marshal(New("cert.expired", "e.test:443", "expired", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"detail":{}`, `"found_on":[]`, `"severity":"fail"`, `"check":"cert"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s does not contain %s", b, want)
		}
	}
}

func TestMergeKeysOnRuleAndSubject(t *testing.T) {
	a := New("links.broken", "https://e.test/old", "404", map[string]any{"status": 404}, Element("https://e.test/", "a", "href", "/old"))
	b := New("links.broken", "https://e.test/old", "404", map[string]any{"status": 404}, Element("https://e.test/about", "a", "href", "/old"))
	dup := New("links.broken", "https://e.test/old", "404", nil, Element("https://e.test/", "a", "href", "/old"))
	other := New("links.broken", "https://e.test/gone", "410", nil)
	got := Merge([]Finding{b, a, dup, other})
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2 (one per rule+subject)", len(got))
	}
	old := got[1]
	if old.Subject != "https://e.test/old" {
		old = got[0]
	}
	if len(old.FoundOn) != 2 {
		t.Fatalf("found_on has %d places, want 2 (the duplicate collapses)", len(old.FoundOn))
	}
	if old.FoundOn[0].Page != "https://e.test/" || old.FoundOn[1].Page != "https://e.test/about" {
		t.Fatalf("found_on is not sorted: %+v", old.FoundOn)
	}
}

func TestSortIsSeverityThenRuleThenSubject(t *testing.T) {
	fs := []Finding{
		New("redirects.temporary", "https://e.test/b", "", nil),
		New("headers.hsts-missing", "https://e.test", "", nil),
		New("links.broken", "https://e.test/z", "", nil),
		New("cert.expired", "e.test:443", "", nil),
		New("links.broken", "https://e.test/a", "", nil),
	}
	Sort(fs)
	var got []string
	for _, f := range fs {
		got = append(got, f.Rule+" "+f.Subject)
	}
	want := []string{"cert.expired e.test:443", "links.broken https://e.test/a", "links.broken https://e.test/z", "headers.hsts-missing https://e.test", "redirects.temporary https://e.test/b"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("order:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMergeDoesNotDependOnInputOrder(t *testing.T) {
	a := New("links.broken", "https://e.test/x", "https://e.test/x returned 404", map[string]any{"status": 404}, Element("https://e.test/", "a", "href", "/x"))
	c := New("links.broken", "https://e.test/x", "https://e.test/x returned 500", map[string]any{"status": 500}, Element("https://e.test/b", "a", "href", "/x"))
	x, _ := json.Marshal(Merge([]Finding{a, c}))
	y, _ := json.Marshal(Merge([]Finding{c, a}))
	if string(x) != string(y) {
		t.Fatalf("Merge depends on input order:\n%s\n%s", x, y)
	}
}

func TestMergeKeepsTheMostSevere(t *testing.T) {
	fail := New("links.broken", "https://e.test/x", "b", nil)
	info := fail
	info.Severity, info.Message = Info, "a"
	for _, in := range [][]Finding{{fail, info}, {info, fail}} {
		if got := Merge(in)[0].Severity; got != Fail {
			t.Fatalf("Merge(%s, %s) = %s, want fail: merging must never lower the severity", in[0].Severity, in[1].Severity, got)
		}
	}
}

func TestAnUnknownKindIsAnError(t *testing.T) {
	if _, err := json.Marshal(Location{}); err == nil {
		t.Fatal("marshalling a Location with no kind succeeded")
	}
}

func TestNewRejectsAnUnknownRule(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New with an unknown rule ID did not panic")
		}
	}()
	New("links.borken", "x", "", nil)
}

func TestLookupFindsKnownRulesOnly(t *testing.T) {
	if _, ok := Lookup("cert.expired"); !ok {
		t.Fatal("cert.expired not found")
	}
	if _, ok := Lookup("cert.nope"); ok {
		t.Fatal("cert.nope found")
	}
}

// Merge は場所を MaxLocations で切って超えた数を More に正確に残し、どれかが Truncated なら合わせたものも Truncated（順に依らない）。
func TestMergeCapsLocationsAndKeepsTruncated(t *testing.T) {
	many := func(n int) []Location {
		var ls []Location
		for i := 0; i < n; i++ {
			ls = append(ls, Element("https://a.test/p", "a", "href", "/x#"+string(rune('a'+i%26))+string(rune('0'+i/26%10))+string(rune('0'+i/260))))
		}
		return ls
	}
	a := New("links.broken", "https://a.test/x", "m", nil, many(150)...)
	b := New("links.broken", "https://a.test/x", "m", nil, many(150)...) // 同じ場所: 集合では 150 のまま
	out := Merge([]Finding{a, b})
	if len(out) != 1 || len(out[0].FoundOn) != MaxLocations || out[0].More != 50 || out[0].Truncated {
		t.Fatalf("len %d more %d truncated %v", len(out[0].FoundOn), out[0].More, out[0].Truncated)
	}
	plain := New("redirects.temporary", "https://a.test/mid", "m", nil, Element("https://a.test/", "a", "href", "/mid"))
	trunc := New("redirects.temporary", "https://a.test/mid", "m", nil, many(5)...)
	trunc.Truncated = true
	for _, order := range [][]Finding{{plain, trunc}, {trunc, plain}} {
		out := Merge(order)
		if len(out) != 1 || !out[0].Truncated || out[0].More != 0 {
			t.Fatalf("order %v: truncated %v more %d (a truncated input must make the merged finding truncated)", order[0].Truncated, out[0].Truncated, out[0].More)
		}
	}
}
