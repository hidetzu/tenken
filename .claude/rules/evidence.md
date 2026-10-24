# Evidence

⚠ **`MUST` = required, `SHOULD` = default, `MAY` = optional.**

⚠ **Grounds: this is the subject.** Every other rule in this directory can be re-argued for a
domain. ⚠ **This one cannot.** A project that softens it stops being able to say what it knows.

⚠ **This file is the owner of `CLAUDE.md` §1 and §6.** ⚠ **Never restate it there.**

## The lines that do not bend

```text
not observed           ≠  did not happen
could not be obtained  ≠  not there
the test passed        ≠  the behaviour is correct
it answered            ≠  it answered for the right reason

⚠ tenken's own three (added under the MAY below, never to be removed):
unreachable            ≠  broken
could not check        ≠  the site failed the check
not seen by the crawl  ≠  not on the site
```

- MUST: ⚠ **Never call something verified when it was not checked.**
- MUST: ⚠ **Never report "absent" for something that was merely not obtained.**
- MUST: ⚠ **Never dress a guess up as a measurement.**
- MAY: ⚠ **Add a line for this domain.** ⚠ **Never remove one.**
  (⚠ A stack adds `not captured ≠ not sent`; a data product adds `not in the data ≠ not real`.)

⚠ **Grounds for tenken's three: everything tenken emits is a verdict about somebody else's site,
read by a machine that cannot ask back.** ⚠ **A DNS failure, a timeout and a 404 are three
different facts, and the exit code is where the first two get collapsed into the third if nobody
stops it** (`docs/cli-reference.md` § 4 keeps them apart). ⚠ **A crawl bounded by depth
and page limits saw a subset** — ⚠ **"no broken links" is a claim about the pages crawled, with
that number as its denominator, and never about the site.**

⚠ **The question is never "did the test pass".** ⚠ **It is "does that test actually assert the
claim".** A stack that answers a ping while computing the checksum wrong still answers the ping.

## Silence is not permission

- MUST: ⚠ **`MUST` / `SHOULD` / `MAY` are different things, and "the spec does not say" is a
  fourth thing.** ⚠ **Do not collapse them.**
- MUST: ⚠ **Say which authority a behavioural claim comes from, and which section of it.**
  ⚠ **"It seemed right" is not an authority.**
- MUST: ⚠ **Where the authority is silent, say it is silent** — not that it permits, and not
  that it forbids.

## Outcomes are not one outcome

⚠ **This list is the owner.** ⚠ **`issue-ready` and `change-review` point here; they do not
copy it.**

| Outcome | ⚠ What it must not be confused with |
|---|---|
| Accepted and handled | — |
| ⚠ **Malformed** — it violates the format | ⚠ **Not the same as unsupported.** The other side is wrong |
| ⚠ **Well-formed but unsupported** — we understand it and decline | ⚠ **Not the same as malformed.** The other side is fine |
| ⚠ **We have not implemented it yet** | ⚠ **Never phrased as the other side's fault** (`CLAUDE.md` §4-1) |
| ⚠ **Nothing arrived** | ⚠ **Not the same as "it was not sent."** ⚠ It may have been lost in between |
| ⚠ **A timer expired while waiting** | ⚠ **Not an answer.** ⚠ It is the absence of one |

- MUST NOT: ⚠ **Never demand all six.** ⚠ **Only the ones that can occur here.**
- MUST: ⚠ **Say explicitly that the rest cannot occur.** ⚠ **Never drop them silently.**
- MUST: ⚠ **Record why something was rejected**, at least well enough to count it.
  ⚠ **An uncounted rejection is indistinguishable from something that never arrived.**

## Numbers

- MUST: ⚠ **Always give the denominator of the claim.** A number from another scope is a lie
  about this one.
- MUST NOT: ⚠ **Never write a number that was not measured.** No probabilities, no confidence figures.
- MUST: ⚠ **A measurement is reproducible or it is not a measurement.** Record **when, where and
  how**: the versions that matter, how the environment was built, how many runs, which percentile.
- MUST: ⚠ **Report a percentile as a value that was actually observed.** ⚠ Do not interpolate.
- MUST: ⚠ **The same rules apply to anything facing outward** (README, articles, commit messages,
  anything shared). ⚠ **When the same claim appears in two places, it uses the same denominator.**
  ⚠ Different scopes are fine — ⚠ **say which scope each number is.**

## Counts

- MUST NOT: ⚠ **Never write a count into a document.** ⚠ **It is stale from the moment it is
  written**, and it makes every parallel change conflict.
- MUST: ⚠ **Whatever produced the count announces it, at the moment it runs.**
  ⚠ **Copy the announced number straight into the report.**
- SHOULD: ⚠ **Have a check fail when a count is written back in.** ⚠ **Otherwise this rule is a
  promise, not a wall.**
