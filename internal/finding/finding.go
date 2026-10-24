// Package finding は指摘（finding）の型、重さ、ルール ID、見つかった場所を持つ。
// 形と意味の正本は docs/cli-reference.md §2.2（ルール）と §5.2（JSON）。ルール ID の一覧はここだけに置く（.claude/rules/go.md）。
package finding

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Severity は 1 件の指摘の重さ（docs/cli-reference.md §6: 実行全体の結果ではない）。
type Severity string

const (
	Fail Severity = "fail"
	Warn Severity = "warn"
	Info Severity = "info"
)

// rank は並び順（fail → warn → info。docs/cli-reference.md §5.1）。
func (s Severity) rank() int {
	switch s {
	case Fail:
		return 0
	case Warn:
		return 1
	case Info:
		return 2
	}
	return 3
}

// MaxLocations は 1 件の指摘が持つ「見つかった場所」の上限。⚠ 入力は敵対的でありうる（同じ URL を何万か所から参照するサイト）。
// 超えた分は数だけ More に残す（JSON には出ない: docs/cli-reference.md §5.2 にキーが無い。text は「ほかに N か所」と言う。§5.1）。
const MaxLocations = 100

// Finding は 1 つのルールが 1 つの対象について言ったこと。JSON のキーは docs/cli-reference.md §5.2。
type Finding struct {
	Rule     string         `json:"rule"`
	Check    string         `json:"check"`
	Severity Severity       `json:"severity"`
	Subject  string         `json:"subject"`
	Message  string         `json:"message"`
	Detail   map[string]any `json:"detail"`
	FoundOn  []Location     `json:"found_on"`
	// More は、Merge が MaxLocations を超えて FoundOn に入れなかった場所の数（正確。JSON には出ない）
	More int `json:"-"`
	// Truncated は、場所がここに届く前に（巡回が）捨てられていて、本当の数が分からないこと。
	// ⚠ 数を言わない: 2 つのリンクが同じ場所を捨てていれば足し算は嘘になる
	Truncated bool `json:"-"`
}

// MarshalJSON は detail と found_on を、空でも null ではなく {} と [] で出す（docs/cli-reference.md §5.2: 0 件でも同じ形）。
func (f Finding) MarshalJSON() ([]byte, error) {
	type plain Finding
	p := plain(f)
	if p.Detail == nil {
		p.Detail = map[string]any{}
	}
	if p.FoundOn == nil {
		p.FoundOn = []Location{}
	}
	return json.Marshal(p)
}

// Location は見つかった場所。キーは Kind で決まる（docs/cli-reference.md §5.2 の found_on の表）。
type Location struct {
	Kind      Kind
	Page      string
	Element   string
	Attribute string
	Value     *string // element: 属性の値。header: そのページで見たヘッダの値（無ければ nil = JSON の null）
	Header    string
	Sitemap   string
	Loc       string
	URL       string
}

// Kind は Location の種類。
type Kind string

const (
	KindElement Kind = "element"
	KindHeader  Kind = "header"
	KindSitemap Kind = "sitemap"
	KindStart   Kind = "start"
	KindProbe   Kind = "probe"
)

// Element は a[href] などで見つかった場所。
func Element(page, element, attribute, value string) Location {
	return Location{Kind: KindElement, Page: page, Element: element, Attribute: attribute, Value: &value}
}

// Header はヘッダで見つかった場所。value が nil ならヘッダが無かった。
func Header(page, header string, value *string) Location {
	return Location{Kind: KindHeader, Page: page, Header: header, Value: value}
}

// InSitemap は sitemap の <loc> で見つかった場所。
func InSitemap(sitemap, loc string) Location {
	return Location{Kind: KindSitemap, Sitemap: sitemap, Loc: loc}
}

// Start は開始 URL そのもの。
func Start(url string) Location { return Location{Kind: KindStart, URL: url} }

// Probe は tenken が自分で確かめに行った URL。
func Probe(url string) Location { return Location{Kind: KindProbe, URL: url} }

// MarshalJSON は Kind ごとに決まったキーだけを出す。
func (l Location) MarshalJSON() ([]byte, error) {
	switch l.Kind {
	case KindElement:
		return json.Marshal(struct {
			Kind      Kind    `json:"kind"`
			Page      string  `json:"page"`
			Element   string  `json:"element"`
			Attribute string  `json:"attribute"`
			Value     *string `json:"value"`
		}{l.Kind, l.Page, l.Element, l.Attribute, l.Value})
	case KindHeader:
		return json.Marshal(struct {
			Kind   Kind    `json:"kind"`
			Page   string  `json:"page"`
			Header string  `json:"header"`
			Value  *string `json:"value"`
		}{l.Kind, l.Page, l.Header, l.Value})
	case KindSitemap:
		return json.Marshal(struct {
			Kind    Kind   `json:"kind"`
			Sitemap string `json:"sitemap"`
			Loc     string `json:"loc"`
		}{l.Kind, l.Sitemap, l.Loc})
	case KindStart, KindProbe:
		return json.Marshal(struct {
			Kind Kind   `json:"kind"`
			URL  string `json:"url"`
		}{l.Kind, l.URL})
	}
	// ⚠ docs/cli-reference.md §5.2 に無い kind は互換の約束を破る。黙って出さず、実装の誤りとして返す
	return nil, fmt.Errorf("finding: location kind %q is not one of docs/cli-reference.md § 5.2's kinds", l.Kind)
}

func (l Location) key() string {
	v := ""
	if l.Value != nil {
		v = "=" + *l.Value
	}
	return string(l.Kind) + "\x00" + l.Page + "\x00" + l.Element + "\x00" + l.Attribute + "\x00" + l.Header + "\x00" + l.Sitemap + "\x00" + l.Loc + "\x00" + l.URL + "\x00" + v
}

// Merge は同じ指摘（rule と subject が同じもの。docs/cli-reference.md §5.2）を 1 件にまとめ、見つかった場所を集める。
// 結果は Sort の順。同じ場所が二度出れば 1 つにする。
// ⚠ 入力の順序に依らない。並行に取りに行った結果を渡しても、同じ入力なら同じ出力（docs/test-perspectives.md §2.3）。
// message と detail は、message → detail の JSON の辞書順でいちばん小さいものを使う。
// severity はいちばん重いものを使う。⚠ まとめることで重さが下がってはいけない（fail が info になれば終了コードが変わる）。
func Merge(fs []Finding) []Finding {
	byKey := map[string]int{}
	var out []Finding
	for _, f := range fs {
		k := f.Rule + "\x00" + f.Subject
		i, seen := byKey[k]
		if !seen {
			byKey[k] = len(out)
			f.FoundOn = append([]Location(nil), f.FoundOn...)
			out = append(out, f)
			continue
		}
		if canonicalLess(f, out[i]) {
			out[i].Message, out[i].Detail = f.Message, f.Detail
		}
		if f.Severity.rank() < out[i].Severity.rank() {
			out[i].Severity = f.Severity
		}
		out[i].FoundOn = append(out[i].FoundOn, f.FoundOn...)
		out[i].Truncated = out[i].Truncated || f.Truncated // どちらかが捨てていれば、合わせた数も分からない
		// More は足さない: 入ってくる指摘の More は常に 0 で（入れるのは下の切り詰めだけ）、集合から数え直す
	}
	for i := range out {
		out[i].FoundOn = uniqueSorted(out[i].FoundOn)
		if n := len(out[i].FoundOn); n > MaxLocations {
			out[i].More = n - MaxLocations // 切る前の集合は重複を除いてあるので、正確な数
			out[i].FoundOn = out[i].FoundOn[:MaxLocations]
		}
	}
	Sort(out)
	return out
}

func canonicalLess(a, b Finding) bool {
	if a.Message != b.Message {
		return a.Message < b.Message
	}
	ja, _ := json.Marshal(a.Detail) // encoding/json は map のキーを並べて出すので、同じ中身なら同じ文字列
	jb, _ := json.Marshal(b.Detail)
	return string(ja) < string(jb)
}

func uniqueSorted(ls []Location) []Location {
	seen := map[string]bool{}
	var out []Location
	for _, l := range ls {
		if k := l.key(); !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].key() < out[b].key() })
	return out
}

// Sort は重さ（fail → warn → info）→ ルール ID → 対象の順に並べる（docs/cli-reference.md §5.1。同じ入力なら同じ出力）。
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(a, b int) bool {
		x, y := fs[a], fs[b]
		if x.Severity.rank() != y.Severity.rank() {
			return x.Severity.rank() < y.Severity.rank()
		}
		if x.Rule != y.Rule {
			return x.Rule < y.Rule
		}
		return x.Subject < y.Subject
	})
}
