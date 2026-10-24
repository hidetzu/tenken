# fixture — a site to check tenken against

A small site with problems placed on purpose. `check.sh` runs tenken against it and asserts that the expected
findings come out, and that the pages and links it lists with no problem are not reported as `links.broken`.
Without a URL, the site's own pages are served on your machine, and only its external links leave it (below).

It is run by hand, not in CI (the external tier of [`.claude/skills/verify/SKILL.md`](../.claude/skills/verify/SKILL.md)).
The release workflow ([`.github/workflows/release.yml`](../.github/workflows/release.yml)) also serves it with `serve.py` and runs
the built archive against it with `--no-external`, so nothing leaves the runner; it asserts that the run worked, not which findings came out.

```sh
fixture/check.sh            # serve fixture/ on 127.0.0.1 and check it
fixture/check.sh <URL>      # check a copy of fixture/ served somewhere else
```

- Run it from the repository root.
- Needs `jq`. Without a URL it also needs `python3`, which serves the site with [`serve.py`](serve.py).
- Runs the binary at `TENKEN_BIN` (default `./tenken`). If that path is not executable, it first builds it
  there with `go build ./cmd/tenken`. ⚠ An executable already there is used as it is and never rebuilt,
  so after changing the code, remove `./tenken` (or point `TENKEN_BIN` at a fresh build) first.
- Ends with `fixture/check.sh: PASS`, or with `fixture/check.sh: FAIL` and exit status 1. It exits 2 when the
  local server does not start. When a step fails before the end (the build, `jq`, output `jq` cannot read),
  it stops with a non-zero status and prints no verdict.

## What is in it

- Every problem here is placed on purpose. The expected findings are in [`check.sh`](check.sh).
  When you add something here, change the expectations in `check.sh` in the same PR.
- `robots.txt` and `sitemap.xml` contain `{{ORIGIN}}`, which has to become the origin they are served from:
  a `Sitemap:` line and a `<loc>` must be absolute URLs. `serve.py` does this replacement. A copy served
  elsewhere needs the same replacement.
- The external links (`example.org`, `www.example.com`) go out to the public network, through real DNS and TLS.
  `check.sh` asserts that they are not reported as `links.broken`, so an answer that tenken reports as
  `links.broken` ([`docs/cli-reference.md`](../docs/cli-reference.md) §2.2) fails the run.
  It does not assert that they were reached. When either is `links.unreachable`, it prints a `note` before the
  verdict, saying the run did not observe the real network.
- The link to `http://localhost:8080/` is not fetched and is counted in `not_fetched.private`
  (`check.sh` asserts exactly one). A private address is fetched when it is the start URL's host
  ([`docs/cli-reference.md`](../docs/cli-reference.md) §5.2), so check a copy served on this machine
  through `127.0.0.1`, not `localhost`.

## What it does not assert

- Anything the serving server decides: the headers, the shape of a 404, how it answers `HEAD`.
- The sitemap and robots.txt expectations, when tenken reports `sitemap.robots-missing` (`/robots.txt` at the origin's
  root answers 4xx other than 429, as when the copy is served under a path, such as a GitHub Pages project site). `check.sh` prints a
  `note`, skips them, and asserts `pages_crawled == 3` instead (`private/` is crawled, since no robots.txt applies).
  A copy served under a path whose root serves a `robots.txt` is not skipped, and those expectations fail.
