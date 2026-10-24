# Go

⚠ **`MUST` = required, `SHOULD` = default, `MAY` = optional.**

⚠ **The language decision is [`docs/design.md §1`](../../docs/design.md); the package
boundaries are [`docs/design.md §2`](../../docs/design.md).**
⚠ **This file says how to write Go inside those boundaries.** ⚠ It does not restate them.

## Dependencies

- MUST: ⚠ **Standard library and `golang.org/x/net` only.** ⚠ **Anything else needs a reason in `docs/design.md` § 1 first**
  (`CLAUDE.md` § 3).
  ⚠ **Grounds: one static binary with nothing to install** (`docs/design.md` § 0.1), ⚠ **and every
  dependency is code that parses a hostile site's bytes on the user's machine.**
- MUST: ⚠ **`go.sum` is committed. ⚠ `go mod tidy` leaves no diff.**

## Errors

- MUST: ⚠ **Return errors; never `panic` on input from outside.** ⚠ **A site that sends garbage is
  a finding or `could not check`, never a crash** (`verification.md` § Priority, clause 3).
- MUST: ⚠ **Wrap with `%w` and keep the kind recoverable with `errors.As` / `errors.Is`.**
  ⚠ **Grounds: `docs/cli-reference.md` § 6 separates `unreachable` (dns / connect / tls / timeout / malformed) from
  `broken`, and § 5.2 promises the reason codes.** ⚠ **An error flattened to a string cannot be
  told apart afterwards.**
- MUST NOT: ⚠ **Never compare error strings.** ⚠ They are not part of any contract.

## Time, network, output

- ⚠ **Who may read the clock, print, exit, or open a connection is design.md §2's § Walls, enforced by
  `internal/walls/boundary_test.go`.** ⚠ Not restated here.
- MUST: ⚠ **Every request takes a `context.Context` and honours its deadline.** ⚠ **Grounds:
  `--timeout` (cli-reference.md § 3.2), and a timeout is `unreachable`, not a hang.**
- MUST: ⚠ **Read a body through a size limit (`--max-body`)** — ⚠ **never `io.ReadAll` on a
  response.** ⚠ **The input is hostile.**
- MUST: ⚠ **Iterate maps in sorted order wherever the order reaches output.** ⚠ **Grounds: the
  same input gives the same output** (`docs/test-perspectives.md` § 2.3); Go randomises map order on
  purpose.

## Concurrency

- MUST: ⚠ **No goroutine outlives the call that started it.** ⚠ **Every one is joined
  (`sync.WaitGroup` / `errgroup`-shaped code) before the call returns.**
  ⚠ **Grounds: the exit code and the JSON summary are decided once the crawl returns**
  (`docs/cli-reference.md` § 4, § 5.2). ⚠ **A goroutine still fetching after that is work that is neither
  counted nor reported — the silent rejection `rules/evidence.md` forbids.**
- MUST: ⚠ **`go test -race` passes.** ⚠ CI runs it.

## Tests

- MUST: ⚠ **Talk only to servers the test started itself** (`internal/testsupport`, 127.0.0.1).
  ⚠ **No hostname literal resolves in a test.**
- MUST: ⚠ **Generate certificates in the test** (`testsupport.NewCA`). ⚠ **Never commit one**
  (`.gitignore` refuses `*.pem` / `*.crt` / `*.key`).
- SHOULD: ⚠ **Table tests, one row per perspective in `docs/test-perspectives.md`, named after it**
  — ⚠ **so a perspective with no row is visible.**
- MUST: ⚠ **A test name says the claim** (`TestExpiredLeafIsReadableAndFailsAsExpired`), ⚠ **not
  the function** (`TestVerify2`).

## Naming

- MUST: ⚠ **Use `docs/cli-reference.md` § 6's words** — `Finding`, `Severity`, `Subject`, `Unreachable`,
  `Broken`, `CouldNotCheck`, `Skipped`. ⚠ **Never `Error` for any of them** (§ 6 says why).
- MUST: ⚠ **Rule IDs live in `internal/finding` only, and a test holds them to `docs/cli-reference.md` § 2.2.**

## Format and static checks

- MUST: ⚠ **`gofmt -l .` prints nothing and `go vet ./...` is clean.** ⚠ CI runs both.
