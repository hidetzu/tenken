# DISCOVERY — what is not decided yet

Once something is decided, move it to the place the top of [`design.md`](design.md) names, and remove it from here.
The history of how it got decided is not kept here.

---

## 0. Proposals to change what to build (design.md §0; waiting for the owner)

Only the owner changes design.md §0, so anything to change there is placed here as a proposal. Once adopted, fix design.md §0 and remove it from here.

(None at the moment)

## 1. Not decided yet

- Whether to provide a GitHub Action that uses tenken itself (`uses: hidetzu/tenken@v1`) — a candidate to pass on for v0.1.0
- A feature that reports only the difference from the previous result is not in v0.1.0 (a candidate for v0.2 or later): [`cli-reference.md`](cli-reference.md) §7

## 2. Known limitations (not fixed)

⚠ **What is not built (outside the v0.1.0 scope) is owned by [`design.md`](design.md) §0.4. It is not written here.**
What goes here is **what is inside the scope of design.md §0.3 but was put off**, and the limitations we know of.
⚠ **An absence with a reason written down is a decision; an absence with no reason written down is just work not started.** They are different things, so write that they are different.

- References to internationalised domain names (Unicode host names) (deliberate): they can be fetched (Go's net/http converts them to punycode and connects), but they appear in `subject` and `found_on` with a percent-encoded host name. A Unicode reference and a punycode reference are counted as different URLs (different origins). Adding the conversion (`golang.org/x/net/idna`) to make them one is put off
- The certificates of hosts part way along the start URL's chain (https hosts that are not the host after redirects) (deliberate): the chain is fetched with verification turned off ([`cli-reference.md`](cli-reference.md) §2.1), and `cert` checks only the last https host. Problems with the certificates of hosts part way along are not reported (cli-reference.md says only "after redirects")
- Progress on standard error, and colour on standard output ([`cli-reference.md`](cli-reference.md) §3.5) (deliberate): added once the crawl exists, together with deciding whether the output is a terminal (`--quiet` is accepted and does nothing)
- Honouring the proxy environment variables ([`cli-reference.md`](cli-reference.md) §3.6) (implemented but not verified): `fetch` follows them, but `cert` through a proxy has not been confirmed end to end
- The sitemap's URLs are checked one at a time, in order, and `--concurrency` has no effect (for 50000 entries, 50000 HEADs in order)
- There are places with no interval (`--delay` / `Crawl-delay`): between the start page / the `http://` probe / robots.txt and the first request of the crawl (the first request of the sitemap when there is no crawl) (all of them are done in order, so they do not overlap). The start page, the probe (the request that checks `redirects.no-https`) and fetching robots.txt do not go through the gate. The crawl and the sitemap share one gate (`internal/cli/cli.go`)
- When the same host fills the queue of candidates, the parallelism does not reach `--concurrency` (one at a time to the same host)
- Whether the same sitemap URL goes into `external` or `private` of `not_fetched` depends on the order it was seen in (the total is 1)
- Even when a link's redirect target is a page already crawled, it is fetched with one more GET while following the chain (`Follow` does not look at the URLs the crawl knows). It does not affect correctness; it only adds one request. To be fixed in a separate PR that changes the structure of the crawl. test-perspectives.md §2.2 "The same URL is never fetched twice" speaks of fetching as a reference
