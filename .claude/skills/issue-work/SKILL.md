---
name: issue-work
description: Take an issue number and carry it through fetch, plan, implement, verify, self-review and PR. Use when implementing against an issue, or when an issue number appears in the request.
---

# From issue to PR

The argument is an issue number (`#` optional). With no number, ask which one.
⚠ **Never pick one.**

**Report each stage before moving to the next.** ⚠ Never run the whole thing silently.

---

## 1. Fetch the issue and its comments

```bash
gh issue view <N> --json number,title,body,labels,state,assignees
gh issue view <N> --comments
```

- **Read every comment.** ⚠ **A later comment overrides the body.**
  An owner decision made mid-flight often exists only in a comment.
- If `state` is `CLOSED`, stop and confirm before proceeding.

## 2. Read the rules

```
CLAUDE.md          how we work here
.claude/rules/     how we write it
docs/test-perspectives.md § 6  what may be claimed today
docs/design.md                 why it was decided that way
```

Read the `docs/design.md` sections covering what the issue touches.

## 3. Extract Owner Decisions and Non-goals

Copy these out of the issue **verbatim** and confirm them.

- **Owner Decisions** … highest priority. ⚠ **Never change one on your own judgement**
- **Non-goals** … never widen past this
- **Acceptance Criteria** … done is judged by these alone
- **Constraints** … issue-specific limits

⚠ **What outranks what, and when to stop and ask, is owned by
[`owner-decisions.md`](../../rules/owner-decisions.md).** ⚠ Not here.

## 4. Produce a plan

Before implementing, produce this briefly and **get it approved**.

- Which files, and what each change does
- For each acceptance criterion, ⚠ **what will confirm it** (which check, measured how)
- ⚠ **Anything needing an owner decision — raise it here, first**
- If something must be measured before it can be decided, ⚠ **measure it now and give the number**
  (`CLAUDE.md` §7: measure before polishing)
- ⚠ **Name the authority and the section** anything behavioural is being held to

## 5. Implement

- ⚠ **The smallest change that meets the goal.** No broad refactor unrelated to the issue
- Read the existing code first. ⚠ **Never build a second implementation of the same question**
- ⚠ **Never print internal state at a human.** Words a person reads live in one place
  (`CLAUDE.md` §4)
- Change code and fix **the comments, the checks and `docs/test-perspectives.md` § 6 with it**
  (⚠ **a stale comment misleads harder than stale code**)

## 6. Verify

⚠ **The contract is [`verification.md`](../../rules/verification.md); how to run it is
`.claude/skills/verify/SKILL.md`.** ⚠ Not here
(rule: never two implementations of the same question — ⚠ **one of them goes stale**).

Two obligations belong to this stage:

- ⚠ **Pass the final gate before opening the PR.** Green in the inner loop is not "it passed"
- ⚠ **Leave every bug fixed here behind as a check.**
  ⚠ **Then break it on purpose and confirm it really fails** —
  ⚠ **and read the failure text to confirm it failed for the intended reason**

## 7. Self-review

⚠ **What to look at is in [`change-review`](../change-review/SKILL.md).** ⚠ Not here.

`change-review` holds: ⚠ who reads it (a fresh context) / scope (AC, Out of Scope, one PR one reason) / the rules /
⚠ **stale results and ordering** / tidy-up (dead code, ⚠ stale comments) /
`PASS` `NEEDS-FIX` `HUMAN-DECISION`.

⚠ **Reading a summary produces no findings.** Read the diff.

## 8. Commit

⚠ **Owned by [`git.md`](../../rules/git.md)** — Conventional Commits, one reason one commit,
never the default branch, why and the numbers in the body. ⚠ Not here.

## 9. PR

- ⚠ **Take permission for `git push` every time** ([`git.md`](../../rules/git.md)).
  ⚠ **Permission granted before does not carry forward**
- Include `Closes #<N>`
- ⚠ **The body carries what [`loop-controller`](../loop-controller/SKILL.md) § 6 names** —
  Testing, Risk, How to revert, and the answer to every review finding. ⚠ Not restated here
- ⚠ **Note which checks did not run on this PR**, and watch the run on the default branch after merge

## 10. Report

Leave a `Completion Report` as a comment on the issue.
If anything needs a decision, ⚠ **stop there and ask.** Never decide it.

```
Summary                  what was done
Changed                  what moved
Verification Results     what ran, and how many
Remaining Issues         what was left
Owner Decision Required  what needs deciding
```

⚠ **A gap named in this report is not permission to claim it is closed anywhere else.**
⚠ **Saying it in one place and claiming it in another is how a false claim survives review.**
