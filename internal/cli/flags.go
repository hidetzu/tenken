package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hidetzu/tenken/internal/check/cert"
	"github.com/hidetzu/tenken/internal/finding"
)

// flagSpec は 1 つのフラグ。名前・既定・意味の正本は docs/cli-reference.md §3。
// ⚠ 既定の値はここにだけ書く（文字列のまま。利用者の値と同じ関数で解釈する）。--help はこの表から作り、
//
//	TestFlagsMatchCLISpec が docs/cli-reference.md §3 と照らす（CLAUDE.md §3: 同じ値を二か所に書くなら機械で照らす）。
type flagSpec struct {
	name  string
	arg   string // --help に出す値の形。"" なら値を取らない
	def   string // 既定。"" なら無し
	help  string
	group string
}

var flagSpecs = []flagSpec{
	{"only", "<ID,...>", "", "run only these checks (checks: cert, redirects, links, headers, sitemap); not with --skip", "What to check"},
	{"skip", "<ID,...>", "", "do not run these checks", "What to check"},
	{"no-external", "", "", "do not check whether external links are alive", "What to check"},
	{"exclude", "<regexp>", "", "do not fetch URLs matching the pattern (repeatable)", "What to check"},
	{"ca-file", "<path>", "", "add the PEM CA certificates in the file to the OS trust store", "What to check"},
	{"cert-warn", "<N%|Nd>", "25%", "cert.expiring when less than this remains of the certificate's lifetime (%) or this many days (d)", "Thresholds and limits"},
	{"cert-fail", "<N%|Nd>", "10%", "cert.expiring-soon, likewise", "Thresholds and limits"},
	{"max-redirects", "<N>", "3", "redirects.too-many beyond this many redirects", "Thresholds and limits"},
	{"hsts-min-age", "<seconds>", "15552000", "headers.hsts-weak below this max-age", "Thresholds and limits"},
	{"max-depth", "<N>", "3", "crawl this deep, the start page being 0", "Thresholds and limits"},
	{"max-pages", "<N>", "200", "crawl at most this many HTML pages", "Thresholds and limits"},
	{"max-sitemap-urls", "<N>", "50000", "read at most this many URLs from sitemaps", "Thresholds and limits"},
	{"max-links-per-page", "<N>", "1000", "check at most this many distinct references per page", "Thresholds and limits"},
	{"concurrency", "<N>", "4", "parallel connections (to different hosts; one at a time per host)", "Thresholds and limits"},
	{"delay", "<duration>", "100ms", "pause between requests to the same host (robots.txt Crawl-delay wins if longer)", "Thresholds and limits"},
	{"timeout", "<duration>", "10s", "per request, from connecting to the end of the body", "Thresholds and limits"},
	{"max-body", "<size>", "10MiB", "do not read bodies beyond this size", "Thresholds and limits"},
	{"max-duration", "<duration>", "10m", "send no new request after this long from the start of the run (0: no limit)", "Thresholds and limits"},
	{"max-requests", "<N>", "0", "send at most this many HTTP requests in the run; when set, the crawl fetches one URL at a time, in URL order (0: no limit)", "Thresholds and limits"},
	{"fail-on", "<fail|warn>", "fail", "exit 1 when a finding of this severity or worse remains", "Severity and exit code"},
	{"severity", "RULE=<fail|warn|info|off>", "", "change a rule's severity; off drops it (counted in summary.suppressed); repeatable", "Severity and exit code"},
	{"ignore", "RULE[@regexp]", "", "hide known findings of a rule whose subject matches (counted in summary.ignored); repeatable", "Severity and exit code"},
	{"format", "<text|json>", "text", "what to print on stdout", "Output"},
	{"quiet", "", "", "no progress on stderr", "Output"},
	{"user-agent", "<string>", "", "how tenken introduces itself (default: tenken/<version> (+repository URL))", "Output"},
	{"help", "", "", "print this and exit 0", "Output"},
	{"version", "", "", "print the version and exit 0", "Output"},
}

func lookupFlag(name string) (flagSpec, bool) {
	for _, f := range flagSpecs {
		if f.name == name {
			return f, true
		}
	}
	return flagSpec{}, false
}

// options はフラグの値。
type options struct {
	target string

	only, skip  []string
	noExternal  bool
	exclude     []*regexp.Regexp
	caFile      string
	certWarn    cert.Threshold
	certFail    cert.Threshold
	warnRaw     string // 利用者が書いたまま（逆転の文に使う）。既定なら ""
	failRaw     string
	maxRedirs   int
	hstsMinAge  int
	maxDepth    int
	maxPages    int
	maxSitemap  int
	maxLinks    int
	concurrency int
	delay       time.Duration
	timeout     time.Duration
	maxBody     int64
	maxDuration time.Duration // 0 なら無制限（docs/cli-reference.md §3.2）
	maxRequests int           // 0 なら無制限
	failOn      finding.Severity
	severity    map[string]finding.Severity // ルール ID → 重さ。"" は off
	ignore      []ignoreRule
	format      string
	quiet       bool
	userAgent   string
	help        bool
	version     bool
}

// ignoreRule は --ignore の 1 つ。re が nil ならそのルールのすべて。
type ignoreRule struct {
	rule string
	re   *regexp.Regexp
}

// usageError は使い方の誤り（終了コード 2）。文は人に見せる（CLAUDE.md §4: ライブラリの文を含めない）。
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usage(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

// parse は args を options にする。形は docs/cli-reference.md §1（--name value と --name=value、短い形は無い、URL は 1 つ）。
func parse(args []string, defaultUserAgent string) (*options, error) {
	o := &options{severity: map[string]finding.Severity{}, userAgent: defaultUserAgent}
	for _, f := range flagSpecs {
		if f.def != "" {
			if err := o.set(f.name, f.def); err != nil {
				panic("cli: the default of --" + f.name + " does not parse: " + err.Error()) // 表の誤り。入力ではない
			}
		}
	}
	o.warnRaw, o.failRaw = "", ""
	var targets []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			if strings.HasPrefix(a, "-") && a != "-" {
				return nil, usage("%s: short flags are not supported; use the long form (--name)", a)
			}
			targets = append(targets, a)
			continue
		}
		name, value, hasValue := strings.Cut(a[2:], "=")
		f, known := lookupFlag(name)
		if !known {
			return nil, usage("unknown flag --%s (see --help)", name)
		}
		if f.arg == "" {
			if hasValue {
				return nil, usage("--%s takes no value", name)
			}
		} else if !hasValue {
			if i+1 >= len(args) {
				return nil, usage("--%s needs a value %s", name, f.arg)
			}
			i++
			value = args[i]
		}
		if err := o.set(name, value); err != nil {
			return nil, usage("--%s %s: %v", name, value, err)
		}
	}
	if o.help || o.version {
		return o, nil
	}
	if len(o.only) > 0 && len(o.skip) > 0 {
		return nil, usage("--only and --skip cannot be used together")
	}
	if less, same := o.certWarn.Less(o.certFail); same && less {
		// 単位が同じで警告の方が小さい（docs/cli-reference.md §3.2）。単位が違えば大小は証明書で決まるので誤りにしない
		warn, fail := or(o.warnRaw, defaultOf("cert-warn")), or(o.failRaw, defaultOf("cert-fail"))
		return nil, usage("--cert-warn %s --cert-fail %s: the warning threshold is below the failure threshold, so every certificate that warns already fails", warn, fail)
	}
	switch len(targets) {
	case 0:
		return nil, usage("a URL is required: tenken [flags] <URL>")
	case 1:
		o.target = targets[0]
	default:
		return nil, usage("only one URL can be checked per run (got %d)", len(targets))
	}
	return o, nil
}

func defaultOf(name string) string {
	f, _ := lookupFlag(name)
	return f.def
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// set は 1 つのフラグの値を解釈して入れる。既定も利用者の値もここを通る。
func (o *options) set(name, value string) error {
	var err error
	switch name {
	case "only":
		o.only, err = checkIDs(value)
	case "skip":
		o.skip, err = checkIDs(value)
	case "no-external":
		o.noExternal = true
	case "exclude":
		var re *regexp.Regexp
		re, err = pattern(value)
		o.exclude = append(o.exclude, re)
	case "ca-file":
		o.caFile = value
	case "cert-warn":
		o.warnRaw = value
		o.certWarn, err = threshold(value)
	case "cert-fail":
		o.failRaw = value
		o.certFail, err = threshold(value)
	case "max-redirects":
		o.maxRedirs, err = count(value, 0)
	case "hsts-min-age":
		o.hstsMinAge, err = count(value, 0)
	case "max-depth":
		o.maxDepth, err = count(value, 0)
	case "max-pages":
		o.maxPages, err = count(value, 1)
	case "max-sitemap-urls":
		o.maxSitemap, err = count(value, 1)
	case "max-links-per-page":
		o.maxLinks, err = count(value, 1)
	case "concurrency":
		o.concurrency, err = count(value, 1)
	case "delay":
		o.delay, err = duration(value, 0)
	case "timeout":
		o.timeout, err = duration(value, time.Millisecond)
	case "max-body":
		o.maxBody, err = size(value)
	case "max-duration":
		o.maxDuration, err = duration(value, 0)
	case "max-requests":
		o.maxRequests, err = count(value, 0)
	case "fail-on":
		switch value {
		case "fail", "warn":
			o.failOn = finding.Severity(value)
		default:
			err = errors.New("must be fail or warn")
		}
	case "severity":
		err = o.setSeverity(value)
	case "ignore":
		var r ignoreRule
		r, err = ignoreFlag(value)
		o.ignore = append(o.ignore, r)
	case "format":
		switch value {
		case "text", "json":
			o.format = value
		default:
			err = errors.New("must be text or json")
		}
	case "quiet":
		o.quiet = true
	case "user-agent":
		if value == "" {
			err = errors.New("must not be empty")
		}
		o.userAgent = value
	case "help":
		o.help = true
	case "version":
		o.version = true
	default:
		panic("cli: flag --" + name + " is in the table and not in set()")
	}
	return err
}

func checkIDs(value string) ([]string, error) {
	var out []string
	for _, id := range strings.Split(value, ",") {
		id = strings.TrimSpace(id)
		if !contains(finding.Checks, id) {
			return nil, fmt.Errorf("unknown check %q (checks: %s)", id, strings.Join(finding.Checks, ", "))
		}
		out = append(out, id)
	}
	return out, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// pattern は正規表現。⚠ regexp の文は人に見せない（CLAUDE.md §4）。
func pattern(value string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(value)
	if err != nil {
		return nil, errors.New("is not a valid regular expression (Go RE2 syntax)")
	}
	return re, nil
}

// threshold は N% か Nd（docs/cli-reference.md §3.2）。
func threshold(value string) (cert.Threshold, error) {
	switch {
	case strings.HasSuffix(value, "%"):
		p, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
		if err != nil || p < 0 || p > 100 {
			return cert.Threshold{}, errors.New("must be a percentage from 0% to 100%, or a number of days like 30d")
		}
		return cert.Percent(p), nil
	case strings.HasSuffix(value, "d"):
		d, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || d < 0 {
			return cert.Threshold{}, errors.New("must be a number of days like 30d, or a percentage like 25%")
		}
		return cert.Days(d), nil
	}
	return cert.Threshold{}, errors.New("must end in % (of the lifetime) or d (days)")
}

func count(value string, min int) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < min {
		return 0, fmt.Errorf("must be a whole number of at least %d", min)
	}
	return n, nil
}

// duration は 500ms / 10s / 2m（docs/cli-reference.md §3.2）。
func duration(value string, min time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d < min {
		return 0, errors.New("must be a duration like 500ms, 10s or 2m")
	}
	return d, nil
}

// size は 512KiB / 10MiB（docs/cli-reference.md §3.2）。
func size(value string) (int64, error) {
	for _, u := range []struct {
		suffix string
		mul    int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(value, u.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(value, u.suffix), 10, 64)
			if err != nil || n <= 0 || n > (1<<62)/u.mul {
				break
			}
			return n * u.mul, nil
		}
	}
	return 0, errors.New("must be a size like 512KiB or 10MiB")
}

// setSeverity は RULE=fail|warn|info|off（docs/cli-reference.md §3.3）。
func (o *options) setSeverity(value string) error {
	rule, level, ok := strings.Cut(value, "=")
	if !ok {
		return errors.New("must be RULE=fail|warn|info|off")
	}
	if _, known := finding.Lookup(rule); !known {
		return fmt.Errorf("unknown rule %q", rule)
	}
	switch level {
	case "fail", "warn", "info":
		o.severity[rule] = finding.Severity(level)
	case "off":
		o.severity[rule] = ""
	default:
		return errors.New("the level must be fail, warn, info or off")
	}
	return nil
}

// ignoreFlag は RULE[@正規表現]（docs/cli-reference.md §3.4）。
func ignoreFlag(value string) (ignoreRule, error) {
	rule, pat, has := strings.Cut(value, "@")
	if _, known := finding.Lookup(rule); !known {
		return ignoreRule{}, fmt.Errorf("unknown rule %q", rule)
	}
	r := ignoreRule{rule: rule}
	if has {
		re, err := pattern(pat)
		if err != nil {
			return ignoreRule{}, err
		}
		r.re = re
	}
	return r, nil
}

// selected は、チェック id が --only / --skip で選ばれているか。
func (o *options) selected(id string) bool {
	if len(o.only) > 0 {
		return contains(o.only, id)
	}
	return !contains(o.skip, id)
}

// helpText は --help の文。フラグの表から作る（既定を二度書かない）。
func helpText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s checks a web site in one run and exits non-zero when the site has a problem.\n\n", toolName)
	fmt.Fprintf(&b, "Usage:\n  %s [flags] <URL>\n  %s --version\n  %s --help\n", toolName, toolName, toolName)
	group := ""
	for _, f := range flagSpecs {
		if f.group != group {
			group = f.group
			fmt.Fprintf(&b, "\n%s:\n", group)
		}
		left := "--" + f.name
		if f.arg != "" {
			left += " " + f.arg
		}
		line := f.help
		if f.def != "" {
			line += fmt.Sprintf(" (default %s)", f.def)
		}
		fmt.Fprintf(&b, "  %-36s %s\n", left, line)
	}
	fmt.Fprintf(&b, "\nExit codes: %d checked, nothing to fail on; %d checked, findings; %d usage; %d could not check the start URL.\n",
		ExitOK, ExitFindings, ExitUsage, ExitCouldNotCheck)
	return b.String()
}
