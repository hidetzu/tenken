package finding

// Rule は 1 つのルール。ID は一度出したら変えない（docs/cli-reference.md §2.2: CI の設定に書かれるので）。
type Rule struct {
	ID      string
	Check   string
	Default Severity
}

// Rules はすべてのルール。docs/cli-reference.md §2.2 の表と同じであることを internal/walls の TestRulesAndDefaultsMatchCLISpec が照らす。
var Rules = []Rule{
	{"cert.expired", "cert", Fail},
	{"cert.not-yet-valid", "cert", Fail},
	{"cert.expiring-soon", "cert", Fail},
	{"cert.expiring", "cert", Warn},
	{"cert.hostname-mismatch", "cert", Fail},
	{"cert.self-signed", "cert", Fail},
	{"cert.untrusted", "cert", Fail},
	{"redirects.too-many", "redirects", Warn},
	{"redirects.loop", "redirects", Fail},
	{"redirects.downgrade", "redirects", Fail},
	{"redirects.no-https", "redirects", Warn},
	{"redirects.temporary", "redirects", Info},
	{"redirects.bad-location", "redirects", Fail},
	{"links.broken", "links", Fail},
	{"links.forbidden", "links", Warn},
	{"links.unreachable", "links", Fail},
	{"links.rate-limited", "links", Warn},
	{"links.mixed-content", "links", Warn},
	{"headers.hsts-missing", "headers", Warn},
	{"headers.hsts-weak", "headers", Warn},
	{"headers.nosniff-missing", "headers", Warn},
	{"headers.nosniff-invalid", "headers", Warn},
	{"headers.framing-missing", "headers", Warn},
	{"headers.csp-missing", "headers", Info},
	{"headers.referrer-policy-missing", "headers", Info},
	{"sitemap.robots-missing", "sitemap", Info},
	{"sitemap.robots-broken", "sitemap", Warn},
	{"sitemap.robots-unreachable", "sitemap", Warn},
	{"sitemap.robots-rate-limited", "sitemap", Warn},
	{"sitemap.missing", "sitemap", Info},
	{"sitemap.invalid", "sitemap", Fail},
	{"sitemap.rate-limited", "sitemap", Warn},
	{"sitemap.url-broken", "sitemap", Fail},
	{"sitemap.url-unreachable", "sitemap", Fail},
	{"sitemap.url-redirects", "sitemap", Warn},
	{"sitemap.url-rate-limited", "sitemap", Warn},
	{"sitemap.url-disallowed", "sitemap", Warn},
	{"sitemap.url-external", "sitemap", Warn},
}

// Checks はチェックの ID（docs/cli-reference.md §2.1）。internal/walls の TestChecksMatchCLISpec が照らす。
var Checks = []string{"cert", "redirects", "links", "headers", "sitemap"}

// Lookup は ID のルールを返す。無ければ ok = false（未知のルール ID は使い方の誤り。docs/cli-reference.md §1）。
func Lookup(id string) (Rule, bool) {
	for _, r := range Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// New はルールの既定の重さで指摘を作る。⚠ 未知のルール ID は実装の誤りなので panic する（外からの入力ではない）。
func New(ruleID, subject, message string, detail map[string]any, foundOn ...Location) Finding {
	r, ok := Lookup(ruleID)
	if !ok {
		panic("finding: unknown rule " + ruleID)
	}
	return Finding{Rule: r.ID, Check: r.Check, Severity: r.Default, Subject: subject, Message: message, Detail: detail, FoundOn: foundOn}
}
