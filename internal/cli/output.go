package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hidetzu/tenken/internal/crawl"
	"github.com/hidetzu/tenken/internal/finding"
)

// report は 1 回の実行の結果。JSON の形は docs/cli-reference.md §5.2（キーの順はこの構造体の順）。
type report struct {
	SchemaVersion int               `json:"schema_version"`
	Tool          tool              `json:"tool"`
	Target        string            `json:"target"`
	StartedAt     string            `json:"started_at"`
	FinishedAt    string            `json:"finished_at"`
	CouldNotCheck *couldNotCheck    `json:"could_not_check"`
	Summary       summary           `json:"summary"`
	Findings      []finding.Finding `json:"findings"`
	Skipped       []skipped         `json:"skipped"`

	o             *options
	couldNotCheck *couldNotCheck
	raw           []finding.Finding
}

type tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type couldNotCheck struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

type summary struct {
	Fail         int            `json:"fail"`
	Warn         int            `json:"warn"`
	Info         int            `json:"info"`
	Ignored      map[string]int `json:"ignored"`
	Suppressed   map[string]int `json:"suppressed"`
	PagesCrawled int            `json:"pages_crawled"`
	LimitsHit    []string       `json:"limits_hit"`
	NotFetched   notFetched     `json:"not_fetched"`
	ExitCode     int            `json:"exit_code"`
}

type notFetched struct {
	Excluded         int `json:"excluded"`
	External         int `json:"external"`
	RobotsDisallowed int `json:"robots_disallowed"`
	RobotsUnavail    int `json:"robots_unavailable"`
	OverMaxBody      int `json:"over_max_body"`
	OverLimits       int `json:"over_limits"`
	Private          int `json:"private"`
}

type skipped struct {
	Check  string `json:"check"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func newReport(o *options, started time.Time) *report {
	return &report{
		SchemaVersion: 1,
		Tool:          tool{Name: toolName, Version: version()},
		Target:        o.target,
		StartedAt:     started.UTC().Format(time.RFC3339),
		Summary:       summary{Ignored: map[string]int{}, Suppressed: map[string]int{}, LimitsHit: []string{}},
		Findings:      []finding.Finding{},
		Skipped:       []skipped{},
		o:             o,
	}
}

func (r *report) skip(check, code, reason string) {
	r.Skipped = append(r.Skipped, skipped{Check: check, Code: code, Reason: reason})
}

func (r *report) add(fs []finding.Finding) { r.raw = append(r.raw, fs...) }

// crawled は巡回の数を summary に入れる（docs/cli-reference.md §5.2: 取りに行かなかったものは必ず数えて出す）。
func (r *report) crawled(res crawl.Result) {
	r.Summary.PagesCrawled = len(res.Pages)
	r.Summary.LimitsHit = append([]string{}, res.LimitsHit...)
	nf := res.NotFetched
	r.Summary.NotFetched = notFetched{Excluded: nf.Excluded, External: nf.External, RobotsDisallowed: nf.RobotsDisallowed, RobotsUnavail: nf.RobotsUnavailable, OverMaxBody: nf.OverMaxBody, OverLimits: nf.OverLimits, Private: nf.Private}
}

// sitemapRead は sitemap の読み取りの数を summary に足す（docs/cli-reference.md §5.2: external は sitemap の別オリジンも、limits_hit は max-sitemap-urls も）。
func (r *report) sitemapRead(res crawl.SitemapResult) {
	if res.LimitHit {
		r.Summary.LimitsHit = append(r.Summary.LimitsHit, "max-sitemap-urls")
	}
	if res.NestingHit {
		r.Summary.LimitsHit = append(r.Summary.LimitsHit, "sitemap-nesting")
	}
	r.Summary.NotFetched.External += res.External
	r.Summary.NotFetched.RobotsDisallowed += res.Disallowed
	r.Summary.NotFetched.Excluded += res.Excluded
	r.Summary.NotFetched.Private += res.Private
	r.Summary.NotFetched.OverLimits += res.OverLimits
}

// budgetHit は、当たった実行全体の上限（max-duration / max-requests）を limits_hit に足す（docs/cli-reference.md §5.2）。
func (r *report) budgetHit(names []string) {
	r.Summary.LimitsHit = append(r.Summary.LimitsHit, names...)
}

// finish は --severity と --ignore を当て、数え、終了コードを決めて、出力する。
func (r *report) finish(stdout, stderr io.Writer, finished time.Time) int {
	r.FinishedAt = finished.UTC().Format(time.RFC3339)
	if r.couldNotCheck != nil {
		// 点検できなかった: findings と skipped は空（docs/cli-reference.md §5.2）
		r.CouldNotCheck = r.couldNotCheck
		r.Findings, r.Skipped = []finding.Finding{}, []skipped{}
		r.Summary.ExitCode = ExitCouldNotCheck
		fmt.Fprintf(stderr, "%s: could not check: %s\n", toolName, r.couldNotCheck.Reason) // reason は URL から始まる
		if r.o.format == "json" {
			r.writeJSON(stdout)
		}
		return ExitCouldNotCheck
	}
	for _, f := range finding.Merge(r.raw) {
		if sev, changed := r.o.severity[f.Rule]; changed {
			if sev == "" {
				r.Summary.Suppressed[f.Rule]++
				continue
			}
			f.Severity = sev
		}
		if r.ignored(f) {
			r.Summary.Ignored[f.Rule]++
			continue
		}
		switch f.Severity {
		case finding.Fail:
			r.Summary.Fail++
		case finding.Warn:
			r.Summary.Warn++
		case finding.Info:
			r.Summary.Info++
		}
		r.Findings = append(r.Findings, f)
	}
	finding.Sort(r.Findings)
	sort.Slice(r.Skipped, func(i, j int) bool { return r.Skipped[i].Check < r.Skipped[j].Check })
	r.Summary.ExitCode = ExitOK
	if r.Summary.Fail > 0 || (r.o.failOn == finding.Warn && r.Summary.Warn > 0) {
		r.Summary.ExitCode = ExitFindings
	}
	if r.o.format == "json" {
		r.writeJSON(stdout)
	} else {
		r.writeText(stdout)
	}
	return r.Summary.ExitCode
}

func (r *report) ignored(f finding.Finding) bool {
	for _, ig := range r.o.ignore {
		if ig.rule == f.Rule && (ig.re == nil || ig.re.MatchString(f.Subject)) {
			return true
		}
	}
	return false
}

func (r *report) writeJSON(w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(r)
}

// writeText は docs/cli-reference.md §5.1 の形。見た目は v0.1.0 の間は変えてよい。機械が読むのは JSON。
func (r *report) writeText(w io.Writer) {
	// 上限はすべて 1 行目に言う（点検した範囲の分母: どこまで見る実行だったか。docs/cli-reference.md §4、§5.1）
	fmt.Fprintf(w, "%s %s — %s (%d pages crawled; limits: max-depth %d, max-pages %d, max-duration %s, max-requests %s)\n\n",
		toolName, version(), r.Target, r.Summary.PagesCrawled, r.o.maxDepth, r.o.maxPages, limitText(durationText(r.o.maxDuration), r.o.maxDuration == 0), limitText(fmt.Sprint(r.o.maxRequests), r.o.maxRequests == 0))
	for _, f := range r.Findings {
		fmt.Fprintf(w, "%-5s %-24s %s\n", strings.ToUpper(string(f.Severity)), f.Rule, f.Message)
		for _, l := range f.FoundOn {
			fmt.Fprintf(w, "        on %s\n", locationText(l))
		}
		switch {
		case f.Truncated:
			// 巡回が場所を捨てた: 本当の数は分からないので言わない（evidence.md）
			fmt.Fprintf(w, "        and more places than tenken kept (it keeps the first %d per URL while crawling; the JSON lists the first %d)\n", crawl.MaxLocationsPerURL, len(f.FoundOn))
		case f.More > 0:
			fmt.Fprintf(w, "        and %d more places (the JSON lists the first %d)\n", f.More, len(f.FoundOn))
		}
	}
	if len(r.Findings) > 0 {
		fmt.Fprintln(w)
	}
	nf := r.Summary.NotFetched
	fmt.Fprintf(w, "not fetched: %d excluded, %d external, %d disallowed by robots.txt, %d because robots.txt was unavailable, %d over --max-body, %d over the limits, %d to private addresses\n",
		nf.Excluded, nf.External, nf.RobotsDisallowed, nf.RobotsUnavail, nf.OverMaxBody, nf.OverLimits, nf.Private)
	fmt.Fprintf(w, "limits hit: %s\n", orNone(r.Summary.LimitsHit))
	var sk []string
	for _, s := range r.Skipped {
		sk = append(sk, fmt.Sprintf("%s (%s)", s.Check, s.Code))
	}
	fmt.Fprintf(w, "skipped: %s\n", orNone(sk))
	fmt.Fprintf(w, "ignored: %s\n", countsLine(r.Summary.Ignored))
	fmt.Fprintf(w, "result: %d fail, %d warn, %d info — exit %d\n", r.Summary.Fail, r.Summary.Warn, r.Summary.Info, r.Summary.ExitCode)
}

// locationText は見つかった場所の 1 行（docs/cli-reference.md §5.1 の例: "on https://example.com/  a[href]"）。
func locationText(l finding.Location) string {
	switch l.Kind {
	case finding.KindElement:
		return fmt.Sprintf("%s  %s[%s]", l.Page, l.Element, l.Attribute)
	case finding.KindHeader:
		return fmt.Sprintf("%s  %s", l.Page, l.Header)
	case finding.KindSitemap:
		return fmt.Sprintf("%s  (sitemap %s)", l.Loc, l.Sitemap)
	case finding.KindStart:
		return l.URL + "  (the start URL)"
	case finding.KindProbe:
		return l.URL + "  (checked by tenken)"
	}
	return l.URL
}

// limitText は上限の値。0（無制限）なら "none"。
func limitText(v string, unlimited bool) string {
	if unlimited {
		return "none"
	}
	return v
}

// durationText は時間を短く書く（10m0s ではなく 10m。1m30s はそのまま）。
func durationText(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

// countsLine は "3 (links.broken, cert.expiring)" の形。ルール ID の順は決まっている（同じ入力なら同じ出力）。
func countsLine(m map[string]int) string {
	if len(m) == 0 {
		return "0"
	}
	total := 0
	var ids []string
	for id, n := range m {
		total += n
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Sprintf("%d (%s)", total, strings.Join(ids, ", "))
}

// proxyUse は、プロキシを通した要求の URL の scheme を集める（要求は並行して出るので mutex で守る）。
type proxyUse struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (p *proxyUse) add(scheme string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	p.seen[scheme] = true
}

func (p *proxyUse) schemes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for s := range p.seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// proxyNotice は、プロキシを通した要求があったときに標準エラーに出す 1 行（docs/cli-reference.md §3.6）。
// 無ければ ""。⚠ プロキシの先で何に接続されたかは確かめられない: プライベートアドレスを避ける規則は、プロキシを通る要求には
// 効かない（docs/cli-reference.md §3.6）。括弧の中は、その scheme の要求に Go が使う変数の名前
// （大文字が空でなければ大文字。Go と同じ順）。通したかどうかは決めない（決めたのは Go）。
func proxyNotice(schemes []string, getenv func(string) string) string {
	var names []string
	for _, sc := range schemes {
		upper, lower := strings.ToUpper(sc)+"_PROXY", strings.ToLower(sc)+"_proxy"
		switch {
		case getenv(upper) != "":
			names = append(names, upper)
		case getenv(lower) != "":
			names = append(names, lower)
		default:
			names = append(names, upper)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: a proxy is in use (%s); where the proxy connects is not checked, so private addresses are not kept out", toolName, strings.Join(names, ", "))
}
