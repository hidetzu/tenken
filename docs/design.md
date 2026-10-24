# tenken design

⚠ **This file gathers "what to build", "how it is built now" and "why" in one place.** It is not a history of decisions.
⚠ **What a user sees is [`cli-reference.md`](cli-reference.md), what may be claimed is [`test-perspectives.md`](test-perspectives.md) §6, and how to work is [`../CLAUDE.md`](../CLAUDE.md).** They are not written here.
⚠ **This file is an owner-only path** ([`.claude/rules/git.md`](../.claude/rules/git.md) § Permission). To change what to build (§0), write it as a proposal in [`discovery.md`](discovery.md) §0.
⚠ **Decisions live in two places, and each holds something different**: [`cli-reference.md`](cli-reference.md) holds the decisions about what a user sees (on the row they concern); this file holds what to build, the current design, and the reasons for it. What is not decided yet, and the known limitations, are in [`discovery.md`](discovery.md). Do not leave an open item like "decide how to handle it" in another document (such as [`test-perspectives.md`](test-perspectives.md)). Decide what can be decided on the spot, and write what cannot in discovery.md.

---

## 0. What to build

### 0.1 Concept

> **A CLI that checks a web site in one run and can fail CI**

- The user runs it against their own site, locally or in CI
- It gives the result in a form a human reads and a form a machine reads (JSON)
- If there is a problem, it ends with a non-zero exit code. CI fails on that
- Regular monitoring is done by running the CLI from GitHub Actions' `schedule` or similar. **No resident service is built**

### 0.2 Where it wins

- **Everything in one run.** It does not have to beat the dedicated tools on the depth of each check
- **Easy to use in CI.** Exit codes, machine-readable output, thresholds, ignoring known problems
- **Light to install.** One binary (for now, `go install`)

### 0.3 What v0.1.0 builds

Checks that run with no browser, from a single binary alone:

| Check | What it covers |
|---|---|
| Broken links | Whether what a page's links, images, scripts and CSS refer to is not 4xx / 5xx / unreachable. An `http://` reference on an https page (statically visible mixed content) is a warning |
| Redirects | Multi-hop (more hops than the threshold), loops, a missing move from http to https, and temporary redirects (302/307) reported as info |
| TLS certificates | The days left until expiry (warn and fail by threshold. The default is a fraction of the validity period, and it can be overridden in days: the values are [`cli-reference.md`](cli-reference.md) §3.2), host name mismatch, a defective chain, expired |
| Security headers | Whether HSTS, CSP, X-Content-Type-Options and the like are present, and plainly wrong values |
| sitemap / robots | Whether robots.txt and sitemap.xml can be fetched. Whether the URLs listed in the sitemap return 200. Whether the sitemap lists URLs that robots disallows |

Shared behaviour:

- It takes exactly one start URL. Several sites are run several times on the CI side
- The crawl stays inside the same origin as the start URL (same scheme, host and port; a subdomain is external). Limits on depth and page count, concurrent connections, and the interval can be set
- External links are checked only for "is the target alive", and the crawl does not go beyond them
- It obeys robots.txt (when crawling)
- Each result carries a severity (fail / warn / info), and the exit code is "non-zero if there is even one fail"
- It takes settings for use in CI: thresholds, ignoring known problems, overriding a severity (raising warn to fail), choosing checks (for example, only the certificate), and naming a trusted CA (the equivalent of `--ca-file`)

### 0.4 What v0.1.0 does not build

- **Checks that need a browser**: the log after rendering (JS errors, resources that failed to load or were blocked, mixed content only visible after rendering), Core Web Vitals.
  From v0.2 on, an option like `--browser` will use the local Chrome over CDP. **No browser is bundled into the single binary**
- Certificate revocation (OCSP / CRL), and grading of TLS versions and cipher suites (testssl.sh's territory)
- Checking domain expiry and DNS (a candidate for v0.2 or later)
- Accessibility (the equivalent of axe-core; a candidate for v0.2 or later)
- Resident monitoring, a web UI, notifications (Slack, email). Notifications are done on the CI side
- Crawling pages that need authentication
- Automated fixes

## 1. Language and dependencies

**Go.** The dependencies are the standard library and `golang.org/x/net` only (`.claude/rules/go.md`). To add anything else, write the reason in this file first.

Reasons:

- tenken's core, "read the contents of a certificate that failed verification too, and call an expired one expired", holds with Go's standard library (`InsecureSkipVerify` + `VerifyConnection` hand over the chain the server sent, and tenken verifies it itself by passing `CurrentTime` to `x509.Certificate.Verify`)
- A server it cannot connect to is fatal for a checking tool: it would "call a site that is not broken unreachable" (`CLAUDE.md` §1). Go's `crypto/tls` speaks TLS 1.2 and 1.3 with ECDSA and RSA (including RSA-PSS) certificates
- `net/http/httptest` starts an HTTPS server inside a test, with no reliance on anything external ([`test-perspectives.md`](test-perspectives.md) §1)

Consequences: the binary is a few MB (`CGO_ENABLED=0`, statically linked, cross-built for 5 targets). A missing intermediate certificate and an unknown certificate authority cannot be told apart, by the nature of TLS, so both are one rule, `cert.untrusted` ([`cli-reference.md`](cli-reference.md) §2.2).

## 2. Layers and walls

```text
cmd/tenken/            main. Only calls internal/cli.Run and exits with its return value
internal/cli/          Flag parsing, output (text / JSON), exit codes. ⚠ The only place for sentences a human reads and for exit codes
internal/finding/      The types for Finding, severity, rule ID, found_on. ⚠ The only place for the list of rule IDs (checked against cli-reference.md §2.2 by internal/walls/rules_test.go)
internal/fetch/        ⚠ The only place that goes out to the network. One HTTP round trip (does not follow redirects), and extracting the TLS certificate
internal/crawl/        What to fetch (origin, depth, page count, robots.txt, exclusions (`--exclude`), reading the sitemap). Counts what was not fetched
internal/check/<name>/ cert / redirects / links / headers / sitemap. ⚠ Only returns Findings from what it received. Does not print, does not exit
internal/testsupport/  Tests only: local HTTP / HTTPS servers, certificate generation, a fixed clock
```

The direction of dependencies (there is no reverse direction). **This table is the source of truth, and a test checks that it is the same as `mayImport` in `internal/walls/boundary_test.go`** (`TestADRTableMatchesMayImport`):

| Package | Packages in this module it may import |
|---|---|
| `cmd/tenken` | `internal/cli` |
| `internal/cli` | `internal/crawl`, `internal/check/*`, `internal/finding`, `internal/fetch` (to assemble them) |
| `internal/crawl` | `internal/fetch`, `internal/finding` |
| `internal/check/*` | `internal/fetch`, `internal/finding` (does not import another check) |
| `internal/fetch`, `internal/finding`, `internal/testsupport` | none |

- **Time is passed as an argument** (`fetch` and `check/cert` take `now func() time.Time`). Only `cli` reads the clock directly
- **`fetch` is taken as an interface.** `crawl` and `check/*` do not know the concrete HTTP client
- There is no API exposed outside `internal/` (no promise to be used as a library)
- To add a layer, change this table first (in the same PR as the walls table is fine: that is what "first" in `CLAUDE.md` §3 means)

Why this shape: "tests do not go outside" is kept in one place, `fetch`. The distinction between "could not check" and "broken" (`CLAUDE.md` §1) is decided in one place: `fetch` returns "there was a response / there was none (and why)", and `cli` and `check` decide how to say it. The checks depend only on `finding` and on `fetch`'s interface, so they could be built one at a time.

### Walls (`internal/walls/boundary_test.go`)

Tests keep them, not sentences. The tests judge by import path (they do not mistake an alias, a dot import, or a local variable that hides a name).

| Rule | Covers |
|---|---|
| **Only `internal/fetch` imports packages that can open a connection** (except `internal/testsupport`, the test tool) | `net`, `crypto/tls`, `net/http` and below it, `net/smtp`, `net/rpc` and below it, `log/syslog`, `golang.org/x/net/` (except `golang.org/x/net/html`) |
| **Only `internal/cli` and `cmd/` print and exit** | `fmt.Print*`, `os.Exit` / `os.Stdout` / `os.Stderr`, all of `log`, `log/slog` and `log/syslog`, the built-in `print` / `println` |
| **Only `internal/cli` reads the clock** | `time.Now` / `time.Since` / `time.Until` |
| **The direction of dependencies** | As in the table above. A directory not in the table is a violation by that alone (it also scans outside `cmd/` and `internal/`) |
| **`internal/testsupport` is for tests only** | Imported by anything other than a `_test.go`, it is a violation |
| **No dot imports** | In any package |
| **Not imported** | `C` (cgo), `unsafe` (including `//go:linkname`), `syscall`, `os/exec`. Scanning imports cannot see beyond them, so the entrance itself is closed |
| **Production code does not import `testing`** (except `internal/testsupport`) | Closes the way to change behaviour only under a test with `testing.Testing()`. ⚠ There are other ways to know whether it is under a test (`flag.Lookup("test.v")`, `os.Args[0]`). The walls import nothing, so the walls cannot be fooled, but a behaviour test can. Review looks at that |
| **No `init()`** | It runs just by being imported, and can swap a value inside the production binary or stop the walls inside a test. State is passed explicitly by the caller |
| **No `//go:debug`** | Like `godebug` in `go.mod`, it changes the behaviour of `crypto/x509` and `crypto/tls` |
| **Every file builds on every target** | For each target CI builds (read from the cross-compile list in ci.yml), ask `go/build`; a file left out of the build is a violation (`TestEveryTargetBuildsEveryFile`). Do not rewrite the rules for build constraints or file name suffixes by hand (rewritten, they drift from go/build) |
| **Everything that goes into the binary is something the walls read** | For each CI target, collect the packages reachable from `cmd/tenken` by asking go/build (the dependency tree, not a directory scan), and confirm that each is in a place the walls scan, that the walls read all its Go files, and that it has no non-Go build input (assembly, C, .syso and so on) (`TestEverythingThatShipsIsReadByTheWalls`). Non-Go input can make system calls and connections without going through an import, and the walls cannot read its contents |
| **The dependencies are the standard library and `golang.org/x/net` only** | Reads `require` in `go.mod`, and allows no `replace` (`TestGoModRequiresOnlyXNet`. It reads `require(` even with no space) |
| **The walls themselves are owner-only, and import nothing** | All the wall tests are in `internal/walls/`, and the whole directory is an owner-only path in merge-gate. The walls do not import this module's packages, and read values by parsing the source (importing them would run the `init()` of packages outside the walls inside the wall tests. `TestWallsImportNothingFromThisModule`). ⚠ What the walls see is the declared literals, not the values at run time: rewriting `Rules` in a package variable's initialiser or inside a function, or hiding `Exit*` in a constant inside a function, is invisible to the walls. Review looks at that |
| **No `go.work`** | `TestNoGoWork` (a workspace can do the same as a replace in go.mod) |
| **No `godebug` in `go.mod`** | It changes the behaviour of `crypto/x509` and `crypto/tls`, so writing the reason in this file comes first |

- The boundary is drawn **by import** (not by whether a function that opens a connection was called). So `check/*` cannot use even `http.Header` or `http.StatusOK`. `fetch` returns its own types (the response status, headers, the certificate chain), and `check/*` looks only at those
- The tests reproduce each way of slipping through in a small source, and confirm that the rule catches it (`TestBoundaryRules`)
- **What the tests do not see (review sees it)**: `fetch` returning `net/http` types or values as they are (`type Header = http.Header` and so on), making standard output with `os.NewFile(1, …)`, and a library default reading the clock (`x509.VerifyOptions.CurrentTime` empty, `tls.Config.Time` nil). For the clock, the tests check behaviour with a fixed clock (`testsupport.Now`), so if a default is read, the results do not match and the test fails
