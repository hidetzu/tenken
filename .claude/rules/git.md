# Git

⚠ **`MUST` = required, `SHOULD` = default, `MAY` = optional.**

⚠ **This file is the owner of `CLAUDE.md` §8.** ⚠ **Never restate it there.**
⚠ **How to review a change is owned by [`change-review`](../skills/change-review/SKILL.md).**

## Commits

- MUST: Conventional Commits (`<type>(<scope>): <subject>`).
- MUST: ⚠ **Never sweep unrelated work in with `git add -A`.**
  ⚠ **One reason for a change, one commit.**
- MUST NOT: ⚠ **Never commit directly to the default branch.** Branch (`<type>/<short-name>`).
- SHOULD: Put **why** and **the measured numbers** in the body.

## Permission

⚠ **tenken diverges from the template here.**
⚠ **The grounds are the template's — a human stands at the point that cannot be walked back.**
⚠ **In tenken that point is publishing: a tag, a release, the repository's settings.** ⚠ **Only the owner
does those.** ⚠ **A merge to `main` is made by the AI through merge-gate, and a revert undoes it in the
repository** (⚠ **not for anyone who already built from `main`**). ⚠ **The owner's own decisions stay the owner's
to make; the AI may merge the PR that records them** (way 2 below), ⚠ **and the owner reads it afterwards.**

- MUST: ⚠ **The standing grant widens who merges, not who decides.** ⚠ **In `docs/cli-reference.md`, in the
  decisions of `docs/design.md` (what to build, what a user sees, the design), and in the AI's own permissions
  (this section), the AI writes only a decision of the owner (said in conversation, or an answer to
  `AskUserQuestion`), or restates or corrects an error in one.**
  ⚠ **A change of its own there goes as a proposal in [`docs/discovery.md`](../../docs/discovery.md) § 0, and is
  asked** — ⚠ **never merged on the grant.** ⚠ **The other rules, the skills, the gate, the walls and CI
  may be changed by the AI: they pass a PASS review, and the owner reads them after the merge.**
- MUST: ⚠ **Removing a label the owner applied is asked every time.**
- MAY: ⚠ **Push a non-default branch of this repository and open a PR without asking.**
  ⚠ **Never push to `main`.** ⚠ **Never push a tag.** ⚠ **Never force-push.**
- There are exactly two ways a PR gets merged. ⚠ **Nothing else merges** — ⚠ **not auto-merge
  (`--auto`), not a merge queue, not `--admin`, and never on red or in-progress CI.**
  1. ⚠ **The AI, through `.claude/tools/merge-gate.mjs`, on its `PASS`.** ⚠ **The conditions are
     that file's code and nowhere else** (⚠ never restate them here).
  2. ⚠ **An owner-only path, under the owner's standing grant.** ⚠ **When merge-gate
     refuses only because of an owner-only path, the AI may merge through
     `node .claude/tools/merge-gate.mjs <PR> --owner-grant`**, ⚠ **which keeps every other
     condition** (CI, the review PASS in a fresh context for this head, up to date with main) ⚠ **and
     records the grant on the PR.**
     ⚠ **A stop label (`owner-decision`, `do-not-merge`) still stops it, and so does a review that
     says `HUMAN-DECISION`: ask (`AskUserQuestion`).** ⚠ **Then permission covers the head SHA the
     owner was shown and nothing after it** — ⚠ **the owner merges it, or the AI runs
     `gh pr merge <PR> --squash --delete-branch --match-head-commit <the SHA shown>` and leaves a
     PR comment saying the owner approved it.**
- ⚠ **`.claude/skills/loop-controller/` is the template's (only its document names changed to tenken's), and stricter: it asks the
  owner before every merge.** ⚠ **Running it is allowed; it simply never uses way 1.**
- MUST: ⚠ **When it refuses for any other reason, fix the reason — never the gate.**
  ⚠ **A PR that changes the gate is owner-only. Under the owner's standing grant the AI may merge it
  through `--owner-grant` after a PASS review** — ⚠ **including a change that loosens the gate.**
  ⚠ **The owner chose this knowingly and reads it afterwards.**
- MUST: ⚠ **Ask every time, and never as part of a loop, for anything that publishes**:
  ⚠ **making the repository public, pushing a tag, a GitHub Release,
  changing repository settings or secrets.**

## ⚠ Never do these without being told

```text
git push --force
git reset --hard
git clean -fd
git checkout -- .
git restore .
```

- MUST: ⚠ **When one is genuinely needed, say what will be lost, first.**
- MUST: ⚠ **Never discard or overwrite someone's uncommitted work.**
- MUST: ⚠ **Never push to a branch someone else is working on without asking.**

## Before starting

- MUST: ⚠ **Look at the current branch and at uncommitted changes.**
- MUST: ⚠ **Keep our changes and theirs separate** (⚠ **a separate worktree makes this certain**).

## Conflicts

- MUST: ⚠ **Never discard the other side's change without understanding what it meant.**
- MUST: ⚠ **Re-run the checks after a merge or rebase.**

## ⚠ Never put the working environment into anything public

⚠ **Grounds: a public repository's commit bodies, PR bodies, issues and comments are readable by
anyone.** ⚠ **This is the mirror image of the evidence rules** ([`evidence.md`](evidence.md)):
⚠ **there, do not claim what was not observed; here, do not emit what was not meant to be emitted.**

⚠ **Apply it in a private repository too.** ⚠ **A repository's visibility can change; its history
does not get re-reviewed when it does.**

- MUST NOT: ⚠ **Never write an AI working-session URL.** ⚠ **Even when the contents are behind
  authentication, the session ids and how many there are can still be read.**
  ⚠ **Where a tool writes one by default, this rule wins.**
- MUST NOT: ⚠ **Never write a local absolute path** (`/home/<name>/…`), a hostname, or an internal URL.
- MUST NOT: ⚠ **Never write a token, a webhook, or an API key.**
- MUST: ⚠ **Write an issue with its repository name** (`owner/repo#N`).
  ⚠ **A bare number points at a different issue once anything is migrated.**

### ⚠ If it is already written, there are two places to erase, and one is not enough

```text
GitHub side    PR body, issue body, comments      ⚠ fixable with gh
git history    commit bodies                      ⚠ needs a rewrite and a force push
```

- MUST: ⚠ **Fixing the PR body does not fix the squash-merged commit body.** ⚠ They are separate.
- MUST: ⚠ **After a force push, an old commit can still be read by anyone who knows its SHA**,
  until the host collects it. ⚠ **Erasing it completely means asking the host.**
- MUST: ⚠ **Confirm it is genuinely unreadable before saying it was erased**
  ([`evidence.md`](evidence.md)).
