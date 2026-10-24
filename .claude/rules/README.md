# Rules

⚠ **This directory holds "how to write it".**
⚠ **"How to work" is [`CLAUDE.md`](../../CLAUDE.md), "what may be claimed" is
[`docs/test-perspectives.md`](../../docs/test-perspectives.md) § 6, and "why" is [`docs/design.md`](../../docs/design.md).**
⚠ **"What to build, and what not" is [`docs/design.md`](../../docs/design.md) § 0, and only the
owner changes it.**

⚠ **Never duplicate.** ⚠ **Written in two places, one of them goes stale.**
⚠ **If a subject already has an owner, this directory only says "look there".**

## ⚠ Language

⚠ **`CLAUDE.md`, everything under `.claude/` and everything under `docs/` is English**
(there is no Japanese README; the developer guide is `docs/development.md`).
⚠ **What a user reads — `README.md`, `docs/cli-reference.md`, `--help`, every message, every JSON key — is English.**
⚠ **The owner's own words are quoted in the original Japanese, with an English translation beside them**,
⚠ **because an owner decision is copied verbatim** ([`owner-decisions.md`](owner-decisions.md)).
⚠ **Comments in the code are Japanese.**
⚠ **This is a decision, not an accident.**

⚠ **Grounds: `.claude/` is a port of [`hidetzu/claude-dev-template`](https://github.com/hidetzu/claude-dev-template),
and it is kept diffable against upstream so improvements can be sent back.**
⚠ **Translating it would end that, and [`evidence.md`](evidence.md) says in its own text that
re-wording it is how it gets softened.** ⚠ **`docs/` is English too, so that the documents are in one language and not mixed.**
⚠ **The user-facing surface is English because the tool is
published as OSS and runs in other people's CI** (`docs/design.md` § 0.2).
⚠ **What each word means is `docs/cli-reference.md` § 6.** ⚠ Not here.
⚠ **There is one reference, in English**: a second, translated one would be the same contract written in two places
(`CLAUDE.md` § 3).

## Two kinds of rule, and they are not interchangeable

```text
Engineering constraint          Learned pitfall
grounded in the language,       something this repository
the protocol, or the fact       actually paid for
that the input is hostile              |
        |                              |
binds from the first line       lives in CLAUDE.md §9,
of code                         with the test that stops
        |                       it happening again
lives here, with its grounds
```

⚠ **Everything in this directory is an engineering constraint.** ⚠ **It binds now.**
⚠ **It does not wait for an accident here to earn its place.**
⚠ **Each section states its grounds** — cite those, never an anecdote.

⚠ **Never demote a constraint to "we will see" because nothing has gone wrong yet.**
⚠ **Never promote one into `CLAUDE.md` §9 by inventing an incident.**

⚠ **A learned pitfall can add a rule here.** ⚠ **When it does, the grounds it cites is the
incident, and `CLAUDE.md` §9 keeps the row.**
⚠ **The two records point at each other; neither replaces the other.**

## What is here

| File | What it holds | Where it came from |
|---|---|---|
| [`evidence.md`](evidence.md) | ⚠ **What may be said to have been observed.** Denominators, counts, outcomes | template, ⚠ **plus tenken's three domain lines** (added under its MAY, never to be removed) |
| [`verification.md`](verification.md) | The three tiers, partial runs, `PASS` / `FAIL` / `NOT-VERIFIED`, ⚠ **and that an exercise must not change the world** | template, verbatim |
| [`owner-decisions.md`](owner-decisions.md) | Decide yourself vs. ask, `ready-for-ai` | ⚠ **diverged** (tenken's note on who answers the "Ask" rows; no Slack lines, no `visual-decision` pointer) |
| [`git.md`](git.md) | Commits, permission, forbidden operations, what never goes public | ⚠ **diverged** (the AI merges through merge-gate; § Permission there) |
| [`go.md`](go.md) | ⚠ **Dependencies, errors, time / network / output, concurrency, tests, naming, format** | ⚠ **tenken's own** (design.md §1, §2) |

> ⚠ **What the parsers must tolerate from a hostile site has no rule file of its own.** HTTP and TLS go
> through Go's `net/http` and `crypto/tls`, HTML through `golang.org/x/net/html`, sitemaps through
> `encoding/xml` and `compress/gzip`, and robots.txt through tenken's own reader.
> ⚠ **The general rules are `go.md` § Errors and § Time, network, output** (grounds: `verification.md`
> § Priority, clause 3). ⚠ **The limit each parser keeps, and what it reports when it is hit, is
> `docs/test-perspectives.md`, held by the tests it names.**

⚠ **`MUST` = required, `SHOULD` = default, `MAY` = optional.**
⚠ **`⚠` marks "it hurts if you step on it"** (same convention as `CLAUDE.md`).

## ⚠ Subjects owned elsewhere

⚠ **Do not copy these here.**

| Subject | Owner |
|---|---|
| not observed ≠ did not happen / never dress a guess as a measurement / denominators | [`evidence.md`](evidence.md) |
| Wording a human reads (never open with what does not work) | `CLAUDE.md` §4 |
| Which checks to run, in what order | [`verify`](../skills/verify/SKILL.md) |
| The package boundaries (network, checks, output, clock) | `docs/design.md §2` (⚠ **the wall is `internal/walls/boundary_test.go`**) |
| How to review a change (scope, rules, stale results) | `.claude/skills/change-review/SKILL.md` |
| Whether an issue can be handed over | `.claude/skills/issue-ready/SKILL.md` |
| Which PRs the AI may merge | `.claude/tools/merge-gate.mjs` (⚠ **its code is the rule's only implementation**; [`git.md`](git.md) § Permission) |
| What tenken checks, what it does not, the v0.1.0 scope | [`docs/design.md`](../../docs/design.md) § 0 (⚠ **owner only**) |
| What "works" means, per check, before and after the tests exist | [`docs/test-perspectives.md`](../../docs/test-perspectives.md) |
| What is not decided yet, and proposals to change `design.md` § 0 | [`docs/discovery.md`](../../docs/discovery.md) |
