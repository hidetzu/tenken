# tenken — how we work

This file holds **how to work**.

⚠ **What tenken is, what v0.1.0 is, and what only the owner may change, is
[`docs/design.md`](docs/design.md) § 0, and that section is the single source.**
**What may be claimed today** (each claim with the tests that assert it) is
[`docs/test-perspectives.md`](docs/test-perspectives.md) § 6; **why a decision was made** goes in [`docs/design.md`](docs/design.md). ⚠ **How to write code** (the language, the layer split, testing
priorities, forbidden git operations) lives in [`.claude/rules/`](.claude/rules/).

⚠ **Never duplicate.** ⚠ **Written in two places, one of them goes stale.**
⚠ **In particular: never restate the product here.** ⚠ **Not the checks, not the v0.1.0 scope,
not the non-goals, not the thresholds.** ⚠ **Link to `docs/design.md` § 0 instead.**
⚠ **When the product changes, change that file. This one does not move.**

### ⚠ A section that points owns nothing

⚠ **Several sections below own no rule: they name the file that does.**
⚠ **A pointer section carries the pointer and nothing else** — ⚠ **no summary, no excerpt,
no "the gist is".** ⚠ **Naming the subjects the file covers is fine; ⚠ stating any of its rules
is not.**

⚠ **Grounds: the body moves and the summary does not.** ⚠ **So the summary is where the stale
copy lives** — ⚠ **and it sits directly under the words "not here", which is the one place a
reader will not think to doubt.**
⚠ **Stepped on where this template came from**: ⚠ **on the day that project moved its rules out,
7 of its 9 pointer sections had grown 16 lines of summary**, ⚠ **one of them a verbatim copy of
the evidence rules.**

⚠ **Never delete a heading here, and never renumber one.**
⚠ **Records already cite these rules by section number** — `docs/design.md`, issues, code comments, the
text a check prints — ⚠ **and the number is the address.**
⚠ **So a rule moves out and the heading stays, holding one line that says where it went.**

Before starting work, read [`.claude/rules/README.md`](.claude/rules/README.md).

⚠ **`⚠` marks "it hurts if you step on it".** ⚠ It is not decoration.

---

## 0. What this repository is

tenken is a CLI that checks a web site in one run and fails CI when the site has a problem.
⚠ **What it checks, what it deliberately does not, and what v0.1.0 is, is
[`docs/design.md`](docs/design.md) § 0.** ⚠ Not here.

⚠ **The five v0.1.0 checks are implemented (`docs/test-perspectives.md` § 6); how it is installed is `docs/design.md` § 0.2. The language is Go**
([`docs/design.md`](docs/design.md)).
⚠ **What comes next is one GitHub issue each, worked through `.claude/skills/issue-work/`**
⚠ **Every check is added one PR at a time against `docs/cli-reference.md`.**

⚠ **How it gets built is also a subject of this repository.** ⚠ **How far an AI can carry a CLI
from a product document to a public release — with the owner deciding what to build, what a user
sees, the rules, and every act of publishing (`.claude/rules/git.md` § Permission) — is what this repository tries out.**

⚠ **`.claude/` follows [`hidetzu/claude-dev-template`](https://github.com/hidetzu/claude-dev-template) and is kept diffable against it on purpose.**
⚠ **When doing the right thing here means fighting the template, the template is wrong, and the fix goes back there naming the project it broke in.**

---

## 1. The first principle

> **"Not checked" before "broken".**

⚠ **The order is never swapped.** ⚠ **Everything tenken emits is a verdict about somebody else's
site, read by a machine that cannot ask back.** ⚠ **A false "broken" blocks a deploy and teaches
people to ignore the tool; a false "fine" hides an outage; both come from reporting what was not
observed.** ⚠ **So when tenken cannot tell, it says it could not check — never "broken", never
"fine".** ⚠ **The exit codes are this principle made mechanical**
([`docs/cli-reference.md`](docs/cli-reference.md) § 4 owns the values).

⚠ **Whatever it says, it never outranks the evidence rules.**
⚠ **Those are [`.claude/rules/evidence.md`](.claude/rules/evidence.md), and they hold everywhere** —
in the code, in the tests, and in every report.

⚠ **Never restate them here.** ⚠ **They are the one thing this project does not re-word**,
because re-wording them is how they get softened.

---

## 2. Verification

⚠ **The contract is [`.claude/rules/verification.md`](.claude/rules/verification.md).**
⚠ **What to actually run is `.claude/skills/verify/SKILL.md`, which this project writes for itself.**
⚠ Neither belongs here (never two copies).

⚠ **Its external tier reaches the public network, and that is the owner's call every time.**

---

## 3. Architecture boundaries

⚠ **What can be said before the language is chosen, and nothing more:**

- ⚠ **One static binary, and nothing it needs at run time.** No browser, no daemon, no service,
  no runtime a user has to install (`docs/design.md` § 0.1, § 0.4).
- ⚠ **The network leaves the process in one place.** ⚠ **Every fetch and every TLS handshake goes
  through it, so a test can stand in for it** — ⚠ **and a test never reaches the public network**
  (`docs/test-perspectives.md` § 1).
- ⚠ **The crawl and the checks are different subjects.** The crawl decides what to fetch and
  within what limits; a check decides what to say about what came back. ⚠ **A finer split than
  that belongs in `docs/design.md` § 2 first.**
- ⚠ **One place writes the sentences a human reads, and one place assigns exit codes.**
  ⚠ **A check returns findings with a severity; it never prints and never exits.**

⚠ **Not to be introduced without a reason recorded in [`docs/design.md`](docs/design.md):**

```text
a dependency beyond the standard library and golang.org/x/net   (docs/design.md §1)
a configuration file, or a second way to say what a flag already says   (docs/cli-reference.md § 1)
an output format beyond human-readable and JSON                         (docs/cli-reference.md § 3.5)
anything that needs a browser in v0.1.0                                 (docs/design.md § 0.4)
a second build system, or a second test runner
a test that opens a socket to anything the test did not start itself
```

Two clauses hold regardless of the domain:

- ⚠ **Never keep two implementations that answer the same question.**
  If one is unavoidable, cross-check them mechanically.
  ⚠ **Writing the same decision in two places is how the two silently diverge.**
  (⚠ **The flag table in `docs/cli-reference.md` § 3 and the flags in `internal/cli` are one such pair, and
  `TestFlagsMatchCLISpec` is the mechanical check.**)
- ⚠ **A layer split belongs in `docs/design.md` § 2 before it belongs in code.**

---

## 4. Words

- **Never leak internal state into what a human reads.** Not an error code, but a sentence
  that says what happened and what to do about it.
- **Name things after the concept the domain already named, not after the data structure.**
  ⚠ **Borrow the existing name exactly.** ⚠ If a name here differs, that difference is a claim —
  justify it.
- **Never rename in bulk.** Changing a term does not license a sweep through `docs/design.md` and past
  discussions.

⚠ **Where tenken's names come from**, in this order:

```text
RFC 9110                 HTTP semantics — status codes, redirect, HEAD and GET, Retry-After
RFC 5280 / RFC 6125      certificates — notBefore, notAfter, subjectAltName, chain, name matching
RFC 6797                 HSTS — max-age, includeSubDomains
RFC 9309                 robots.txt — user-agent groups, allow / disallow
sitemaps.org protocol    sitemap, sitemap index, urlset
WHATWG HTML and URL      origin, base, srcset, the elements that carry a reference
docs/cli-reference.md § 6           ⚠ the product's own words
```

⚠ **What each word means is owned by [`docs/cli-reference.md`](docs/cli-reference.md) § 6.** ⚠ Not here.

⚠ **Language: this file, `.claude/` and `docs/` are English (comments in the code are Japanese); there is no Japanese README;
⚠ the user-facing surface (`README.md`, `docs/cli-reference.md`, `--help`, every message, every JSON key) is English.**
⚠ **The grounds are in [`.claude/rules/README.md`](.claude/rules/README.md) § Language.** ⚠ Not here.

### 4-1. Never open with what does not work

⚠ **This is not about hiding anything.** §1 outranks it, and **limitations are always stated**.
What changes is the **order, the subject, and the tense** — not whether it is said.

- **Say what this does first.** What it does not do comes after, with the reason
  and with what to do instead.
- **Do not use the progressive tense for a state.** "not receiving" reads as something
  happening right now on the reader's machine.
- **Never phrase our own gap as the other side's fault.** If we never implemented it,
  do not report it as "no response". ⚠ **The reader's next move depends on which it is** —
  retry, wait, or give up.
  ⚠ **Concretely here: `unreachable`, `broken` and `could not check` are three different
  sentences** (`docs/cli-reference.md` § 6). ⚠ **Never one of them for another** — ⚠ **the reader fixes the site for one,
  waits for another, and fixes their CI for the third.**
- **Do not sound stalled.** "not implemented yet" beats "unavailable": leave a reason to come back.

---

## 5. Comments

Comments carry **why this, why this value, what is being avoided**. That is an asset.
⚠ **But a stale comment misleads harder than stale code**, because it is believed.

Change code, and update the whole set:

```
implementation → test → comment → --help text → README.md → docs/test-perspectives.md § 6
```

⚠ **`docs/test-perspectives.md` says what "works" means.** ⚠ **When a behaviour changes, that
file changes first, then the test, then the code** — ⚠ **a test changed to match the code has
asserted nothing.**

⚠ **When a check reads documentation or comments, strip the comments first.**
⚠ Otherwise the check picks up the very words written to describe it.

---

## 6. How to write numbers

⚠ **Owned by [`.claude/rules/evidence.md`](.claude/rules/evidence.md).** ⚠ Not here.

---

## 7. How to proceed

1. **Measure before polishing.** Before fixing anything, state what it does now, in numbers.
2. Report **observation** (measured values, captured output) and **inference** (interpretation)
   separately.
3. Never report as confirmed what was not verified.
4. Do not widen a change past its `Non-goals`. The smallest change that meets the goal is the default.

⚠ **Deciding yourself vs. asking, and `ready-for-ai`, are owned by
[`.claude/rules/owner-decisions.md`](.claude/rules/owner-decisions.md).**
⚠ **What is the owner's alone in tenken — what to build (`docs/design.md` § 0), and how a change to it is proposed —
is owned by `docs/design.md` (its header) and [`.claude/rules/git.md`](.claude/rules/git.md) § Permission.**
⚠ Neither is here.

---

## 8. git

⚠ **Owned by [`.claude/rules/git.md`](.claude/rules/git.md)** — Conventional Commits, permission
for `git push` and merge, how to split commits, and what never goes into anything public.
⚠ Not here.

---

## 9. Pitfalls we have stepped on

⚠ **This table starts empty, and that is correct.**

⚠ **Nothing goes in here that did not happen in this repository.** Not an analogy from another
project, not something plausible, not something an AI expects to be true.
⚠ **A pitfall is a measurement**: it names what happened, and what to do instead.

⚠ **This is not where engineering constraints go.** A rule that holds because of the language,
because of a protocol, or because the input is hostile ⚠ **belongs in `.claude/rules/`, and binds
already.**
⚠ **Never manufacture an incident to move a constraint in here**, and
⚠ **never soften a constraint on the grounds that this table has no row for it yet.**

⚠ **When you fill a row in, also leave the test behind.** A row with no test is a note;
a row with a test is a wall.
⚠ **If the incident also produces a new rule, the rule goes to `.claude/rules/` citing this row.**
⚠ **Both records stay. Neither replaces the other.**

| What happened | What to do instead |
|---|---|
