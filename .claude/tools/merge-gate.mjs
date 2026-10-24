#!/usr/bin/env node
// The one step through which the AI merges its own PR (.claude/rules/git.md § Permission).
//
// ⚠ **This is a wall, not a note.** ⚠ The conditions for an AI merge live here as code, so
//   "the AI may merge when …" is enforced by a refusal and not by remembering to check.
//   ⚠ **The rule is owned by `.claude/rules/git.md` § Permission; this file is its only
//   implementation** (`CLAUDE.md` § 3: never two implementations of one question).
//
// ⚠ **A refusal is an answer, not an obstacle.** ⚠ Never work around it — ⚠ not by editing the
//   protected list in the same PR, not by an AI merge with `gh` directly
//   (git.md § Permission, way 2 — `--owner-grant` below, or the owner-approved merge — is the only other way).
//   ⚠ This file is itself on the protected list, so a PR that changes it, loosening included, is merged only
//   through way 2.
//
// ## Usage
//
//   node .claude/tools/merge-gate.mjs <PR>             decide, and merge on PASS
//   node .claude/tools/merge-gate.mjs <PR> --dry-run   decide, print, ⚠ never merge
//   node .claude/tools/merge-gate.mjs <PR> --owner-grant   the same, but an owner-only path does not refuse:
//     ⚠ the owner's standing grant (.claude/rules/git.md § Permission). ⚠ Every other condition still holds — CI, the
//     review (PASS, fresh context, this head), up to date with main, no stop label. ⚠ A stop label still stops it.
//
// Exit:
//   0  merged (or, with --dry-run, would merge)
//   1  refused — ⚠ nothing was merged
//   2  could not decide or could not merge (usage, `gh` failed, the merge call failed)
//      — ⚠ nothing was merged
//   3  ⚠ MERGED, but the record of the AI's merge could not be posted. ⚠ Post it by hand.
//   ⚠ "The gate said no" (1), "the gate could not act" (2) and "it acted but did not record" (3)
//   are different facts (`.claude/rules/evidence.md`, tenken's lines).

import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

// ⚠ Paths whose change is the owner's to merge, always. ⚠ Why each one is, is on its line (how they are merged:
//   .claude/rules/git.md § Permission).
//   A prefix ending in "/" covers the directory. ⚠ Compared case-insensitively: a checkout on a
//   case-insensitive filesystem (macOS) puts `.Claude/rules/x` inside `.claude/rules/`.
//   ⚠ `.claude/` is covered whole: the gate, its tests, the checks CI runs, the hooks and what
//   they import all live there, and each of them decides what the AI may do.
//   ⚠ Root-anchored. Files the AI's tooling reads from ANY directory are `OWNER_ANYWHERE`.
export const OWNER_PATHS = [
  "docs/cli-reference.md", // what a user types and reads — the "Ask" column of owner-decisions.md
  "internal/walls/",      // the tests that hold code to owner decisions (design.md §2, docs/cli-reference.md). ⚠ A whole directory, so no
                          //   AI file joins the package (a TestMain would switch it off); ⚠ and the walls import nothing from this
                          //   module, so no AI init() runs inside them (design.md §2 § Walls)
  "go.work",              // a workspace can replace any module, like go.mod's replace
  "docs/design.md",       // what to build (§0) and the design — decisions that would otherwise be re-argued
  "CLAUDE.md",            // how we work
  ".claude/",             // rules, skills, hooks, tools (this gate, its test, docs-check), settings
  ".github/",             // what CI runs is what "green" means
  "LICENSE",
];

// ⚠ Claude Code reads these from any directory, not only the root: a nested `CLAUDE.md` or
//   `.claude/skills/` is loaded as instructions, `.mcp.json` adds tools. ⚠ Matched as a path
//   segment (a directory named `.claude`) or as a file name, at any depth.
export const OWNER_ANYWHERE = {
  dirs: [".claude"],
  files: ["claude.md", "claude.local.md", ".mcp.json"],
};

// ⚠ The checks that must have run and succeeded. ⚠ "Every check that ran passed" is not enough:
//   a rollup of only skipped checks passes that, and so does one check from any app.
//   ⚠ A check run from the Actions workflow, by job name — ⚠ never a commit status of the same name,
//   which anything holding the token can post.
export const REQUIRED_CHECKS = [{ workflow: "ci", job: "docs-and-tools" }, { workflow: "ci", job: "go" }];

// ⚠ Labels that mean "a human decides this one".
export const STOP_LABELS = ["owner-decision", "do-not-merge"];

// ⚠ The REST files endpoint stops at this many files. ⚠ Past it, the list is not the change.
const FILES_LIMIT = 3000;

export const isOwnerPath = (p) => {
  const l = p.toLowerCase();
  const parts = l.split("/");
  if (parts.slice(0, -1).some((d) => OWNER_ANYWHERE.dirs.includes(d))) return true;
  if (OWNER_ANYWHERE.files.includes(parts.at(-1))) return true;
  return OWNER_PATHS.some((o) => {
    const ol = o.toLowerCase();
    return ol.endsWith("/") ? l.startsWith(ol) : l === ol;
  });
};

// Every path a file entry touches: ⚠ a rename touches where it came from as well as where it went.
const touched = (f) => [f.filename, f.previous_filename].filter(Boolean);

// The change-review return, as posted to the PR (`.claude/skills/change-review/SKILL.md` § 6).
// ⚠ Strict on purpose. ⚠ A review is a comment whose FIRST line is exactly `Verdict: <one value>`
//   — so the skill's template line (`Verdict: PASS / NEEDS-FIX / …`) and a comment that quotes an
//   earlier return further down are not reviews. ⚠ The SHA must be the full 40 characters, and
//   `Reviewed in` must say exactly `fresh context`.
// ⚠ Only the latest review counts: an older PASS says nothing about a newer head.
export const latestReview = (comments) => {
  const reviews = comments
    .filter((c) => /^Verdict: \S/.test(c.body.trimStart()))
    .sort((a, b) => a.created_at.localeCompare(b.created_at));
  const c = reviews.at(-1);
  if (!c) return null;
  // ⚠ An edited review is not the review that was given. ⚠ Post a new one instead.
  const edited = Boolean(c.updated_at && c.updated_at !== c.created_at);
  const lines = c.body.trimStart().split(/\r?\n/);
  const verdict = /^Verdict: (PASS|NEEDS-FIX|HUMAN-DECISION)$/.exec(lines[0])?.[1] ?? null;
  const after = (label) => {
    const i = lines.findIndex((l) => l === `${label}:`);
    return i >= 0 ? (lines[i + 1] ?? "").trim() : null;
  };
  const at = after("Reviewed at");
  return {
    verdict,
    sha: at && /^[0-9a-f]{40}$/.test(at) ? at : null,
    fresh: after("Reviewed in") === "fresh context",
    edited,
  };
};

const checkName = (c) => c.name ?? c.context;
const checkSucceeded = (c) =>
  c.__typename === "StatusContext" ? c.state === "SUCCESS" : c.status === "COMPLETED" && c.conclusion === "SUCCESS";
const checkPassedOrSkipped = (c) =>
  checkSucceeded(c) || (c.status === "COMPLETED" && ["SKIPPED", "NEUTRAL"].includes(c.conclusion));

// ⚠ Pure: decides from what `gh` returned, touches nothing. ⚠ Every reason is collected, not
//   just the first — a refusal that names one problem at a time costs one round per problem.
//   Inputs: `pr` (gh pr view), `files` (REST pulls/N/files, every page), `comments` (REST
//   issues/N/comments, every page), `compare` (REST compare/main...head).
export function decide({ pr, files, comments, compare }, { ownerGrant = false } = {}) {
  const no = [];
  if (pr.state !== "OPEN") no.push(`the PR is ${pr.state}, not OPEN`);
  if (pr.isDraft) no.push("the PR is a draft");
  if (pr.baseRefName !== "main") no.push(`the base is ${pr.baseRefName}, not main`);
  if (pr.mergeable !== "MERGEABLE") no.push(`GitHub says mergeable=${pr.mergeable}`);

  // ⚠ A list that may be cut short is not the change. ⚠ Refuse rather than judge a part.
  if (files.length !== pr.changedFiles || files.length >= FILES_LIMIT)
    no.push(`the file list has ${files.length} entries and the PR says ${pr.changedFiles} — ⚠ cannot see the whole change`);
  const owned = [...new Set(files.flatMap(touched).filter(isOwnerPath))];
  if (owned.length && !ownerGrant) no.push(`it changes owner-only paths: ${owned.join(", ")} (⚠ merge with --owner-grant only under the owner's standing grant, .claude/rules/git.md § Permission)`);

  const stop = pr.labels.map((l) => l.name).filter((n) => STOP_LABELS.includes(n));
  if (stop.length) no.push(`it carries ${stop.join(", ")}`);

  // ⚠ "Nothing failed" and "nothing ran" print the same. ⚠ Name what must have run.
  const checks = pr.statusCheckRollup ?? [];
  for (const { workflow, job } of REQUIRED_CHECKS) {
    const c = checks.filter((x) => x.__typename === "CheckRun" && x.workflowName === workflow && x.name === job);
    if (!c.length) no.push(`the required check ${job} (workflow ${workflow}) did not run on this head`);
    else if (!c.every(checkSucceeded)) no.push(`the required check ${job} (workflow ${workflow}) did not succeed`);
  }
  const bad = checks.filter((c) => !checkPassedOrSkipped(c));
  if (bad.length) no.push(`CI is not green: ${bad.map((c) => `${checkName(c)}=${c.conclusion ?? c.status ?? c.state}`).join(", ")}`);

  // ⚠ Green on an old base is a result about a combination that will not exist after the merge.
  //   ⚠ The head must contain today's main, so the CI run above tested what lands.
  if (compare.behind_by !== 0) no.push(`the branch is ${compare.behind_by} commit(s) behind main — ⚠ update it and let CI run again`);

  const r = latestReview(comments);
  if (!r) no.push("no change-review return is posted on the PR");
  else {
    if (r.verdict !== "PASS") no.push(`the latest change-review does not say exactly "Verdict: PASS" (${r.verdict ?? "unreadable"})`);
    if (!r.sha) no.push("the latest change-review names no full 40-character `Reviewed at` SHA");
    else if (r.sha !== pr.headRefOid) no.push(`the review is of ${r.sha.slice(0, 12)}, and the head is ${pr.headRefOid.slice(0, 12)} — ⚠ a commit after the review was not reviewed`);
    if (!r.fresh) no.push("the latest change-review does not say `Reviewed in: fresh context`");
    if (r.edited) no.push("the latest change-review was edited after it was posted — ⚠ post a new review instead");
  }
  return { ok: no.length === 0, reasons: no };
}

const gh = (args) => execFileSync("gh", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
const ghPages = (path) => JSON.parse(gh(["api", path, "--paginate", "--slurp"])).flat();

function read(n) {
  const pr = JSON.parse(gh(["pr", "view", n, "--json",
    "number,state,isDraft,baseRefName,headRefOid,mergeable,changedFiles,labels,statusCheckRollup"]));
  return {
    pr,
    files: ghPages(`repos/{owner}/{repo}/pulls/${n}/files`),
    comments: ghPages(`repos/{owner}/{repo}/issues/${n}/comments`),
    compare: JSON.parse(gh(["api", `repos/{owner}/{repo}/compare/main...${pr.headRefOid}`])),
  };
}

const fail = (code, msg) => { console.error(`merge-gate: ${msg}`); process.exit(code); };
const firstLine = (e) => (e.stderr?.toString().trim() || e.message).split("\n")[0];

function main() {
  const args = process.argv.slice(2);
  // ⚠ An unknown argument is a usage error, never ignored: `--dryrun` must not merge for real.
  const unknown = args.filter((a) => !/^\d+$/.test(a) && a !== "--dry-run" && a !== "--owner-grant");
  const nums = args.filter((a) => /^\d+$/.test(a));
  if (unknown.length || nums.length !== 1)
    fail(2, `usage: merge-gate.mjs <PR number> [--dry-run] [--owner-grant]${unknown.length ? ` (unknown: ${unknown.join(" ")})` : ""}. ⚠ Nothing was decided.`);
  const dry = args.includes("--dry-run");
  const ownerGrant = args.includes("--owner-grant");
  const n = nums[0];

  let state;
  try { state = read(n); }
  catch (e) { fail(2, `could not read PR #${n} — ${firstLine(e)}. ⚠ Nothing was decided.`); }

  const { ok, reasons } = decide(state, { ownerGrant });
  const head = state.pr.headRefOid;
  console.log(`merge-gate: PR #${n} at ${head.slice(0, 12)}${dry ? " (--dry-run)" : ""}`);
  if (!ok) {
    console.log(`  REFUSED — ${reasons.length} reason(s):`);
    for (const r of reasons) console.log(`    - ${r}`);
    if (!ownerGrant) console.log("  ⚠ Only an owner-only path: run again with --owner-grant (the owner's standing grant, .claude/rules/git.md § Permission).");
    console.log("  ⚠ A stop label or a HUMAN-DECISION review: ask the owner. ⚠ Otherwise fix the reason — never the gate.");
    process.exit(1);
  }
  if (dry) {
    console.log("  PASS — would merge. ⚠ --dry-run: nothing was merged.");
    process.exit(0);
  }

  // ⚠ Read again right before merging: a label or a NEEDS-FIX posted since the first read must stop it.
  try {
    const again = read(n);
    const second = decide(again, { ownerGrant });
    if (!second.ok || again.pr.headRefOid !== head) {
      console.log("  REFUSED on the re-read just before merging:");
      for (const r of second.reasons) console.log(`    - ${r}`);
      if (again.pr.headRefOid !== head) console.log(`    - the head moved to ${again.pr.headRefOid.slice(0, 12)}`);
      process.exit(1);
    }
  } catch (e) { fail(2, `could not re-read PR #${n} before merging — ${firstLine(e)}. ⚠ Nothing was merged.`); }

  // ⚠ --match-head-commit: the host refuses if the head moved after this decision.
  try { gh(["pr", "merge", n, "--squash", "--delete-branch", "--match-head-commit", head]); }
  catch (e) { fail(2, `the merge call failed — ${firstLine(e)}. ⚠ Check the PR: nothing should have been merged.`); }

  // ⚠ Record that the AI merged it (owner-decisions.md: who acted has to stay answerable).
  //   ⚠ The AI acts through the owner's credentials, so the host's actor field cannot tell.
  try {
    gh(["pr", "comment", n, "--body",
      `merge-gate: merged by the AI at ${head} (.claude/rules/git.md § Permission)${ownerGrant ? " under the owner's standing grant for owner-only paths (--owner-grant)" : ""}. All conditions in .claude/tools/merge-gate.mjs held.`]);
  } catch (e) {
    fail(3, `⚠ PR #${n} WAS MERGED at ${head}, but the record could not be posted — ${firstLine(e)}. ⚠ Post it by hand.`);
  }
  console.log("  PASS — merged, and the merge recorded on the PR.");
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
