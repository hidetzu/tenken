---
name: verify
description: Run tenken's checks in order — inner (gofmt, go vet, go test -race, docs-check, tools tests), final gate (the built binary against local servers), external (the real web, owner's call) — and return PASS / FAIL / NOT-VERIFIED in the shape rules/verification.md requires. Use before opening a PR and after every fix.
---

# Verify — tenken

⚠ **The contract is [`.claude/rules/verification.md`](../../rules/verification.md).**
⚠ **This file names only the commands.** ⚠ **When the two disagree, the contract wins.**

⚠ **Return the `PASS` / `FAIL` / `NOT-VERIFIED` block from the contract, verbatim in shape.**
⚠ **`Regression guard` and `Mutation check` are never left empty.**

Requires Go (the version in `go.mod` or newer) and `node` for the document and tool checks.

---

## The three tiers

| Tier | Entry point | ⚠ What it cannot show |
|---|---|---|
| **Fast / inner** | `gofmt -l .`, `go vet ./...`, `go test -race ./...`, `node .claude/tools/docs-check.mjs`, `node --test ".claude/tools/*.test.mjs"` | ⚠ **Nothing about the binary a user runs.** Every Go case is a package against `internal/testsupport`'s local servers |
| **Final gate** | `go build -o ./tenken ./cmd/tenken`, then drive `./tenken` against local servers started for the run | ⚠ **Nothing about real sites** — servers we wrote and certificates we made |
| **External** | `fixture/check.sh` with no argument (it serves `fixture/` on 127.0.0.1 with `fixture/serve.py`; the page's external links go over the real network), run by hand, not in CI, or ⚠ **a real web site we did not write** (`docs/test-perspectives.md` § 4) | ⚠ **It reaches the public network: the fixture is ours to hit; anyone else's site is the owner's call every time** (`docs/test-perspectives.md` § 4). ⚠ **It is not evidence about our code when the site itself changed** |

⚠ **CI runs the fast tier and builds for every target in the matrix.** ⚠ **"It builds for
Windows" is what CI observes. ⚠ "It works on Windows" is not** — say "compiled".

---

## 1. Fast / inner

```sh
gofmt -l .                          # prints files it would reformat; silent = clean
go vet ./...
go test -race ./...                 # ⚠ the runner announces the count. Never write it down
node .claude/tools/docs-check.mjs
node --test ".claude/tools/*.test.mjs"
```

⚠ **Stop and go back at the first failure** (contract, § Order).

### Run one named case only

```sh
go test ./internal/testsupport -run '^TestExpiredLeafIsReadableAndFailsAsExpired$' -v
node --test --test-name-pattern="owner-only" ".claude/tools/*.test.mjs"
node .claude/tools/docs-check.mjs --only=links
```

⚠ **`-run` is a regular expression and can match nothing.** ⚠ **`ok … [no tests to run]` is not a
pass** — anchor with `^…$` and use `-v` to see the case ran.

### Count without running

```sh
grep -rhoE '^func (Test|Fuzz)[A-Za-z0-9_]+' --include='*_test.go' . | wc -l   # ⚠ compiles nothing; top-level declarations only
node .claude/tools/docs-check.mjs --list
```

⚠ **`grep` counts declarations, not what the runner executes** (a subtest, a skipped test).
⚠ **The number in a report is the runner's** — `go test -v ./... | grep -c '^--- PASS'` after a run.

---

## 2. Final gate

⚠ **Never point the binary at a real site here.** ⚠ **Local servers only** (contract, § An
exercise must not change the world; `docs/test-perspectives.md` § 1).

```sh
go build -o ./tenken ./cmd/tenken
./tenken --version                  # expect: "tenken <version>", exit 0
./tenken --nope; echo "exit=$?"     # expect: exit 2, nothing on stdout, one sentence on stderr
```

⚠ **Confirm the binary under test is the one just built** (contract): build in the same shell,
every time. ⚠ `./tenken` is gitignored.

⚠ **The five checks' end-to-end cases are `internal/cli`'s tests**: they run the whole CLI against
local servers started with `internal/testsupport` and assert the exit code and the JSON against
`docs/cli-reference.md` § 4 / § 5.2 (`docs/test-perspectives.md` § 6 names them; § 1 runs them).
⚠ **Here, run the binary just built once against `fixture/` on 127.0.0.1.** ⚠ **`--no-external` is not what
keeps it on the machine** (contract, § An exercise must not change the world): every proxy variable points at
`127.0.0.1:1`, where nothing listens, so any request for another host fails here, and the check below asserts that
none was tried. ⚠ The release workflow runs the same thing on its archive (`.github/workflows/release.yml`).

```sh
work="$(mktemp -d)"
python3 fixture/serve.py "$work/port" & server=$!
for _ in $(seq 50); do [ -s "$work/port" ] && break; sleep 0.1; done
[ -s "$work/port" ] || { echo "fixture/serve.py did not start"; kill "$server"; }
env -u http_proxy -u https_proxy -u no_proxy HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 NO_PROXY= \
  ./tenken --format json --no-external "http://127.0.0.1:$(cat "$work/port")/" > "$work/out.json" 2> "$work/err.txt"
echo "exit=$?"
kill "$server"
jq -c '{version: .tool.version, pages: .summary.pages_crawled}' "$work/out.json"; ./tenken --version
grep -c 'a proxy is in use' "$work/err.txt"; jq '[.findings[] | select(.rule == "links.unreachable")] | length' "$work/out.json"
# expect: exit=1 (fixture/ has problems on purpose); version equals the one --version prints; pages ≥ 1;
#         0 and 0 on the last line (no request was tried for another host)
```

---

## 3. External

⚠ **Ask the owner first, every time.** Then run the built binary against the sites the owner
named, ⚠ **record when, which version, which site**, and file any false positive or miss as a
perspective in `docs/test-perspectives.md` § 3 with a local test (§ 4 there).

---

## Mutation check, concretely

- Go: change the line the fix touched (invert the condition, drop the guard), run the one case
  with `-run '^…$' -v`, and ⚠ **read the failure text** — ⚠ **it must name the claim, not a
  compile error.** Restore with `git checkout -- <file>` ⚠ **only on a file you own in this branch.**
- Boundary rules (`internal/walls/boundary_test.go`): add a throwaway file under `internal/check/` that
  breaks the rule, watch the named test fail, delete the file.
