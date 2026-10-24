---
name: change-review
description: Review a change (git diff) against the issue's scope and the project rules, and return PASS / NEEDS-FIX / HUMAN-DECISION. Use before opening a PR, and when the Loop Controller reaches its review stage.
---

# Change / Scope Review

⚠ **Read `git diff` yourself.** ⚠ Never write from a summary or from memory.

⚠ **Running the checks is not this skill.** That is `.claude/skills/verify/SKILL.md`.
What this looks at is **whether the change itself sits inside what was asked and inside the rules**.

⚠ **Never invent a standard here.** [`CLAUDE.md`](../../../CLAUDE.md),
[`.claude/rules/`](../../rules/), [`docs/test-perspectives.md`](../../../docs/test-perspectives.md),
[`docs/design.md`](../../../docs/design.md) and the issue's Owner Decisions are authoritative.

---

## 0. ⚠ Read it in a fresh context

⚠ **The context that wrote the change does not review it.**
⚠ **Run this skill in a context that has not seen the implementation** — a subagent, given what
an outside reader would have, ⚠ **and not the plan, the reasoning, or the conversation that
produced the change.**

⚠ **Grounds: a check that agrees only with itself has proved nothing**
([`verification.md`](../../rules/verification.md)). ⚠ **The context that wrote it reads what it
meant, not what it wrote.**

- MUST: ⚠ **Hand over the issue (body and comments) and the branch** — ⚠ **and, on a re-review,
  the previous return verbatim and the answer to each of its findings** (§ 1, § 6).
  ⚠ **Nothing else.** ⚠ **Never a summary of the change** — ⚠ the reviewer reads `git diff`
  itself (above).
- MUST: ⚠ **When a fresh context cannot be had, say so in the return** (`Reviewed in`, § 6).
  ⚠ **A silent gap reads as "checked and fine".**

---

## 1. What to read

```bash
git diff <default>...HEAD          # the change itself
git diff --stat <default>...HEAD   # where it moved
git log --oneline <default>..HEAD  # is it split by reason
```

⚠ **Never judge from `--stat` alone.** Read the contents.

### ⚠ A re-review reads what moved since the last one

⚠ **The first review reads the whole change.** ⚠ **A re-review after `NEEDS-FIX` reads the delta.**

```bash
git diff <reviewed-at>..HEAD       # <reviewed-at> is the previous return's `Reviewed at`
```

⚠ **Grounds: re-reading the whole change finds what the last pass happened to miss, ⚠ not what
the fix broke.** ⚠ **Each pass then turns up a little more of what was always there, and the loop
does not converge.**

- MUST: ⚠ **A finding the previous return carried is carried forward, not raised as new.**
  ⚠ **It stays in the return, marked `(carried from <SHA>)`, until it is closed.**
  ⚠ **`Fixed` closes it only when the delta shows the fix**; ⚠ **an answer claiming a fix is not
  the fix.** ⚠ **`Declined` and `Pre-existing` close it on the answer** (§ 6).
  ⚠ **A carried `Blocking` finding that is not closed still blocks.**
- MUST: ⚠ **A carried finding still open after a fix round is the same finding repeating**
  (`loop-controller` § 5 stops on it).
- MUST: ⚠ **When the delta reaches a file the previous review did not read, read that file whole.**
- MUST: ⚠ **When `<reviewed-at>` is not an ancestor of `HEAD`** (a rebase), ⚠ **or the default
  branch was merged in since**, ⚠ **the delta is not this change's alone: read the whole change
  again.**

---

## 2. Scope

| What to check | ⚠ How it fails |
|---|---|
| **Acceptance Criteria** | ⚠ **One at a time**, each with what confirmed it. ⚠ "Probably satisfied" is not satisfied |
| **Out of Scope / Non-goals** | Files outside it have moved |
| **One PR, one reason** | ⚠ Other work got swept in (the `git add -A` accident) |
| **Unrequested features** | Something not in the issue has appeared |
| **Incidental refactors** | Rewrites unrelated to the goal |

---

## 3. The rules

⚠ **These outrank scope.** ⚠ Inside scope but touching one of these is still `NEEDS-FIX`.

| What to check | ⚠ How it fails |
|---|---|
| **not observed ≠ did not happen** | Something that was merely not seen is reported as absent |
| **could not be obtained ≠ not there** | A failure to read gets reported as "there is nothing" |
| **the test passed ≠ it is correct** | A check that would pass with the behaviour removed |
| **it answered ≠ for the right reason** | An answer asserted, but not the thing that made it right |
| **never dress a guess as a measurement** | Probabilities, confidence figures, numbers nobody measured |
| **denominators** | A number with no scope, date, or conditions. ⚠ Numbers from different scopes side by side |
| **counts written down** | ⚠ A count put into a document instead of announced by whatever produced it |
| **claims against an authority** | A behaviour changed with no section named. ⚠ `MUST`, `SHOULD` and silence conflated |
| **two implementations of one question** | ⚠ The same decision made in two places. ⚠ **If unavoidable, is it cross-checked mechanically?** |
| **the meaning of a recorded value** | A counter, a state name, or a unit changed meaning without saying so |
| **untrusted input** | A length or an offset that came from outside, used without checking it against what was actually read |
| **silent rejections** | Something discarded with nothing counted (⚠ indistinguishable from something that never arrived) |
| **internal state shown to a human** | A raw enum, an errno, or a status code reaching output |

### ⚠ tenken's own rows

⚠ **These come from tenken's three lines in [`../../rules/evidence.md`](../../rules/evidence.md)
and from [`docs/test-perspectives.md`](../../../docs/test-perspectives.md) § 1.**
⚠ **A row says what to look at and how it fails. ⚠ It never restates the rule's text.**
⚠ **The last rows come from [`../../rules/go.md`](../../rules/go.md).**

| What to check | ⚠ How it fails |
|---|---|
| **a verdict where "could not check" belongs** | ⚠ **`fail`, or exit code 1, for a DNS failure, a timeout, a TLS handshake that never completed, or a check that never ran.** ⚠ Look at every place that turns an error into a severity |
| **a crawl claim without its denominator** | "no broken links" in output or a doc with no count of pages crawled and no word on which limit stopped it |
| **a test that reaches the network** | ⚠ **A test resolving a name or opening a socket to anything it did not start itself.** ⚠ A hostname literal in a test is the first place to look |
| **a certificate committed as a file** | ⚠ **Anything under test fixtures with a `notAfter`.** ⚠ It expires, and the test fails on a date nobody chose |
| **a default written twice** | A threshold, a limit or an exit code in code and again in `--help`, README or a doc, with nothing cross-checking them (`CLAUDE.md` § 3) |
| **a panic, or `io.ReadAll`, on bytes from a site** (`go.md`) | ⚠ **Any parse of a response that can crash or read without `--max-body`.** ⚠ Look at every decoder and every body read |
| **map order reaching output** (`go.md`) | A `range` over a map that feeds findings, `found_on` or a summary without sorting |
| **an error compared by string, or flattened** (`go.md`) | `err.Error() ==`, `strings.Contains(err.Error(), …)`, or `%v` where `%w` keeps `unreachable`'s reason recoverable |
| **a word used for another row's meaning** | `unreachable` / `broken` / `could not check` / `external` / `origin` meaning anything but what `docs/cli-reference.md` § 6 fixes |

---

## 4. ⚠ Stale results and ordering

⚠ **Does an old result overwrite the current state?**

⚠ **Anything answered asynchronously has this defect available to it.** ⚠ **The premise can move
while the answer is in flight**, and the answer arrives believing the old one.

| What to check | ⚠ How it fails |
|---|---|
| ⚠ **Was the premise the same when it was sent and when it came back?** | An answer is applied to state that has since moved on |
| ⚠ **A late reply overtaking a newer one** | ⚠ **A slow response arrives after, and overwrites, a newer one.** Arrival order is not send order |
| ⚠ **A sample attributed to the wrong attempt** | ⚠ **A measurement taken from a retried exchange cannot say which attempt it belongs to.** Attributing it anyway is a guess wearing the face of a measurement |
| **Timers still armed** | The condition changed but the timer is still scheduled to fire |
| **Ordering of failure and success** | "Nothing arrived" is emitted, then an old success lands. Or the reverse |
| ⚠ **Where state is written** | ⚠ The same state written from two places (⚠ **nothing decides which is last**) |
| **Cancellation** | Something cancelled still writes its result afterwards |
| **Identity** | A response matched to a request by position or timing rather than by an identifier |

⚠ **The way to check is not to read — it is to reorder.**
⚠ **Delay it, invert the order, and see whether it actually happens.**
⚠ **Never pass it because "it is unlikely."**

⚠ **This is a correctness question, not a presentation one.** ⚠ **State overwritten by a stale
result reads, from outside, as something that was observed.**

⚠ **Do not prescribe the fix.** Generation counters, identifier matching, or cancellation are
implementation calls. ⚠ **Only judge whether it is handled.** If it is not, `NEEDS-FIX`.

---

## 5. Tidy-up

| What to check | ⚠ How it fails |
|---|---|
| **Dead code** | Things no longer used are still there |
| ⚠ **Stale comments** | ⚠ **They mislead harder than code.** Old numbers, counts, filenames |
| ⚠ **Copied comments** | Moved to another file, ⚠ **and the numbers no longer match** |
| **Changes with no effect** | Written, but nothing changes (⚠ confirm by measuring) |
| **Fixed as a set** | implementation -> check -> comment -> README -> `docs/test-perspectives.md` § 6 |
| **Bare issue numbers** | ⚠ Write them with the repository name (a bare number points elsewhere after a migration) |
| ⚠ **A stale issue body** | ⚠ **A partial fix left the issue body untrue** (⚠ when `Closes` cannot be carried). ⚠ **Look only at the issue this PR touches.** ⚠ **Write it under `## Updates`** ([`owner-decisions.md`](../../rules/owner-decisions.md)) |

⚠ **Never write "confirmed" for something that was not verified.**

---

## 6. What to return

```
Verdict: PASS / NEEDS-FIX / HUMAN-DECISION

Reviewed at:
  <head SHA>                         ⚠ the commit this verdict is about
Reviewed in:
  fresh context / ⚠ same context (<why a fresh one could not be had>)
Read:
  whole change / ⚠ delta since <SHA>

Scope:
  N files changed / M reasons        ⚠ only what was actually read

Acceptance Criteria:
  1. <AC>  -> <what confirmed it>  OK / NO / ⚠ unverified

Blocking:
  - <the class> — <every instance, file:line>
    <breaks a rule / outside scope / an AC unmet / ⚠ a stale result can overwrite current state>

Non-blocking:
  - <the class> — <every instance>   worth fixing, not worth blocking on

Pre-existing:
  - <the class> — <file:line> — <how it was shown to predate this change>   ⚠ never blocks

Human Decision:
  - <spec, behaviour, meaning of a recorded value / ⚠ a Blocking finding answered Declined>
```

| Verdict | When |
|---|---|
| **PASS** | Every AC met, nothing outside scope, no rule touched |
| **NEEDS-FIX** | ⚠ **Any AC unmet** / outside scope / a rule touched / ⚠ **a stale result can overwrite** / tidy-up outstanding |
| **HUMAN-DECISION** | ⚠ **The spec, the behaviour, or the meaning of a value** must be settled first, ⚠ **or a `Blocking` finding was answered `Declined`**. ⚠ **Never settle it yourself** |

⚠ **One blocking item means it is not a PASS.**
⚠ **One "unverified" AC means it is not a PASS** (either `NEEDS-FIX`, or back to verifying to measure).

### ⚠ Every finding says whether this change introduced it

⚠ **Grounds: a defect the change did not introduce cannot be fixed inside its scope.**
⚠ **Blaming the change for it either pushes the fix past `Non-goals` or spends a round on it,
⚠ and the next pass finds the next old one.**

- MUST: ⚠ **`Pre-existing` only when shown** — ⚠ **the same code is on the default branch**
  (`git show <default>:<path>`, `git blame`). ⚠ **Say how it was shown.**
- MUST: ⚠ **Not shown is `Blocking` or `Non-blocking`, never `Pre-existing`.**
  ⚠ **Failing to show it predates the change is not showing that it does**
  ([`evidence.md`](../../rules/evidence.md)).
- MUST: ⚠ **A pre-existing defect that this change makes reachable, or worse, is this change's.**
- MUST NOT: ⚠ **`Pre-existing` never makes a `NEEDS-FIX`.** ⚠ **It is listed so it is not lost**;
  ⚠ **whether it becomes its own issue is not this skill's to decide.**

### ⚠ Name the class, not the instance

- MUST: ⚠ **A finding names the class of defect, and every instance of it in what was read.**
  ⚠ **Grounds: a finding raised one instance at a time gets fixed one instance at a time, ⚠ and
  the next round finds the next instance.**
- MUST: ⚠ **When the instances could not all be looked for, say where the search stopped.**

### ⚠ Every finding gets an answer

⚠ **Grounds: a finding dropped without a word is indistinguishable from one never raised** —
⚠ **the same hole as a silent rejection** ([`evidence.md`](../../rules/evidence.md)).

⚠ **The reviewer raises it. ⚠ Whoever made the change answers it**, with exactly one of:

| Answer | ⚠ What it carries |
|---|---|
| **Fixed** | ⚠ **The commit that fixed it** |
| **Declined** | ⚠ **A reason someone could disagree with.** ⚠ "Not needed" is not a reason |
| **Pre-existing** | ⚠ **Only for a finding the review already marked `Pre-existing`**, with how it was shown. ⚠ **Left as found**, and listed where the owner will read it |

- MUST: ⚠ **`Non-blocking` findings are answered too.** ⚠ **Not blocking is not the same as not
  answered.**
- MUST: ⚠ **A `Blocking` finding answered `Declined` makes the verdict `HUMAN-DECISION`.**
  ⚠ **Whoever made the change does not overrule the review on what blocks**; ⚠ **the owner does.**
- MUST NOT: ⚠ **Whoever made the change never re-labels a finding `Pre-existing`.**
  ⚠ **That label needs the showing above, and the reviewer gives it.**
- MUST: ⚠ **The answers go where the owner reads them** — ⚠ **the PR body, when there is one.**
