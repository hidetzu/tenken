# Owner decisions

⚠ **`MUST` = required, `SHOULD` = default, `MAY` = optional.**

⚠ **This file is the owner of `CLAUDE.md` §7-1 and §7-2.** ⚠ **Never restate them there.**

## Grounds

⚠ **Stalling costs more than deciding.** ⚠ **But a decision the owner cannot walk back costs
more than either.** ⚠ **The line between them is the only thing this file draws.**

## Decide yourself vs. ask

⚠ **The default is: decide, and carry it through to the end.**
Ask only where **being wrong cannot be walked back**.

| Decide yourself | Ask |
|---|---|
| Implementation order, how to split it, where tests go | ⚠ **Anything a human reads** on screen or in output |
| What to measure and how | ⚠ **When the scope moves** (crossing `Non-goals`) |
| A bug with exactly one sensible fix | ⚠ **When what may be claimed changes** (`docs/test-perspectives.md` § 6) |
| Adding docs, comments, tests | Two presentable options that **measurement cannot settle** |

⚠ **tenken: the owner answers the first and third rows once, in `docs/cli-reference.md`.**
⚠ **Output that `docs/cli-reference.md` already specifies, and a `docs/test-perspectives.md` § 6 row that a check asserts,
are the AI's to write and to merge through merge-gate.** ⚠ **Anything a user would see that
`docs/cli-reference.md` does not already allow — a new flag, a new rule ID, a changed exit code, a changed
JSON key, a claim beyond the scope in `docs/design.md` § 0 — is still Ask, and stops the loop.**

- MUST: Ask with **`AskUserQuestion`**. ⚠ **Never bury the question in prose** (it gets missed).
- MUST NOT: ⚠ **Never ask what measuring would settle.** Measure first.
- MUST: ⚠ **Asking stops the work.** Finish everything that does not depend on the answer
  **before** asking.
- MUST: ⚠ **Record what was asked.** ⚠ **Once decided, the owning document states the result as the current
  state, and the reason as present-tense grounds in `docs/design.md`** — ⚠ **no date, no "the owner decided"**;
  ⚠ **the question and the answer go in the PR or the issue.**
- SHOULD: ⚠ **When the thing being settled is how something looks, hand over the artefacts, not
  a description of them.**

## ⚠ An owner decision outranks your judgement

- MUST: ⚠ **Copy `Owner Decisions` out of an issue verbatim**, and never change one on your own
  judgement.
- MUST: ⚠ **A later comment overrides the issue body.** ⚠ **A decision made mid-flight often
  exists only in a comment.**
- MUST: ⚠ **When body and comment conflict and you cannot tell which is authoritative, ask.**
- MUST: ⚠ **When the issue, the spec, `docs/design.md` and the code disagree, do not decide which is
  right.** ⚠ **Return that a human must decide.**

## ⚠ `ready-for-ai`

⚠ **This is not a priority label.** It marks "an AI can carry this to the end without a human
deciding mid-way".

⚠ **The AI applies it, under the conditions below.** ⚠ **It never removes it.**

### ⚠ The gate did not get weaker. It moved.

```text
template  AI applies ready-for-ai  ->  ... -> PR  ->  ⚠ human approves the merge
tenken    AI applies ready-for-ai  ->  ... -> PR  ->  merge-gate PASS -> AI merges
                                                  '-> owner-only path -> merge-gate --owner-grant PASS -> AI merges
                                                  '-> stop label / HUMAN-DECISION -> ⚠ human approves the merge
          ⚠ and every publish (public, tag, release) -> ⚠ human, every time
```

⚠ **tenken diverges from the template here** ([`git.md`](git.md) § Permission).

- MUST: ⚠ **A human gate always exists, and it sits where things cannot be walked back.**
  ⚠ **That is every act of publishing, every stop label the owner puts on a PR, and every review
  that returns `HUMAN-DECISION`.** ⚠ **Never end up with zero.**
  ⚠ **Owner-only paths (what to build, what a user sees, `docs/design.md`, the rules, the gate itself) are
  merged by the AI under the owner's standing grant after a PASS review.** ⚠ **The owner reads them after the
  merge; a revert undoes them in the repository** (⚠ **not for anyone who already built from `main`**).
- MUST: ⚠ **Which PRs the AI may merge on its own is decided by `.claude/tools/merge-gate.mjs`
  alone** ([`git.md`](git.md) § Permission names the only two ways a PR gets merged).
  ⚠ **If you are about to merge without its `PASS` and without the owner's approval of that
  exact SHA, you have misread this file.**

### ⚠ When the AI may apply it

- MAY: ⚠ **Apply it only when [`../skills/issue-ready/SKILL.md`](../skills/issue-ready/SKILL.md)
  returns `Ready for AI: YES`**, ⚠ **and the verdict was posted to the issue first.**
  ⚠ **The reasoning goes on the issue before the label does** — ⚠ **a label with no verdict behind
  it is indistinguishable from one applied by mistake.**
- MUST: ⚠ **Apply it through one step that also records that the AI did it.**
  ⚠ **Never by hand, and never as a bare label edit.**
  ⚠ **Post-then-label-then-record as three commands drifts apart on the first busy day** —
  ⚠ **a rule every call site has to remember is not a rule, it is a hope.**
  ⚠ **Every project writes that step for itself; ⚠ this file names what it has to do, not how.**
- MUST: ⚠ **Re-run the gate immediately before work starts, every time**, even on an issue the AI
  labelled itself. ⚠ **Bodies and comments change after a label is applied.**

### ⚠ When the AI must not apply it

⚠ **Any one of these means no label.** ⚠ **Not "probably fine". No label.**

| # | ⚠ Condition | ⚠ Why |
|---|---|---|
| 1 | ⚠ **An owner decision on the issue is unresolved** | ⚠ **The owner has not decided yet** |
| 2 | ⚠ **A dependency the issue names is still open** | ⚠ **The work can be started and not finished** |
| 3 | ⚠ **The environment cannot run the verification the issue needs** | ⚠ **Then nobody can show it green** (`issue-ready` clause 10) |
| 4 | ⚠ **Anything the "Ask" column above covers is unsettled** | ⚠ **Scope, what may be claimed, anything a human reads** |
| 5 | `issue-ready` returned anything but `YES` | ⚠ **The gate already said no** |

- MUST NOT: ⚠ **Never remove the label.** ⚠ **Applying and removing are not symmetric:**
  ⚠ **removing one the owner applied overrides the owner.**
  ⚠ **When an issue should lose it, say so and stop.**
- MUST: ⚠ **When condition 1 or 4 is what stopped it, mark the issue as needing an owner decision
  and move to another issue.** ⚠ **Do not stall the whole queue on one owner question.**

### ⚠ Who applied it has to stay answerable

⚠ **Grounds: the question this change has to keep answering is "how often does the owner still
have to do it".** ⚠ **A number that only ever goes down without being looked at is not evidence
of autonomy; it is evidence that nobody looked.**

- MUST: ⚠ **Keep a record of the AI's own applications**, ⚠ **and compare it against what the
  host actually recorded.**
- MUST: ⚠ **Where the AI acts through the owner's own credentials, the host's actor field cannot
  separate them.** ⚠ **Using it anyway is dressing a guess as a measurement**
  ([`evidence.md`](evidence.md)).
  ⚠ **The owner's count is then a subtraction, ⚠ and it says so wherever it is printed.**
- MUST NOT: ⚠ **Never score it.** ⚠ **A count of zero is not a result.**

### ⚠ The label is still only an entry condition

- MUST: ⚠ **It is not a guarantee that the issue can be implemented.**
  ⚠ **Labels get applied by mistake — ⚠ including by the AI.**
- MUST: ⚠ **The Loop Controller re-runs the gate before touching anything, and stops on `NO`**
  ([`../skills/loop-controller/SKILL.md`](../skills/loop-controller/SKILL.md)).

## ⚠ Rewriting an issue

⚠ **The AI may rewrite an issue body.** ⚠ **Closing it stays with the human** (above).
⚠ **The label follows the section above.**

⚠ **Rewrite only what you measured.**

| May | ⚠ May not |
|---|---|
| Add the date and the PR to a finished item | ⚠ Change `Goal` / `Scope` / `Out of Scope` / `Owner Decisions` |
| Append a correction to a statement that is no longer true | ⚠ Delete or rewrite the original words |
| Note that a reference is gone | ⚠ Fix by guesswork (⚠ if you have not measured it, leave it) |
| Tick a box (⚠ only when measured) | ⚠ Close it |

⚠ **Never delete. Append.** ⚠ **Deleting the original words loses why they were written.**

⚠ **Not because it is old — because it is no longer true.**
⚠ **Time is not a reason to touch an issue.**

⚠ **What we stepped on**: ⚠ **we first reached for a periodic review — look over the issues
every N days.** ⚠ **The practice around auto-closing stale issues argues against it:
closing on time alone throws away context, silence is not low value, and it hides the triage
problem rather than solving it.** ⚠ **Review by time, and you gain a reason to touch the
issues that are still correct.**

### ⚠ Collect the appends under `## Updates`

⚠ **The body is the record of the time it was written.** ⚠ **`Updates` is the diff against now.**

```text
## Updates

### <date> — <state>        ⚠ partly solved / solved / the fact changed / the reference is gone

<what you confirmed>        ⚠ with the PR. ⚠ with the numbers when you measured

The body's "<the passage>" stays as the record of when it was written.
⚠ But as of now, <the fact>.

Unresolved:
- <what is left>
```

- MUST: ⚠ **Newest on top** (⚠ so the current fact reads first).
- MUST: ⚠ **Name the passage in the body.** ⚠ **Without it a reader trusts the body**
  (⚠ they cannot tell which part is stale).
- MUST: ⚠ **Keep the unresolved list.** ⚠ **A half-solved issue is not a solved one.**

⚠ **Without this, a long-lived issue collects corrections until the current fact is unreadable again.**

### ⚠ When to look

⚠ **Not on a clock.** ⚠ **At the seams of the work.**

```text
issue-ready     ⚠ the one issue you are about to take on (§4-2 there)
change-review   ⚠ the issue this PR touches, when a partial fix cannot carry `Closes`
```

⚠ **The other open issues are not looked at.**
⚠ **An issue nobody reads does no harm by being stale.** ⚠ **The harm is believing it while reading.**
⚠ **Looking at all of them turns a periodic review into a review pinned to a seam — the same thing.**

⚠ **This leaves a hole, and it is left open on purpose**: ⚠ **an issue that never reaches
`issue-ready` is never looked at.** ⚠ **Nothing reads the body until someone picks the issue
up.** ⚠ **No mechanism fills that.**
