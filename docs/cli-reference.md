# CLI — what a user types and reads

**This file is the source of truth for everything tenken shows to the outside:** the command line, the flags and
their defaults, the exit codes, the human-readable output, the JSON schema, the rule names and their severities,
and the words.
**Only the owner decides what it says** (how a change to it is merged: [`.claude/rules/git.md`](../.claude/rules/git.md) § Permission).

What tenken checks (the scope) is [`design.md`](design.md) §0, how each check is tested is
[`test-perspectives.md`](test-perspectives.md), what is implemented, with the tests that assert it, is [`test-perspectives.md`](test-perspectives.md) §6, and the known limitations are [`discovery.md`](discovery.md) §2.
**This file is the specification and is written before the code.** It does not say whether something is implemented.

Everything a user sees — `--help`, every message, JSON keys and values, rule IDs — is English.
`docs/` is English ([`.claude/rules/README.md`](../.claude/rules/README.md) § Language).

---

## 1. Command line

```text
tenken [flags] <URL>
tenken --version
tenken --help
```

- Exactly one `<URL>` (design.md §0.3), `http://` or `https://`. Without a scheme it is a **usage error** (exit code 2).
  tenken does not guess `https://` (checking the guessed URL would report on a URL the user did not write)
- There are no subcommands: the only operation in v0.1.0 is "check"
- Flags are accepted as `--name value` and as `--name=value`. There are no short forms (`-o` and the like):
  they get written into CI configuration and read back later, and long names read better
- **There is no configuration file** (v0.1.0). In CI, list the flags. A second way to say the same thing would
  let one setting be said in two places (CLAUDE.md §3)
- **An unknown flag, check ID or rule ID is a usage error** (2). It is never ignored silently: a mistyped
  exclusion would otherwise have no effect while CI stays green

## 2. Checks and rules

### 2.1 Check names

| ID | design.md §0.3 | What it fetches |
|---|---|---|
| `cert` | TLS certificate | Only a TLS connection to the start URL's host (after redirects), to the address the start URL's chain connected to (it does not resolve the name again). No crawl |
| `redirects` | Redirects | The redirects met while crawling. If the start URL is https, one request to `http://` on the same host. If the start URL is http, whether it itself fails to redirect to https is also `redirects.no-https` |
| `links` | Broken links (including static mixed content) | What the crawled pages refer to |
| `headers` | Security headers | The headers of the crawled pages (HTML responses) |
| `sitemap` | sitemap / robots | robots.txt, the sitemaps, and the URLs listed in them |

- **robots.txt is read for the crawl even with `--skip sitemap`.** The `sitemap.robots-*` findings belong to
  `sitemap`'s rules, so they are not reported then
- robots.txt follows RFC 9309: only the group that most specifically matches the User-Agent's product name (§3.5)
  is used (several groups with the same name are merged); with no match, the `*` group. Redirects of robots.txt
  are followed up to 5 times (RFC 9309 §2.3.1.2); a loop, a broken `Location` or more than 5 is treated like
  unreachable (everything disallowed). robots.txt's disallow (and the complete
  disallow when it is unavailable) also applies to liveness checks of same-origin URLs: they are not fetched and
  are counted in `not_fetched`. Other sites' robots.txt is not read
- If the start URL's certificate has a problem (expired, name mismatch and so on), **the other checks still run
  as long as tenken can connect and read the response**. Those fetches skip verification, and the certificate's
  problem is reported once, as a `cert.*` finding

### 2.2 Rules

**Every finding comes from exactly one rule.** A rule ID is `<check>.<name>`.
Ignoring (§3.4) and severity overrides (§3.3) name rules by this ID. **An ID never changes once released**
(it gets written into CI configuration).

Default severities: `fail` / `warn` / `info`. The grounds are test-perspectives.md §3.
**"Broken" (`*-broken`) means the response said so; "unreachable" (`*-unreachable`) means no usable response came
back** (§6).

| Rule ID | Default | What is happening |
|---|---|---|
| `cert.expired` | fail | The certificate has expired |
| `cert.not-yet-valid` | fail | The certificate's validity period has not started |
| `cert.expiring-soon` | fail | What remains is below the fail threshold (§3.2) |
| `cert.expiring` | warn | What remains is below the warn threshold (§3.2) |
| `cert.hostname-mismatch` | fail | The host name is not among the certificate's names (SAN) |
| `cert.self-signed` | fail | Self-signed (not reported when trusted with `--ca-file`) |
| `cert.untrusted` | fail | No path to a trusted issuer (self-signed excluded). **A missing intermediate and an unknown CA cannot be told apart**, so this is one rule and its reason says so ([`design.md §1`](design.md)). **The chain is judged at the current time** (if the certificate itself is expired or not yet valid, at the nearer end of its validity period, kept apart from the validity finding) |
| `redirects.too-many` | warn | More redirects than the threshold (§3.2) |
| `redirects.loop` | fail | The redirects loop |
| `redirects.downgrade` | fail | A redirect from https to http |
| `redirects.no-https` | warn | `http://` does not redirect to https |
| `redirects.temporary` | info | 302 / 307 (a temporary redirect) |
| `redirects.bad-location` | fail | `Location` is missing or is not a valid URL |
| `links.broken` | fail | The target answers 4xx / 5xx (except 429, and 401 / 403 from another origin). **The start URL too** (§4) |
| `links.forbidden` | warn | A target on another origin answers 401 / 403 (the other side refused; often bot protection, and it opens in a browser). 401 / 403 on the same origin is your own site, so it is `links.broken`. Which one is decided by **the origin of the last response, the one that answered 401 / 403**: your own URL redirecting to another origin that answers 403 is `links.forbidden`, and the reverse is `links.broken` |
| `links.unreachable` | fail | The target cannot be reached (DNS, connect, TLS, timeout, a response that is not readable as HTTP). The reason is stated |
| `links.rate-limited` | warn | Still 429 after the retry |
| `links.mixed-content` | warn | An https page refers to an `http://` subresource (`src` / `srcset` / `link[href]`). `a[href]` is not included. The subject is the `http://` resource |
| `headers.hsts-missing` | warn | An https response has no HSTS (http responses are not looked at) |
| `headers.hsts-weak` | warn | HSTS `max-age` is below the threshold, or its value is broken |
| `headers.nosniff-missing` | warn | No `X-Content-Type-Options` |
| `headers.nosniff-invalid` | warn | `X-Content-Type-Options` is something other than `nosniff` |
| `headers.framing-missing` | warn | Neither `X-Frame-Options` nor CSP `frame-ancestors` |
| `headers.csp-missing` | info | No CSP |
| `headers.referrer-policy-missing` | info | No `Referrer-Policy` |
| `sitemap.robots-missing` | info | robots.txt answers 4xx (except 429). Crawled without restrictions, as RFC 9309's "unavailable" |
| `sitemap.robots-broken` | warn | robots.txt answers 5xx. The crawl stops at the start page (§2.3) |
| `sitemap.robots-unreachable` | warn | robots.txt cannot be reached (DNS, connect, TLS, timeout, a response that is not readable as HTTP), or its redirects are unusable (a loop, a broken `Location`, more than 5, or a redirect into a private address, which is not followed — §5.2 `not_fetched.private`; `reason` is `redirect`). The crawl stops at the start page (§2.3) |
| `sitemap.robots-rate-limited` | warn | robots.txt is still 429 after the retry. The crawl stops at the start page (§2.3) |
| `sitemap.missing` | info | No sitemap was found (robots.txt has no `Sitemap:` line, and `/sitemap.xml` answers 4xx; a 5xx is a broken response, so it is `sitemap.invalid`) |
| `sitemap.invalid` | fail | The sitemap's XML is broken. Also: a declared sitemap (a `Sitemap:` line, an index's child) that answers 4xx / 5xx or cannot be reached, and a sitemap larger than `--max-body` that could not be read to the end (the URLs read so far are still checked). Also: a `<loc>` that is not a URL (`problem` gives the count and an example; the other URLs are checked) |
| `sitemap.rate-limited` | warn | A sitemap file (declared, an index's child, or `/sitemap.xml`) was still 429 after the retry and was not read |
| `sitemap.url-broken` | fail | A URL in the sitemap answers 4xx / 5xx |
| `sitemap.url-unreachable` | fail | A URL in the sitemap cannot be reached (DNS, connect, TLS, timeout, a response that is not readable as HTTP) |
| `sitemap.url-redirects` | warn | A URL in the sitemap redirects |
| `sitemap.url-rate-limited` | warn | A URL in the sitemap is still 429 after the retry |
| `sitemap.url-disallowed` | warn | The sitemap lists a URL that robots.txt disallows (not fetched). When robots.txt is unavailable (the complete disallow of §2.1), every same-origin URL |
| `sitemap.url-external` | warn | The sitemap lists a URL on another origin (not fetched) |

**Which findings can appear together**:

- `cert` reports **only the most severe one** about the validity period: `expired` / `not-yet-valid` >
  `expiring-soon` > `expiring`. Only one about trust: `self-signed` or `untrusted`. A name mismatch is independent
  of both
- When the warn threshold is below the fail threshold (different units that cross over for this certificate),
  only `expiring-soon` is reported (§3.2)

### 2.3 Not findings

- **Could not check** (no usable response for the start URL — unreachable, still 429 after the retry, or its
  redirects went into a private address of another host —, or
  wrong arguments) is not a finding. It is exit code 3 / 2 (§4, CLAUDE.md §1)
- **A check that could not run, or that stopped part-way,** goes in `skipped` (§5.2) with a **reason code**, not in
  findings. It is never dropped silently

| Reason code | When |
|---|---|
| `not-https` | The start URL is http, so `cert` does not apply |
| `start-page-broken` | The start URL (or where it redirects) answered 4xx / 5xx, or its redirects looped, had a broken `Location`, or went beyond `--max-redirects`, so the checks that need a crawl (`redirects` / `links` / `headers`) cannot go past the start page |
| `probe-unavailable` | When the start URL is https, the `http://` URL on the same host that tenken checks could not be reached (including a refused connection) or was still 429 after the retry, so `redirects.no-https` could not be checked |
| `robots-unavailable` | robots.txt answered 5xx or could not be reached (RFC 9309's complete disallow), its redirects were unusable (a loop, a broken `Location`, more than 5, a redirect into a private address; §2.1), or it was still 429 after the retry (RFC 9309 allows treating that as 4xx; tenken stops, to be careful), so the crawl stopped at the start page |
| `not-selected` | Left out by `--only` / `--skip` |

## 3. Flags

### 3.1 What to check

| Flag | Default | Meaning |
|---|---|---|
| `--only <ID,...>` | (all) | Run only these checks. Not together with `--skip` (2) |
| `--skip <ID,...>` | (none) | Do not run these checks |
| `--no-external` | (external links are checked) | Do not check whether external links are alive |
| `--exclude <regexp>` | (none) | Do not fetch matching URLs. Repeatable |
| `--ca-file <PATH>` | (the OS trust store, as Go loads it: `SSL_CERT_FILE` / `SSL_CERT_DIR` change where it is read from; on macOS / Windows, setting either reads the certificates there instead of the OS store) | **Add** the PEM CA certificates to the trust store (does not replace it) |

### 3.2 Thresholds and limits

| Flag | Default | Meaning |
|---|---|---|
| `--cert-warn <N%\|Nd>` | `25%` | `cert.expiring` when less than this share of the validity period (`%`) or this many days (`d`) remains |
| `--cert-fail <N%\|Nd>` | `10%` | Likewise, `cert.expiring-soon` |
| `--max-redirects <N>` | `3` | `redirects.too-many` beyond this (a warning at the 4th redirect) |
| `--hsts-min-age <seconds>` | `15552000` | A `max-age` below this is `headers.hsts-weak` (180 days) |
| `--max-depth <N>` | `3` | Crawl to this depth, the start page being 0 |
| `--max-pages <N>` | `200` | The most HTML pages to crawl |
| `--max-sitemap-urls <N>` | `50000` | The most URLs to read from sitemaps (nested ones included). The same as sitemaps.org's limit for one file |
| `--max-links-per-page <N>` | `1000` | The most distinct targets to check from one page. The rest are not fetched and are counted in `not_fetched.over_limits`, with `max-links-per-page` in `limits_hit`. A reference over the limit is still remembered as a place: if the same URL is checked from another page, it appears in `found_on` |
| `--concurrency <N>` | `4` | Parallel connections. **One at a time to the same host** (what runs in parallel is different hosts) |
| `--delay <duration>` | `100ms` | The pause between requests to the same host (after the previous response). robots.txt's `Crawl-delay` wins if it is longer. A host that answered 429 gets a wider pause for the rest of the run (the values are in test-perspectives.md §2) |
| `--timeout <duration>` | `10s` | Per request (from connecting to the end of reading the response) |
| `--max-body <size>` | `10MiB` | Bodies beyond this are not read (their links are not picked up) |
| `--max-duration <duration>` | `10m` | The time limit for the whole run, counted from its start. After it, no new request is sent; requests already sent are finished (up to `--timeout`), and the findings so far are reported. URLs not fetched are counted in `not_fetched.over_limits`, with `max-duration` in `limits_hit`. The exit code follows the findings (§4). `0` means no limit |
| `--max-requests <N>` | `0` | The most HTTP requests in the whole run. `0` means no limit. Every request that leaves tenken counts (the start page, the `http://` probe, robots.txt, the crawl, the sitemaps, 429 retries, each redirect hop). The start page, the probe and robots.txt are counted but never stopped (without them there is nothing to check). When it is reached, the same as `--max-duration`, with `max-requests` in `limits_hit`. **When set, the crawl fetches one URL at a time, in URL order** (`--concurrency` does not apply), so that which URLs fall inside the limit is the same for the same input (the default is to be decided again once the number of requests has been measured) |

- Durations are written like `500ms` / `10s` / `2m`. Sizes like `512KiB` / `10MiB`.
- If `--cert-warn` and `--cert-fail` **use the same unit** and the warning is the smaller one (it would fail first),
  it is a usage error (2). **With different units** which one is larger depends on the certificate's validity
  period, so it is not an error; for a certificate where they cross over, only `expiring-soon` is reported (§2.2)
- **Whatever a limit or an exclusion kept tenken from fetching is always counted and reported**
  (`summary.not_fetched`, `summary.limits_hit`; §5.2). "No broken links" is a claim about the pages crawled, not
  about the whole site (`.claude/rules/evidence.md`, tenken's lines)

### 3.3 Severity and exit code

| Flag | Default | Meaning |
|---|---|---|
| `--fail-on <fail\|warn>` | `fail` | With `warn`, a warning alone makes the exit code 1 |
| `--severity <RULE=fail\|warn\|info\|off>` | (none) | Change a rule's severity. `off` drops it (the number dropped is in `summary.suppressed`). Repeatable |

### 3.4 Ignoring known problems

| Flag | Default | Meaning |
|---|---|---|
| `--ignore <RULE[@regexp]>` | (none) | Hide that rule's findings whose subject (`subject` in §5.2) matches the regexp. Without `@`, all of that rule's findings. Repeatable |

- **Ignored findings do not count toward the exit code.** Their number is reported per rule in `summary.ignored`.
  **They are never removed silently** (if you cannot see how many are hidden, you cannot notice the list growing)
- `--severity RULE=off` means "do not use this rule"; `--ignore` means "hide a known, specific problem". They mean
  different things, so they are different flags

### 3.5 Output

| Flag | Default | Meaning |
|---|---|---|
| `--format <text\|json>` | `text` | What to print on standard output |
| `--quiet` | (none) | No progress on standard error |
| `--user-agent <string>` | `<name>/<version> (+<repository URL>)` | How tenken introduces itself. `<name>` is the public name `tenken` |

- **Results go to standard output; progress and logs to standard error.** Progress is shown only when standard
  error is a terminal
- Colour only when standard output is a terminal, and never when `NO_COLOR` is set
- JUnit XML, SARIF and GitHub Actions annotations are not in v0.1.0 (CLAUDE.md §3: a third output format needs a
  reason in `design.md`). The JSON is shaped so that it can be converted

### 3.6 Other

- Proxies follow `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`. Go (`golang.org/x/net/http/httpproxy`) interprets the
  values; a value that is not a valid URL is dropped silently, as Go does (tenken connects directly)
- **When a request went through a proxy**, one line on standard error at the end of the run says that where the
  proxy connects is not checked: `tenken: a proxy is in use (HTTPS_PROXY); where the proxy connects is not checked, so
  private addresses are not kept out`. The name in parentheses is the variable Go uses for that scheme (upper case
  first, as Go does). A request through a proxy is not checked against private addresses (§5.2
  `not_fetched.private`): the proxy connects, not tenken. Standard output and the exit code do not change. Whether a
  request goes through a proxy is Go's decision as above (so not when `NO_PROXY` matches, when Go drops the value, or
  for a request to a loopback address); a run that connects to nothing prints nothing
- `--version` prints `<name> <version>` on one line and exits 0. `<version>` (also in the default `--user-agent`) is,
  in this order: the version a release build was given; otherwise the module version Go recorded in the binary
  (`go install github.com/hidetzu/tenken/cmd/tenken@v0.1.0` gives `0.1.0`), without the leading `v`; otherwise `0.0.0-dev`

## 4. Exit codes

**This section is the source of truth for the exit code values.** Other documents point here.

| Value | Meaning | Examples |
|---|---|---|
| `0` | Checked; no finding reaches `--fail-on` **among what was checked** (below) | No problems, or warnings only (by default) |
| `1` | Checked; findings reach `--fail-on` (ignored ones do not count) | An expired certificate, a broken link, **the start URL answering 4xx / 5xx** |
| `2` | Usage error; the check did not start | An unknown flag, check ID or rule ID, a URL without a scheme, `--only` together with `--skip`, **a check that this build does not have** (select only implemented ones with `--only`), **no CA could be read from the OS trust store and there is no `--ca-file`** (going on would make every certificate untrusted; on macOS / Windows the OS verifier is used, so this is not judged there unless `SSL_CERT_FILE` or `SSL_CERT_DIR` is set) |
| `3` | Could not check: no usable response for the start URL | The start URL (or where it redirects) cannot be reached (DNS, connect, the TLS handshake, timeout). The start URL (or where it redirects) is still 429 after the retry. The start URL's redirects go into a private address of another host (`private-address`) |

- **If the start URL (or where it redirects) answers 4xx / 5xx, it is `links.broken`** (with the path in
  `detail.chain` if it went through redirects). The response said so, so it is "broken" (§6). The checks that need a crawl are `skipped` (`start-page-broken`). `cert` and `sitemap` still run
- **If the start URL's redirects loop, or its `Location` is broken, it is `redirects.loop` /
  `redirects.bad-location`** (the response said so). The checks that need a crawl are likewise `skipped`
  (`start-page-broken`). When the redirects go beyond `--max-redirects` (`redirects.too-many`), the crawl does not
  go on either and `skipped` is the same, but `redirects.too-many` defaults to `warn`, so on its own the exit code
  is 0 (§2.3)
- These are `fail` at their default severity, so **the exit code is 1**. If the user lowers or hides them with
  `--severity` / `--ignore`, that is followed (it can become 0). The crawl still does not go on, so the selected
  crawling checks always appear in `skipped` as `start-page-broken`. Checks not selected by `--only` / `--skip` are
  `not-selected`
- **If the start URL (or where it redirects) is still 429 after the retry, it is 3** (`could_not_check.code` =
  `rate-limited`). A 429 does not say "broken"; it says "not now". tenken does not answer 0 when it has not checked
  (CLAUDE.md §1)
- **If the start URL's redirects go into a private address of a host other than the start URL's host, it is 3**
  (`could_not_check.code` = `private-address`), also when the start URL's host is itself local (`localhost` →
  `127.0.0.1`, an intranet site → its sign-in host). tenken's safety policy does not connect there (§5.2
  `not_fetched.private`, which counts that URL), so there is no start page to check. The reason names that URL. A
  stop in the crawl, in robots.txt, in a sitemap, on an external link, or in the `http://` probe is not 3: it is
  counted in `not_fetched.private`
- **If the start URL's own host resolves to a private address during the run although its first connection found
  a public address, it is also 3** (`private-address`), for example on a redirect to another path on the same host
  or on the retry of a 429. The reason names the URL not connected to, says that tenken's safety policy does not
  connect to it, and says to check why the host name's resolution changed
- **If the first connection to the start URL went through a proxy** (so tenken did not check whether the start URL's
  host is local, and treats it as not local) **and a later URL of that host, not through the proxy, resolves to a
  private address, it is also 3** (`private-address`), with its own reason: it names the URL, says it resolved to a
  private address and that the first connection went through a proxy, and says to use the proxy, or not, the same
  way for every URL of that host (`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`, §3.6)
- **An expired certificate on the start URL is not 3.** tenken connected and could read the certificate, so it
  reports `cert.expired` (1). 3 is only for not getting as far as reading the certificate
- **0 is about what was checked, not about the whole site.** A run can stop part-way and still be 0: a limit was
  reached (`summary.limits_hit`; the values are in §5.2), URLs were not fetched (`summary.not_fetched`), or a check
  did not run or stopped part-way (`skipped`, e.g. `robots-unavailable`). These do not change the exit code. On a
  run that checked (0 or 1), both formats print them (§5); on 3 the JSON says why in `could_not_check` and the text
  format prints only the sentence on standard error (below). To fail CI on them, read those fields in the JSON.
  `--max-duration` is the point
  after which no new request is sent, not a hard deadline: requests already sent are finished (up to `--timeout`;
  §3.2). A limit is how a user bounds a large site, and failing on it would fail
  such runs every time
- For 2 and 3, one sentence on standard error says what happened and what to do (CLAUDE.md §4)
- **With `--format json`, 3 still prints JSON on standard output** (the reason in `could_not_check`; §5.2), so that
  CI can read the reason. For 2 the check did not start, so no JSON is printed

## 5. Output

### 5.1 Human-readable (`--format text`)

```text
tenken 0.1.0 — https://example.com/ (42 pages crawled; limits: max-depth 3, max-pages 200, max-duration 10m, max-requests none)

FAIL  cert.expiring-soon      example.com:443: expires 2026-11-20 (3 days left, 6% of 47 days)
FAIL  links.broken            https://example.com/old-page → 404
        on https://example.com/          a[href]
        on https://example.com/about     a[href]
WARN  headers.hsts-missing    https://example.com/ and 41 other pages
INFO  redirects.temporary     https://example.com/login → 302 → https://example.com/login/

not fetched: 3 excluded, 12 external, 0 disallowed by robots.txt, 0 because robots.txt was unavailable, 0 over --max-body, 0 over the limits, 0 to private addresses
limits hit: none
skipped: none
ignored: 1 (links.broken)
result: 2 fail, 1 warn, 1 info — exit 1
```

- Ordered by severity (fail → warn → info), then rule ID, then subject. **The same input gives the same output**
  (test-perspectives.md §2.3)
- The same finding (as defined in §5.2) found in several places is one entry listing the places
  (folded into a count when there are too many)
- Percentages are rounded down to whole numbers
- The look of this format (column widths, symbols) may change during v0.1.0. **Machines read the JSON**

### 5.2 JSON (`--format json`)

```json
{
  "schema_version": 1,
  "tool": { "name": "tenken", "version": "0.1.0" },
  "target": "https://example.com/",
  "started_at": "2026-11-17T03:00:00Z",
  "finished_at": "2026-11-17T03:00:41Z",
  "could_not_check": null,
  "summary": {
    "fail": 2, "warn": 1, "info": 1,
    "ignored": { "links.broken": 1 },
    "suppressed": {},
    "pages_crawled": 42,
    "limits_hit": [],
    "not_fetched": { "excluded": 3, "external": 12, "robots_disallowed": 0, "robots_unavailable": 0, "over_max_body": 0, "over_limits": 0 },
    "exit_code": 1
  },
  "findings": [
    {
      "rule": "links.broken",
      "check": "links",
      "severity": "fail",
      "subject": "https://example.com/old-page",
      "message": "https://example.com/old-page returned 404",
      "detail": { "status": 404, "chain": [] },
      "found_on": [
        { "kind": "element", "page": "https://example.com/", "element": "a", "attribute": "href", "value": "/old-page" }
      ]
    }
  ],
  "skipped": []
}
```

**Top level**

| Key | Type | Meaning |
|---|---|---|
| `schema_version` | integer | The version of this schema. `1` now |
| `tool` | `{name, version}` | |
| `target` | string | The URL exactly as the user gave it |
| `started_at` / `finished_at` | string | RFC 3339, UTC |
| `could_not_check` | `null` or `{code, reason}` | Has a value only for exit code 3. `code` is `dns` / `connect` / `tls` / `timeout` / `malformed` (a response arrived but is not readable as HTTP) / `rate-limited` / `private-address` (the start URL's redirects went into a private address of another host, or the start URL's own host, whose first connection found a public address or went through a proxy, resolved to a private address during the run; tenken's safety policy connects to neither; §4). `findings` and `skipped` are then empty |
| `summary` | the table below | |
| `findings` | array | The findings, in the order of §5.1 |
| `skipped` | `[{check, code, reason}]` | Checks that did not run or stopped part-way. `code` is a reason code from §2.3 |

**`summary`**

| Key | Meaning |
|---|---|
| `fail` / `warn` / `info` | The number of findings reported (ignored and suppressed ones excluded) |
| `ignored` | The number hidden by `--ignore`. Rule ID → count |
| `suppressed` | The number dropped by `--severity RULE=off`. Rule ID → count |
| `pages_crawled` | The number of HTML pages crawled. The denominator of "no broken links" |
| `limits_hit` | The limits that were reached: any of `"max-pages"` / `"max-depth"` / `"max-links-per-page"` / `"max-sitemap-urls"` / `"sitemap-nesting"` (an index nested too deep, with children not read) / `"max-duration"` / `"max-requests"`. Empty if none |
| `not_fetched` | The number of URLs not fetched: `excluded` (`--exclude`); `external` (external links not checked because of `--no-external`, and URLs on another origin listed in a sitemap); `robots_disallowed`; `robots_unavailable` (URLs not followed because robots.txt was unavailable and the crawl stopped); `over_max_body` (pages whose body could not be read to the end, so their links were not picked up: beyond `--max-body`, or cut off); `over_limits` (HTML pages not followed because of a limit — their liveness is still checked —; also the targets not checked because of `--max-links-per-page`, which are not fetched, so whether they are HTML is unknown; also the URLs not fetched because of `--max-duration` / `--max-requests`, including chains stopped part-way, whose redirects seen so far are not findings either); `private` (references to private addresses or `localhost`, and to host names that resolve only to private addresses (checked just before connecting, and the checked address is the one connected), and sitemap files on them and redirects into them (a redirect of robots.txt into one makes robots.txt unavailable: §2.3 `robots-unavailable`), with no finding; **a private address is one that the IANA IPv4 and IPv6 Special-Purpose Address Registries mark as not globally reachable**, except the blocks inside it that they mark globally reachable, **plus multicast and the limited broadcast**: for example `0/8`, `10/8`, `100.64/10`, `127/8`, `169.254/16`, `172.16/12`, `192.0.0/24` (but not `192.0.0.9` and `192.0.0.10`), `192.168/16`, `198.18/15`, the documentation ranges (`192.0.2/24`, `198.51.100/24`, `203.0.113/24`, `2001:db8::/32`, `3fff::/20`), `240/4`, `255.255.255.255`, `224/4`, `::`, `::1`, `2001::/23` (but not its globally reachable blocks), `fc00::/7`, `fe80::/10` and `ff00::/8`; an IPv4-mapped IPv6 address is judged as its IPv4 address, which is what gets connected to (the registry marks `::ffff:0:0/96` itself not globally reachable); `192.88.99/24` (no value in the registry; except `192.88.99.2`, which is private) and `2002::/16` (6to4, N/A) are not private; `2001::/32` (TEREDO, N/A in the registry) and `2001:10::/28` (no value) are private, as tenken's choice, following `2001::/23`; not checked for a request that goes through a proxy (§3.6: the proxy connects, so the destination cannot be checked); tenken does not reach into the CI machine that a public page points at; a redirect into one is stopped before it and counted here, and the chain up to there is looked at by `redirects.*`; **the only exception is the host of the start URL as the user wrote it** (not where it redirects; the host name compared case-insensitively, without a trailing dot, without the port): if it is a private IP or `localhost`, or the first connection to it finds only private addresses, its URLs are fetched for the whole run; otherwise it is like any other host, also when the same name later resolves only to private addresses; **the start URL's own chain and the `http://` probe of §2.1 follow the same rule**: a redirect of the start URL's own chain into a private address of another host is exit 3, §4 `private-address`) |
| `exit_code` | This run's exit code |

**A finding (`findings[]`)**

| Key | Meaning |
|---|---|
| `rule` / `check` / `severity` | The rule ID of §2.2, its check, and the severity after `--severity` |
| `subject` | **What the finding is about.** For `cert.*`, `host:port` (IPv6 in brackets, like `[::1]:443`: the form of RFC 3986 §3.2.2). For `headers.*`, **the origin** (`https://example.com`: one finding for the site, not one per page, with the pages in `found_on`). For `sitemap.robots-*` / `sitemap.missing` / `sitemap.invalid` / `sitemap.rate-limited`, that URL (robots.txt / the sitemap). For `links.mixed-content`, the `http://` resource. Otherwise the URL with the problem. `--ignore`'s regexp is matched against this |
| `message` | One sentence for a human |
| `detail` | Values per rule (the table below) |
| `found_on` | Where it was found (the table below). Empty for `cert.*`, `sitemap.robots-*`, `sitemap.missing`, `sitemap.invalid` and `sitemap.rate-limited` (the subject itself is the place) |

**The same finding means the same `rule` and `subject`.** It is one entry, and the places are gathered in `found_on`.

**The shape of `found_on[]`** (decided by `kind`)

| `kind` | Keys | Used by |
|---|---|---|
| `element` | `page`, `element`, `attribute`, `value` | `links.*`, `redirects.*` found in a page |
| `header` | `page`, `header`, `value` | `headers.*` (`header` is the header's name, `value` the value seen on that page, or `null` if absent). **The value can differ per page, so it is kept here** |
| `sitemap` | `sitemap`, `loc` | `sitemap.url-*`, `redirects.*` followed from a sitemap |
| `start` | `url` | The start URL itself (`links.broken`, `redirects.*`; also `redirects.no-https` when the start URL is http) |
| `probe` | `url` | A URL tenken checked on its own (the `http://<host>/` of `redirects.no-https` when the start URL is https) |

**The shape of `detail`**

| Rule | Keys |
|---|---|
| `cert.*` | `not_before`, `not_after` (RFC 3339), `days_left` (the time remaining divided by 24 hours, rounded down; negative when expired), `lifetime_days`, `issuer`, `subject_alt_names` (array) |
| `cert.untrusted` | In addition, `reason`: `issuer-not-found` (following the signatures of the certificates sent does not reach a trusted CA: a missing intermediate, an unknown CA, a different certificate with the same name; also when reaching one cannot be confirmed) or `invalid-chain` (the signatures reach a trusted CA, but the chain breaks a rule: an intermediate that is not a CA, the wrong usage, an intermediate or root expired or not yet valid at the time, and so on). It does not depend on the order the certificates were sent in. **Whether a trusted CA is reached is confirmed by whether verification succeeds at any of three times for each certificate sent: its start, its end, and "now" (the time of `cert.untrusted` in §2.2 — the nearer end if the certificate is outside its validity — clamped into that certificate's validity period)** (the validity period of a root in the trust store cannot be read; a root issued after the intermediate, expired now while the intermediate is still valid, cannot be confirmed and is `issuer-not-found`) |
| `redirects.*` | `chain`: `[{url, status}]` (from the first request to the last response) |
| `links.broken` / `links.forbidden` / `sitemap.url-broken` | `status`, `chain` (the path if it went through redirects, otherwise empty) |
| `links.unreachable` / `sitemap.url-unreachable` | `reason`: `dns` / `connect` / `tls` / `timeout` / `malformed` (a response arrived but is not readable as HTTP) |
| `links.rate-limited` | `retry_after_seconds` (`null` if absent) |
| `headers.*` | An empty object `{}`. The per-page values are in `found_on[].value` |
| `sitemap.robots-broken` | `status` |
| `sitemap.robots-unreachable` | `reason`: `dns` / `connect` / `tls` / `timeout` / `malformed` / `redirect` (unusable redirects, including one into a private address) |
| `sitemap.robots-rate-limited` / `sitemap.rate-limited` / `sitemap.url-rate-limited` | `retry_after_seconds` (`null` if absent) |
| `sitemap.invalid` | `problem` (one sentence on what was broken) |
| Anything else | An empty object `{}` |

**The compatibility promise** (kept from v0.1.0 on: tenken is built into CI)

- While `schema_version` stays the same, **no key is removed, and no key changes its meaning or its type**.
  Adding keys is allowed. A breaking change raises `schema_version`
- The promise covers: key names, types, rule IDs, `kind`, reason codes (the values of `code` and `reason`)
- **It does not cover the sentences in `message`, `skipped[].reason`, `could_not_check.reason`, and
  `sitemap.invalid`'s `problem`.** They are for humans and their wording may change. Machines read the codes
- With no findings, the shape is the same (`findings: []`)

## 6. Words

**This table decides what each word means.**

| Word (`--help`, output, JSON, `docs/`) | What it means here; it is never used for anything else |
|---|---|
| `fail` / `warn` / `info` | **The severity of one finding.** Not the result of the whole run |
| finding | What one rule said about one subject |
| subject | What a finding is about (a URL, or `host:port` for a certificate) |
| unreachable | **A request was sent and no usable response came back** (DNS, connect, TLS, timeout, a response not readable as HTTP = `malformed`). Not a status code |
| broken | **The response said so** (4xx, 5xx, a loop, an expired certificate). The same for the start URL |
| could not check | **tenken itself could not do its job**: no usable response for the start URL (unreachable, still 429 after the retry, or its redirects went into a private address of another host; 3), or wrong arguments (2). Never 1. Wider than unreachable: a 429 is not unreachable, but it is could-not-check |
| skipped | A check did not run, or stopped part-way. Always with a reason code |
| not fetched | URLs not fetched because of an exclusion, robots.txt, a limit, or a private address (§5.2 `not_fetched.private`), and pages not followed because of a limit (their HTML was not read to go further; their liveness is checked). Not `unreachable`: no connection was made. Always counted |
| ignored | Known findings the user hid with `--ignore` |
| suppressed | Findings the user dropped with `--severity RULE=off` |
| origin | **The same scheme, host and port** (WHATWG URL). Never "domain" or "host" instead |
| external | Another origin. Never "third-party" |
| crawl | Fetching HTML and following what it refers to, within the limits. One liveness check of a link is not a crawl. robots.txt applies to the crawl and to liveness checks of same-origin URLs (not to external ones; §2.1) |

**The word `error` is not used for any of the meanings above**, because it would no longer say which row it means.

## 7. Not decided in v0.1.0

Kept in [`discovery.md`](discovery.md):

- Reporting only the difference from the previous run (discovery.md §1). Not in v0.1.0. With the JSON compatibility promise
  (§5.2), an outside tool can take the difference
