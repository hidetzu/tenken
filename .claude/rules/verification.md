# Verification

⚠ **`MUST` = required, `SHOULD` = default, `MAY` = optional.**

⚠ **This file holds the contract.** ⚠ **It names no command, and it never will.**
⚠ **What to actually run is `.claude/skills/verify/SKILL.md`, and every project writes its own**
(⚠ **the template deliberately ships none** — see the README).

⚠ **This file is the owner of `CLAUDE.md` §2.** ⚠ **Never restate it there.**

## Grounds

⚠ **A clean build says nothing about what the thing actually does.**
⚠ **And a check that agrees only with itself has proved nothing.**

## The three tiers

⚠ **All three must exist.** ⚠ **They fail for different reasons, and the difference is the point.**

| Tier | What it sees | ⚠ What it cannot show |
|---|---|---|
| **Fast / inner** | What can be known without building an environment: reading the source, static checks, pure functions against fixtures | ⚠ **Nothing about how it behaves in the real thing** |
| **Final gate** | The whole thing exercised the way it is actually used, in an environment we build ourselves | ⚠ **Nothing about how anyone else behaves** |
| **External** | ⚠ **The other end is something we did not write** — another implementation, a real client, a captured trace | ⚠ **It is not evidence about our code when it is the other side that failed** (see below) |

- MUST: ⚠ **At least one check has the other end be something we did not write.**
  ⚠ **This is the tier that gets skipped.**
- MUST NOT: ⚠ **Never assert what the other side will do right now.**
  ⚠ **Assert our own correctness, and record what actually came back before judging.**
- SHOULD: ⚠ **Depend on nobody else's uptime.** ⚠ **A check whose result depends on a third party
  being up cannot assert our correctness.** ⚠ Prefer a fixture, and say when it was captured.
- MUST: ⚠ **Name each tier after what separates it**, not after how true it feels.
  ⚠ **If the name says how true the test is rather than what it needs, it will mislead.**

## Every entry point must be able to run in part

⚠ **A check suite that can only run whole gets skipped.**

- MUST: ⚠ **Run one named case only.**
- MUST: ⚠ **Count without running** (⚠ must not load anything heavy).
- MUST: ⚠ **Announce which subset it ran, on its first line of output.**
- MUST: ⚠ **The runner announces the count.** ⚠ **Never write it into a document**
  ([`evidence.md`](evidence.md)).

## ⚠ Am I measuring what I think I am measuring?

- MUST: ⚠ **Confirm the thing under test is the one just built, and the environment under test is
  the one just created.** ⚠ **A stale artefact or a leftover environment measures the previous run.**
- MUST: ⚠ **When something else could be holding the port, the socket, the device or the lock,
  confirm which one is being measured** before believing the result.

### ⚠ A check that reads the index keeps passing after the rule moves out

⚠ **Where a rule has an index entry and an owning file, a check that greps the index is
measuring the pointer, not the rule.** ⚠ **Move the rule out and the check stays green** —
⚠ **the pointer line still carries the words it greps for.**

⚠ **Stepped on where this template came from**: ⚠ **a check asserted "the label's meaning is
written down" by grepping the index file for the label's name.** ⚠ **The rule moved to its own
file; ⚠ the pointer that replaced it still said the label's name; ⚠ the check stayed green while
measuring nothing.**

- MUST: ⚠ **Point the check at the owning file**, ⚠ **not at whatever file happens to mention
  the subject.**
- MUST: ⚠ **Require enough of the rule that deleting the rule fails the check** —
  ⚠ **not merely that the subject is mentioned somewhere.**
- MUST: ⚠ **Fail when the pattern matches nothing at all.** ⚠ **Zero matches and zero problems
  are indistinguishable in the output**, ⚠ **so a format change silently turns the check off.**

## ⚠ An exercise must not change the world

⚠ **Grounds: a run that believes it is a rehearsal, and is not, does real damage while reporting
nothing unusual.** ⚠ **The belief is the dangerous part** — ⚠ **it is what makes the operator
reach for something they would otherwise think twice about.**

- MUST: ⚠ **Treat every outward call as real unless something explicitly blocked it.**
  ⚠ **Not "unless it looked like a test".** ⚠ **Blocked, by something you can point at.**
- MUST: ⚠ **Never read a flag, a mode, or a redirected output as a promise about side effects.**
  ⚠ **Redirecting where a run *records* is not redirecting what it *does*.**
  ⚠ **The two are separate, and the name of the switch will not tell you which one it is.**
  ⚠ **A "dry run" is only as dry as the code paths that actually check it.**
- MUST: ⚠ **When a tool can tell it is being exercised, it refuses to change anything outside
  this process.** ⚠ **Fail closed.** ⚠ **Say which flag means it, in the refusal.**
- MUST NOT: ⚠ **Never let "it is only a test" widen what a run may touch.**
  ⚠ **It is the other way round: ⚠ a test may touch less, never more.**
- SHOULD: ⚠ **Prove the refusal by making the outward call observable and asserting it did not
  happen** — ⚠ **a stub the run would have reached, and did not.**
  ⚠ **Reading the source to see that a branch returns early proves the branch, not the run.**
- MUST: ⚠ **Pair that with a control that reaches the stub.** ⚠ **Otherwise the check passes when
  the tool fails to start at all** (⚠ **it failed ≠ it failed for the reason intended**).

⚠ **This is the same shape as "am I measuring what I think I am measuring", one step out:**
⚠ **there, the risk is measuring the wrong thing; ⚠ here, it is changing the wrong thing while
believing you changed nothing.**

## Order

1. Fast / inner, while fixing. ⚠ **Stop and go back at the first failure.**
2. Final gate, before the PR. ⚠ **Green in the inner loop is not "it passed".**
3. ⚠ **After any fix, run the whole order again.** ⚠ Never skip forward.

## ⚠ Split the failure before blaming yourself

- MUST: ⚠ **Say plainly which side failed.**
- MUST: ⚠ **An external failure is still a `FAIL`** — ⚠ **but it is not evidence that our code broke.**
- MUST NOT: ⚠ **Never edit code to make a check go quiet.**

## When something is fixed

- MUST: ⚠ **Every fixed bug leaves a check behind.**
- MUST: ⚠ **Break the code on purpose and watch the check fail** —
  ⚠ **and read the failure text to confirm it failed for the reason intended.**

## What to return

```
Verdict: PASS / FAIL / NOT-VERIFIED

Ran:
  <entry point>  <result>  <count>      ⚠ never list what was not run

Failed:
  - <the failure text, verbatim>
  - <ours, or external>

Not verified:
  - <which check did not run, and why>

Regression guard:
  EXISTING / ADDED-AND-PROVEN / NONE    ⚠ would this stop next time?

Mutation check:
  <with the fix removed, did it FAIL ⚠ for the intended reason? ⚠ failure text verbatim>

Next:
  - <back to the work, or on to review>
```

### ⚠ Regression guard is one of exactly three

| Value | When |
|---|---|
| **EXISTING** | ⚠ **A check already catches this, ⚠ and that was confirmed by watching it catch it** |
| **ADDED-AND-PROVEN** | A check was added, ⚠ **broken on purpose, and seen to fail for the intended reason** |
| **NONE** | ⚠ **Neither.** ⚠ **State why** |

- MUST: ⚠ **There is no `ADDED`.** ⚠ Merely adding is not `ADDED-AND-PROVEN`.
- MUST: ⚠ **`EXISTING` is not "probably catches it."** ⚠ Without watching it fail, it is `NONE`.
- MUST: ⚠ **A bug fix with `NONE` is not a `PASS`.** Make it `NOT-VERIFIED` and say
  ⚠ **why it cannot be left as a check.**

### ⚠ "It failed" is not enough for the mutation check

⚠ **Read whether it failed for the reason you intended.** If it failed for another reason
(link order, a syntax error, a timeout), ⚠ **that check is not yet asserting the claim.**
⚠ **Record the failure text verbatim.** A summary makes the next reader redo the work.

| Verdict | When |
|---|---|
| **PASS** | ⚠ **Every tier ran and every tier passed.** ⚠ If a bug was fixed, ⚠ **Regression guard is not `NONE`** |
| **FAIL** | Anything failed. ⚠ **External causes are still FAIL** — say "external" and recommend a rerun |
| **NOT-VERIFIED** | ⚠ **A check did not run.** A partial run, a fork PR, or an environment that could not be built |

- MUST NOT: ⚠ **Never write `PASS` while a check went unrun.**
- MUST NOT: ⚠ **Never leave Regression guard or Mutation check empty.** Write "not applicable" if it is.
- MUST: ⚠ **Green CI does not mean the spec is right.** ⚠ **It means nothing contradicted what was
  asserted.** ⚠ **Know which checks CI does not run** — a fork PR, a privileged environment, a
  paid third party — ⚠ **and say so on the PR.**

## Priority, when deciding what to confirm first

1. Parsing and validation of what arrives from outside
2. State transitions
3. ⚠ **Malformed, truncated, hostile, and absent input**
4. The path a user actually exercises end to end
5. Performance detail

- MUST NOT: ⚠ **Never add a pile of checks that pin down the current implementation's steps.**
- SHOULD: ⚠ **Test the contract** — what goes in, what comes out, what is observable from outside.
