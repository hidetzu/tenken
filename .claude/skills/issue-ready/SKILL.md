---
name: issue-ready
description: Judge whether an issue can be handed to an AI, and shape it into something that can. Use when drafting a new issue, auditing existing ones, or before applying ready-for-ai. Applies the label only on YES, and never on the conditions owner-decisions.md forbids.
---

# Issue Quality Gate

⚠ **This is not the skill that implements an issue.** That is
[`issue-work`](../issue-work/SKILL.md). ⚠ **This runs before it.**

```
draft issue / existing issue
        |
   issue-ready       <- here. decides whether it can be handed over
        |
   ⚠ the verdict is posted to the issue, then the label is applied
        |
   issue-work / loop-controller  (⚠ which re-runs this gate first)
```

⚠ **When the label may be applied, and when it may not, is owned by
[`owner-decisions.md`](../../rules/owner-decisions.md) § `ready-for-ai`.**
⚠ **Read it. ⚠ It is not copied here.**

## ⚠ What this skill never does

- ⚠ **Never applies the label on anything but `Ready for AI: YES`**, and
  ⚠ **never on the conditions [`owner-decisions.md`](../../rules/owner-decisions.md) forbids**
  (an unresolved owner decision, an open dependency, an environment that cannot verify).
  ⚠ **That file owns the list.**
- ⚠ **Never removes the label.** ⚠ **Applying and removing are not symmetric** — ⚠ **removing one
  the owner applied overrides the owner.** ⚠ **Say it should go, and stop.**
- ⚠ **Never closes, rewrites, or splits an issue.** It proposes.
- ⚠ **Never fills in a missing spec.** When the issue, `docs/cli-reference.md`, `docs/test-perspectives.md`, `docs/design.md` and the code
  disagree, ⚠ **do not decide which is right** — return `NEEDS-HUMAN-DECISION`.
- ⚠ **Never decides what the thing should do.**

---

## 1. Read first

```
CLAUDE.md          how we work
.claude/rules/     how we write it
docs/test-perspectives.md § 6  what may be claimed today
docs/design.md                 why it was decided that way
```

⚠ **Read the `docs/design.md` sections covering what the issue touches.** ⚠ A decision recorded there is often newer than the issue body.

## 2. Look at the real thing

⚠ **Never treat an old issue body as the current spec.**
A number in an issue is **the value on the day it was written**, not today's.

```bash
gh issue view <N> --json number,title,body,labels,state,createdAt
gh issue view <N> --comments        # ⚠ comments override the body
```

⚠ **Re-measure on the default branch.** If the issue says "N of them are dropped" or "it never
answers", confirm that is **still** true. A discrepancy is itself something to report.

---

## 3. Shape

An issue handed to an AI carries:

```
Goal                  one thing that, once true, means it is done
Background            why. ⚠ With denominator, date and conditions if measured
Scope                 what may be touched
Out of Scope          ⚠ what may not. Without this it spreads without limit
Owner Decisions       ⚠ what is already settled. The AI never overrides these
Acceptance Criteria   ⚠ in a form a machine can judge
Verification          which checks. ⚠ Including any that must be added
Human Decision        ⚠ what is not settled (if any, ready-for-ai is not allowed)
Stop Conditions       what makes it stop and ask
```

⚠ **Acceptance criteria must be machine-judgeable.**

| ⚠ Not this | This |
|---|---|
| X works | ⚠ **Name the input, and the observable that must follow from it** |
| handles bad input properly | ⚠ **Name which bad input, and what is observable afterwards** — including the counter that must move, and by how much |
| it is fast | Under `<stated conditions>`, the measured p90 is at or below `<value the owner set>` |
| responds correctly | ⚠ **"Correctly" cannot be judged.** Name the authority, its section, and the observable |

⚠ **A number carries the denominator of its claim, the date, and the conditions**
([`evidence.md`](../../rules/evidence.md)).
⚠ **A behavioural claim carries the authority and the section it comes from.**

### ⚠ Distinguishing outcomes (⚠ **when it applies**)

⚠ **An issue about "we do / do not respond to something" must say which outcomes it covers.**
Without that, ⚠ **the AI will pick an implementation that reports "not there" for something it
merely failed to obtain.**

⚠ **The list of outcomes is owned by [`evidence.md`](../../rules/evidence.md).**
⚠ **Never copy it here.** ⚠ **Read it, and say which of them can occur for this issue.**

⚠ **Some issues do not apply at all** (build flags, file layout).
⚠ **Say "not applicable" in the verdict** — ⚠ never leave it blank.

⚠ **Never invent vocabulary in an issue.** ⚠ If a new distinction is needed, that is a **spec
change**, and a human decides (§4, clauses 1 and 8).

⚠ **Write the acceptance criteria per outcome** — not "handles it", but
⚠ **which outcome produces which observable effect** (⚠ **both halves**: what is emitted, and
what is counted).

---

## 4. Judge the granularity

⚠ **Any one of these means `ready-for-ai` is not allowed.**

| # | What | ⚠ Why |
|---|---|---|
| 1 | The spec itself has to be decided | ⚠ The AI invents a spec |
| 2 | The behaviour has to be chosen between options the authority permits equally | Same |
| 3 | It has several independent goals | It cannot be one PR for one reason |
| 4 | Acceptance criteria are not machine-judgeable | Nobody can say it is done |
| 5 | Scope is too broad | A judgement call arrives mid-way |
| 6 | Out of Scope is missing | It spreads without limit |
| 7 | An owner decision is unresolved | ⚠ The AI decides in their place |
| 8 | It contradicts the code, `docs/cli-reference.md`, `docs/test-perspectives.md`, or `docs/design.md` | ⚠ The AI decides which is right |
| 9 | ⚠ **It changes what a recorded value means** | ⚠ Goes straight to the rules. A human decides |
| 10 | ⚠ **The AI given this issue cannot actually run the verification it needs** | ⚠ Then nobody can show it green |
| 11 | ⚠ **Outcomes are not distinguished** (and this issue is one where they apply) | ⚠ **The AI reports "not there" for "not obtained."** §3 |
| 12 | ⚠ **A dependency this issue names is still open** | ⚠ **The work can be started and not finished.** ⚠ **See below** |

### ⚠ Clause 12 — dependencies are read, not guessed

⚠ **An issue's dependencies are the ones it names.** ⚠ **Never infer one from a hunch about
implementation order.**

- MUST: ⚠ **Check the state of each named issue.** ⚠ **Open means clause 12 is hit.**
- MUST: ⚠ **Say which dependency is open, by `owner/repo#N`.** ⚠ **"Blocked" with no number is
  not a reason.**
- MUST NOT: ⚠ **Never treat a merged PR as a closed dependency.** ⚠ **The issue closing is the
  event that matters**, ⚠ **and `Closes` does not always fire.**
- ⚠ **A dependency that only becomes visible once its neighbour is built is normal.**
  ⚠ **Add it when it appears, and say that it was not declared before.**

### ⚠ Clause 10 asks about the AI, not about CI

⚠ **"CI cannot run it" is not clause 10.**
⚠ **Where CI runs fewer tiers than exist, a criterion written against CI would block every issue
whose proof needs the tiers CI skips** — ⚠ **and those tend to be the ones that matter.**

⚠ **Measure it. Never assume it.** Run the tier the issue needs, or confirm its environment can be
built, ⚠ **before answering** — ⚠ **the answer is a property of the machine, not of the issue**,
and it can differ between a developer's machine and a hosted runner.

⚠ **What does not change:** when the tier CI cannot run is the one that proves a change,
⚠ **the report says so** ([`verification.md`](../../rules/verification.md)).
⚠ **A green tick is then not evidence for that change.**

⚠ **When it is too big, propose a split.** ⚠ **Split by reason, never by file.**

---

## 4-2. ⚠ Is the body still true?

⚠ **The label is an entry condition, not a guarantee**
([`owner-decisions.md`](../../rules/owner-decisions.md)). ⚠ **Bodies go stale after a label is applied.**

⚠ **What we stepped on**: ⚠ **a body still described a problem that had been fixed a month earlier.**
⚠ **A partial fix could not carry `Closes`, so the issue stayed open and the wording was left behind.**
⚠ **The next reader nearly believed it.**

⚠ **Look at that one issue only.** ⚠ **Not the other open ones**
(⚠ the reasoning is in [`owner-decisions.md`](../../rules/owner-decisions.md)).

| Where | ⚠ What fails |
|---|---|
| ⚠ **What the body calls the present** | ⚠ **It is already fixed** (⚠ confirm in the code, the checks, the PR) |
| ⚠ **Checkboxes** | ⚠ **A finished item still shows `[ ]`** |
| ⚠ **References** | ⚠ The issue it points at is closed, ⚠ or a file or a section it names is gone |
| ⚠ **Measured values** | ⚠ Measured long ago, ⚠ and the number has moved |

⚠ **When it has drifted, write it under `## Updates`** (⚠ the format lives in
[`owner-decisions.md`](../../rules/owner-decisions.md)).
⚠ **Do not delete the body.** ⚠ **Fix it before you return the verdict** —
⚠ **never return `YES` on a stale body.**

---

## 5. Return the verdict

```
Issue #N  <title>

Classification: KEEP / REWRITE / SPLIT / CLOSE / NEEDS-HUMAN-DECISION

Ready for AI: YES / NO

Reason:
  <which clause it hit. If none, say all 12 were checked>

⚠ Dependencies:
  <each named dependency, by owner/repo#N, with its state. "none named" if there are none>

⚠ Label:
  <APPLIED / NOT-APPLIED, and which condition in owner-decisions.md stopped it>

⚠ Outcome distinction:
  <applicable or not. If applicable, which outcomes are written and which are missing>

Where it disagrees with the default branch today:
  <⚠ the re-measured result. If none: "re-measured, no discrepancy">

Split proposal:
  <only for SPLIT. by reason>

Verification:
  <which checks. Including any that must be added>

Human Decision:
  <⚠ list what a human must decide, without deciding it>
```

## 5-1. ⚠ Applying the label

⚠ **`YES` means "an AI can implement this", not "this should be implemented."**
⚠ **Those are different, and the second one is still not this skill's to answer** — ⚠ **but the
first one is now acted on** ([`owner-decisions.md`](../../rules/owner-decisions.md)).

- MUST: ⚠ **Post the verdict first, then label**, ⚠ **in one step that also records that the AI
  did it.** ⚠ **A label with no verdict behind it is indistinguishable from one applied by
  mistake**, ⚠ **and an unrecorded one cannot be told from the owner's.**
- MUST: ⚠ **Never apply it as a bare label edit.** ⚠ **That skips the record.**
- MUST: ⚠ **The step refuses on the conditions `owner-decisions.md` forbids.**
  ⚠ **A refusal is an answer, not an obstacle** — ⚠ **never work around it.**
- ⚠ **What that step is, is the project's** (⚠ **same split this template already makes for
  `verify`: the contract travels, the commands do not**).

---

## 6. ⚠ Drafting a new issue

⚠ **Do not fold this shape into the templates outsiders use.**
A bug report from someone outside should stay cheap to file. ⚠ **Demanding nine sections raises
the cost of reporting.**

⚠ **The §3 shape is for issues the owner writes, and for drafts this skill has shaped.**

⚠ **One issue, one reason.** Never add "and while we're here".
