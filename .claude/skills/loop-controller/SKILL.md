---
name: loop-controller
description: Carry exactly one ready-for-ai issue through gate, plan, owner approval, implement, verify, review and PR. Use for "run #<N> through the loop" or "loop-controller #<N>". Never picks an issue on its own.
---

# Loop Controller v1

⚠ **The controller holds no judgement of its own.** It calls, and transitions on the verdict returned.

```
PRECHECK -> READY_CHECK -> PLAN -> WORK -> PR -> CI -> ⚠ MERGE APPROVAL -> MERGE -> STOP
                  ^                  |                   ^
                  '--- back on a NO -'                   '-- ⚠ the one human gate
```

⚠ **The human gate is at the merge, not at the start**
([`owner-decisions.md`](../../rules/owner-decisions.md) § `ready-for-ai` says why).
⚠ **The number of human gates per issue is one. ⚠ Never let it become zero.**

⚠ **v1 is not a queue processor.** One issue. ⚠ **Never patrols on its own.**

## ⚠ Never, under any circumstances

```
auto-merge (--auto / merge queue)     ⚠ merge without the owner approving it
merge bypassing protection (--admin)  ⚠ remove ready-for-ai
pick an issue                         rewrite or split an issue
switch to a different issue           create a new issue
widen the scope                       decide the spec or the behaviour
push to the default branch            skip a check or a review
merge on red or in-progress CI        retry without limit
skip a gate and merge
```

⚠ **`apply ready-for-ai` is not on this list.** ⚠ **`remove` is, and always will be** —
⚠ **removing one the owner applied overrides the owner.**

⚠ **Never copy a judgement standard into this file.** The list of checks, the issue criteria —
each belongs to its own skill (rule: never two implementations of the same question).

---

## 1. PRECHECK

### With no issue number

⚠ **Never pick one.** List candidates and stop.

```bash
gh issue list --state open --label ready-for-ai
```

```
STOP: no target
Candidates: <list>
⚠ Say which one by number. This will not choose.
```

### With an issue number

```bash
gh issue view <N> --json number,title,body,labels,state
gh issue view <N> --comments
```

⚠ **Stop when**

| What | ⚠ Why stop |
|---|---|
| `state` is `CLOSED` | Never touch something already finished |
| ⚠ **an owner decision on it is unresolved** | ⚠ **Never label past it, and never start it** |

### ⚠ When `ready-for-ai` is absent

⚠ **This is no longer a stop.** ⚠ **It is a gate to run.**

- Run [`issue-ready`](../issue-ready/SKILL.md).
- On `YES`, apply the label ([`owner-decisions.md`](../../rules/owner-decisions.md) owns when that
  is allowed — ⚠ **read it, it is not copied here**), then continue.
- ⚠ **On anything else, stop.** ⚠ **Report the verdict. Implement nothing.**

### Local state

```bash
git status --short
git branch --show-current
gh pr list --state open --search "<N>"
```

⚠ **Stop when**

- ⚠ **Uncommitted changes unrelated to this issue** -> `STOP: WORKTREE NOT CLEAN`.
  ⚠ **Never stash, discard, or commit them**
- ⚠ **An open PR already closes this issue** -> ⚠ **Never open a second one.**
  If you cannot tell whether it is a continuation or a restart, stop

⚠ **Never commit to the default branch.** Branch as `<type>/<short-name>`.

---

## 2. READY_CHECK

⚠ **The label is an entry condition, not a guarantee that it can be implemented.**
Both body and comments change after a label is applied. Labels get applied by mistake.

-> ⚠ **Always run [`issue-ready`](../issue-ready/SKILL.md)** (`YES` / `NO`).

⚠ **On `NO`, implement nothing.**

```
STOP: ISSUE NOT READY
Issue: #N / Label: ready-for-ai / Quality Gate: NO
Unresolved: <verbatim>
⚠ Not one line has been implemented.
```

⚠ **Never rewrite the issue, remove the label, close it, or fill in the spec.**
⚠ **Report, and nothing else.**

---

## 3. PLAN

Use the planning stage of [`issue-work`](../issue-work/SKILL.md).
⚠ **Never copy its contents here.**

Produce:

```
Issue / Goal / Owner Decisions / Scope / Out of Scope
Files to touch (planned) / Acceptance Criteria
Verification Plan (which checks)
Review Plan (change-review)
⚠ Where this is likely to stop
```

⚠ **If something must be measured before it can be decided, measure it now and give the number.**

---

## 4. ⚠ The execution contract (⚠ no gate here — the gate is § 7)

⚠ **Report the plan. ⚠ Do not wait on it.**

Once `READY_CHECK` returns `YES`, ⚠ **for that one issue only**, the following count as permitted:

```
implement / fix / inner verify / final verify / review
commit / ⚠ push to that branch / open the PR
```

⚠ **Not included** — ⚠ **and the first line is the one that matters**

```
⚠ MERGE. ⚠ It is a gate, and it is taken every time (§ 7)
another issue / widening scope / deciding the spec or the behaviour
⚠ removing ready-for-ai / a broad refactor
⚠ auto-merge (--auto / merge queue) / ⚠ merge bypassing protection (--admin)
⚠ merge on red or in-progress CI
```

⚠ **The human enters at exactly one point — the merge.**
⚠ **Green CI does not mean the spec is right.** ⚠ **That is precisely why the gate is there and
not on the green tick.** ⚠ **The PR remains, so it stays readable afterwards.**

⚠ **This is the [`git.md`](../../rules/git.md) exception, made explicit and confined to the
controller.** ⚠ **Used standalone, every skill still takes permission every time.**
⚠ **Push and PR are covered here. ⚠ Merge is covered nowhere.**

### ⚠ Stop and ask, mid-run, whenever this appears

⚠ **These do not wait for the merge gate.** ⚠ **They stop the run where they are found.**

```
the scope would have to move (crossing Non-goals)
what may be claimed would have to change
anything the "Ask" column of owner-decisions.md covers
```

---

## 5. WORK (⚠ the only autonomous stretch)

```
implement (issue-work)
   |
inner verify (the fast tier)
   | PASS
final verify (the final gate)
   | PASS
review (change-review, ⚠ in a fresh context — its § 0)
   | PASS
PR
```

⚠ **Where each verdict goes**

| Returned | What to do |
|---|---|
| Verify `PASS` | Continue |
| Verify `FAIL` | ⚠ **If fixable within scope**, back to implement. **Counts as one round** |
| Verify `NOT-VERIFIED` | ⚠ **Stop by default.** Only when an external cause makes a retry sensible, **retry once under the same conditions**. Then stop. ⚠ **Never edit code to make it go away** |
| Review `PASS` | Continue |
| Review `NEEDS-FIX` | ⚠ **Only the in-scope findings**, then back to implement. **Counts as one round.** ⚠ **The re-review reads the delta** (`change-review` § 1) |
| Review `HUMAN-DECISION` | ⚠ **Stop immediately.** The controller does not decide |

⚠ **After a fix, run inner -> final -> review again.** Never skip.

### ⚠ Round limit

⚠ **Three rounds maximum** (the first implementation does not count).
On reaching it: `STOP: LOOP LIMIT REACHED`.

⚠ **The same cause, the same failing check, or the same finding repeating stops it early**
(`STOP: REPEATED FAILURE`). ⚠ **Turning the crank three times is not the point.**

⚠ **Nothing mechanically enforces this limit.**
⚠ **A static check could only confirm the limit is written down**, which is not the same as
confirming it was honoured. ⚠ Operate knowing that.

---

## 6. PR

⚠ **Only when all of these hold.**

```
[ ] Issue Quality Gate = YES              ⚠ re-run, not the label's word for it
[ ] Final verify = PASS
[ ] Required review = PASS
[ ] Unresolved human decisions = 0
[ ] Round limit not exceeded
[ ] ⚠ Every review finding answered        ⚠ Non-blocking and Pre-existing too (change-review § 6)
```

⚠ **`Owner approval` is not on this list.** ⚠ **It moved to § 7, in front of the merge.**

Include `Closes #<N>`, and make it visible that the controller ran it.

```
Loop Controller: Quality PASS / Verify PASS / Review PASS / round 2 of 3 / no decisions pending
⚠ ready-for-ai: applied by the AI on <date> / applied by the owner   ⚠ say which
⚠ Merge: awaiting owner approval
```

⚠ **The PR body also carries what the owner needs to judge the merge**, ⚠ **and the answer to
every review finding**.
⚠ **Grounds: the merge is the gate because it is the point that cannot be walked back**
([`owner-decisions.md`](../../rules/owner-decisions.md)). ⚠ **An owner not shown how far it can
be walked back, or what would be left behind, is judging the one thing the gate is for blind.**

```
## Testing
<what ran, which tier>  ⚠ and what did not
## Risk
<what this could break>  ⚠ and what the checks above would not catch
## How to revert
<the one step that undoes it>  ⚠ and what that step does not undo (data written, a message sent)
## Findings
- <class> — Fixed in <commit> / Declined: <reason> / Pre-existing: <file:line>, <how it was shown>, left as found
```

---

## 7. CI -> MERGE -> STOP

⚠ **After opening the PR, wait for CI.**

```
forbidden: --auto / merge queue / --admin / pushing to the default branch
```

### ⚠ MERGE APPROVAL (⚠ the one human gate)

⚠ **Ask with `AskUserQuestion`.** ⚠ **Never bury it in prose**
([`owner-decisions.md`](../../rules/owner-decisions.md)).

⚠ **Ask only once CI is green and § 6 holds.** ⚠ **Asking earlier spends the owner's attention on
something that may still fail.**

⚠ **What the question must carry**

```
what the change does, in one line
⚠ the head SHA being approved (git.md)
⚠ which tiers ran, and ⚠ which did not (⚠ a silent gap reads as "checked and fine")
⚠ how to revert it, ⚠ and what reverting does not undo
⚠ who applied ready-for-ai, and when
⚠ anything the run decided that the owner might have decided differently
```

- MUST: ⚠ **A `NO` is not a failure of the run.** ⚠ **Report and stop.**
- MUST NOT: ⚠ **Never re-ask after a `NO` by rephrasing it.**

### ⚠ Merge only when all of these hold

```
[ ] every item in §6
[ ] CI is entirely green (not one pending)
[ ] ⚠ the owner approved this merge, in this run
[ ] ⚠ the PR head is still the SHA the owner approved
```

⚠ **On a failure, split it apart first** ([`verification.md`](../../rules/verification.md)).

| Cause | What to do |
|---|---|
| Our defect | ⚠ **Back to WORK. Counts as one round** |
| External (the environment could not be built, a tooling install hung) | ⚠ **Retry once under the same conditions.** Then stop |

⚠ **Never edit code to silence CI.**
⚠ **Never merge while it is not green.**

### merge

```bash
gh pr merge <PR> --squash --delete-branch --match-head-commit <approved SHA>
```

⚠ **`--match-head-commit` is what makes the SHA a wall, not a note**: ⚠ **the host refuses the
merge if the head moved after the owner was asked.**

⚠ Then re-fetch the default branch and ⚠ **confirm the issue actually closed**
(that `Closes` took effect).

⚠ **After merging, stop. Never move on to the next issue.**

⚠ **This holds even though the AI can now open the gate itself.** ⚠ **Being able to open it is
not permission to walk through it unasked.** ⚠ **v1 is still not a queue processor.**

### Report (⚠ same shape when it stopped early)

```
Loop Controller Report

Issue:          #N
Quality:        PASS / NO
Owner approval: YES
What was done:  <summary>
Rounds:         2 / 3
Verify:         inner PASS / final PASS
Review:         change-review PASS
PR:             #XXX (merged / not merged)
CI:             all green / <what failed>
Stopped because: complete / <stop condition>
Unresolved:     none / <list>
```

---

## 8. dry-run

`loop-controller #<N> dry-run`

⚠ **Does**: fetch the issue / confirm `ready-for-ai` / re-run `issue-ready` /
produce the plan / confirm the stop conditions.

⚠ **Does not**

```
create a branch / change a file / commit / push / open a PR / modify the issue
```

---

## 9. Stop conditions (⚠ each stops immediately)

```
Issue Quality Gate = NO      an owner decision is unresolved
issue is CLOSED              ⚠ a named dependency is still open
⚠ the owner did not approve the merge
issue / comments / cli-reference.md / test-perspectives.md / design.md conflict and authority cannot be determined
it cannot be fixed without leaving scope
the spec or the behaviour would have to be newly decided
the meaning of a recorded value would have to change
the runtime structure would have to change substantially
Final verify = NOT-VERIFIED  Review = HUMAN-DECISION
the same failure repeats     round 3 reached
unrelated uncommitted changes exist
an open PR for this issue already exists
```

⚠ **Stopping is not a failure.** ⚠ **It means the boundary held.**
