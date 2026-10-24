#!/usr/bin/env bash
# Runs tenken against the fixture site and asserts the expected findings (the external tier: .claude/skills/verify/SKILL.md).
# Run by hand from the repository root; CI does not run it. See fixture/README.md.
# Usage: fixture/check.sh          serve fixture/ on 127.0.0.1 (fixture/serve.py, python3) and check it
#        fixture/check.sh <URL>    check a copy of fixture/ served elsewhere ({{ORIGIN}} in robots.txt and sitemap.xml
#                                  must already be replaced with that origin)
#        TENKEN_BIN=<binary>       the binary to run (default ./tenken; built with go build if not executable)
# ⚠ The page's external links (example.org, www.example.com) go out to the public network (real DNS and TLS).
#   Reaching them is not asserted (the result must not depend on a third party being up); when they are
#   unreachable, a note says so before the verdict.
# ⚠ Only findings that do not depend on the serving server are asserted: headers and how HEAD is answered are its call.
#   When tenken reports sitemap.robots-missing (e.g. a copy served under a path), the sitemap / robots
#   expectations are skipped, and a note says so.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
url="${1:-}"
if [ -z "$url" ]; then
  # ⚠ serve.py binds the port itself (port 0) and writes it to portfile only after binding. There is no gap between
  #   choosing a port and binding it, so whatever answers on it is our server (a FAIL against some other process
  #   must not read as "the fixture does not match").
  #   portfile lives in a 0700 directory only we can write (directly in /tmp, another process could plant one
  #   between removing and writing it).
  portdir="$(mktemp -d)"
  portfile="$portdir/port"
  python3 "$here/serve.py" "$portfile" &
  server=$!
  trap 'kill "$server" 2>/dev/null || true; rm -f "$portfile" "$portfile.tmp"; rmdir "$portdir" 2>/dev/null || true' EXIT
  port=""
  for _ in $(seq 50); do
    kill -0 "$server" 2>/dev/null || { echo "fixture/check.sh: the local server (fixture/serve.py) exited before it started serving" >&2; exit 2; }
    [ -s "$portfile" ] && { port="$(cat "$portfile")"; break; }
    sleep 0.1
  done
  [ -n "$port" ] || { echo "fixture/check.sh: the local server (fixture/serve.py) did not start serving after 50 checks" >&2; exit 2; }
  url="http://127.0.0.1:${port}/"
fi
bin="${TENKEN_BIN:-./tenken}"
if [ ! -x "$bin" ]; then
  go build -o "$bin" ./cmd/tenken
fi
out="$(mktemp)"
set +e
"$bin" --format json "$url" > "$out"
code=$?
set -e
echo "tenken exit code: $code"
jq -r '.summary | "pages_crawled \(.pages_crawled)  fail \(.fail)  warn \(.warn)  info \(.info)  not_fetched \(.not_fetched | tojson)"' "$out"
jq -r '.findings[] | "\(.severity)\t\(.rule)\t\(.subject)"' "$out" | sort
fail=0
expect() { # expect <rule> <subject-suffix>
  if jq -e --arg r "$1" --arg s "$2" '.findings[] | select(.rule == $r and (.subject | endswith($s)))' "$out" > /dev/null; then
    echo "ok    $1 $2"
  else
    echo "MISSING $1 $2"; fail=1
  fi
}
forbid() { # forbid <rule> <subject-suffix>
  if jq -e --arg r "$1" --arg s "$2" '.findings[] | select(.rule == $r and (.subject | endswith($s)))' "$out" > /dev/null; then
    echo "UNEXPECTED $1 $2"; fail=1
  else
    echo "ok    no $1 $2"
  fi
}
# Wherever it is served: the broken links (a page and an image), the one private reference, and the listed links with no problem
expect links.broken /missing.html
expect links.broken /assets/nope.png
forbid links.broken /ok.html
forbid links.broken /assets/style.css
forbid links.broken /assets/pic.png
forbid links.broken example.com/
forbid links.broken example.org/
[ "$(jq '.summary.not_fetched.private' "$out")" -eq 1 ] && echo "ok    private == 1" || { echo "BAD private"; fail=1; }
# robots.txt and sitemap.xml are read at the origin's root (/robots.txt, /sitemap.xml; RFC 9309, sitemaps.org).
# ⚠ A copy served under a path (a GitHub Pages project site, for example) has another site at the root, so the
#   fixture's robots.txt / sitemap.xml are not read. The sitemap / robots findings and counts are asserted only
#   when tenken did not report sitemap.robots-missing (the root's /robots.txt did not answer a 4xx other than 429).
if jq -e '.findings[] | select(.rule == "sitemap.robots-missing")' "$out" > /dev/null; then
  echo "note  /robots.txt at the origin root answered 4xx (sitemap.robots-missing; a project Pages site?): sitemap / robots expectations skipped"
  [ "$(jq '.summary.pages_crawled' "$out")" -eq 3 ] && echo "ok    pages_crawled == 3 (private/ is crawled: no robots.txt applies)" || { echo "BAD pages_crawled"; fail=1; }
else
  expect sitemap.url-broken /gone.html
  expect sitemap.url-disallowed /private/secret.html
  expect sitemap.url-external example.com/
  forbid sitemap.missing /sitemap.xml
  # The one disallowed URL is /private/secret.html (both linked and in the sitemap); not_fetched counts it once per URL
  [ "$(jq '.summary.not_fetched.robots_disallowed' "$out")" -eq 1 ] && echo "ok    robots_disallowed == 1" || { echo "BAD robots_disallowed $(jq '.summary.not_fetched.robots_disallowed' "$out") (want 1)"; fail=1; }
  [ "$(jq '.summary.pages_crawled' "$out")" -eq 2 ] && echo "ok    pages_crawled == 2" || { echo "BAD pages_crawled"; fail=1; }
fi
# There are broken links, so the exit code is 1
[ "$code" -eq 1 ] && echo "ok    exit 1" || { echo "BAD exit code $code (want 1)"; fail=1; }
# Reaching the external links is not asserted (no dependence on a third party being up); when either was unreachable,
# say that this run did not observe the real network
if jq -e '.findings[] | select(.rule == "links.unreachable" and ((.subject | startswith("https://example.org")) or (.subject | startswith("https://www.example.com"))))' "$out" > /dev/null; then
  echo "note  the external links were not reached (links.unreachable): this run did not observe the real network"
fi
rm -f "$out"
if [ "$fail" -ne 0 ]; then echo "fixture/check.sh: FAIL"; exit 1; fi
echo "fixture/check.sh: PASS"
