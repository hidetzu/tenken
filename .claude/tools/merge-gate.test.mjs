// Holds merge-gate.mjs to its own conditions (.claude/rules/git.md § Permission leaves them to this code).
//
//   node --test ".claude/tools/*.test.mjs"          every case
//   node --test --test-name-pattern="owner-only" ".claude/tools/*.test.mjs"
//
// ⚠ No network. ⚠ The cases that run the CLI put a stub `gh` first on PATH and assert, from the
//   stub's own log, which `gh` calls were and were not made (`rules/verification.md`
//   § An exercise must not change the world). ⚠ Each also asserts the stub was reached at all,
//   or "merge was not called" would pass when the tool failed to start.

import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, readFileSync, chmodSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { decide, isOwnerPath, latestReview, REQUIRED_CHECKS } from "./merge-gate.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const HEAD = "abcdef1234567890abcdef1234567890abcdef12";
const review = (verdict, { sha = HEAD, ctx = "fresh context", at = "2026-10-18T10:00:00Z", before = "", login = "hidetzu", id = 2199131 } = {}) => ({
  created_at: at,
  user: { login, id },
  body: `${before}Verdict: ${verdict}\n\nReviewed at:\n  ${sha}\nReviewed in:\n  ${ctx}\nRead:\n  whole change\n`,
});
const ok = (name, workflowName = "ci") => ({ __typename: "CheckRun", name, workflowName, status: "COMPLETED", conclusion: "SUCCESS" });
const good = () => ({
  pr: {
    number: 9, state: "OPEN", isDraft: false, baseRefName: "main", headRefOid: HEAD, mergeable: "MERGEABLE",
    changedFiles: 2, labels: [], statusCheckRollup: [ok("docs-and-tools"), ok("go")],
  },
  files: [{ filename: "internal/check/cert.go", status: "added" }, { filename: "docs/test-perspectives.md", status: "modified" }],
  comments: [review("PASS")],
  compare: { behind_by: 0 },
});
const reasons = (s) => decide(s).reasons.join("\n");
const withFile = (s, f) => { s.files.push(f); s.pr.changedFiles = s.files.length; return s; };

test("passes when every condition holds", () => {
  assert.deepEqual(decide(good()), { ok: true, reasons: [] });
});

test("owner-only paths refuse", () => {
  for (const p of ["docs/cli-reference.md", "internal/walls/boundary_test.go", "internal/walls/zz_test.go", "go.work", "docs/design.md", "CLAUDE.md", ".claude/rules/git.md",
    ".claude/skills/change-review/SKILL.md", ".claude/tools/merge-gate.mjs", ".claude/tools/merge-gate.test.mjs",
    ".claude/tools/docs-check.mjs", ".claude/rules/go.md", ".claude/agents/x.md", ".claude/settings.json",
    ".github/workflows/ci.yml", "LICENSE"]) {
    const s = withFile(good(), { filename: p, status: "modified" });
    assert.equal(decide(s).ok, false, p);
    assert.match(reasons(s), /owner-only/, p);
  }
  // ⚠ a prefix is a directory, not a substring
  assert.equal(isOwnerPath("docs/design.md.bak"), false);
  assert.equal(isOwnerPath("docs/adrian.md"), false);
  assert.equal(isOwnerPath(".claudette/x"), false);
});

test("files the AI's tooling reads from any directory are owner-only", () => {
  for (const p of ["internal/CLAUDE.md", "docs/claude.md", "CLAUDE.local.md", "a/b/CLAUDE.local.md", ".mcp.json",
    "sub/.mcp.json", "internal/.claude/skills/x/SKILL.md", "pkg/.claude/settings.json", "x/.Claude/y"]) {
    assert.equal(isOwnerPath(p), true, p);
  }
  for (const p of ["docs/CLAUDE-notes.md", "internal/claude.go", "x/.claudette/y", "mcp.json"])
    assert.equal(isOwnerPath(p), false, p);
});

test("--owner-grant lets an owner-only path through and nothing else", () => {
  const s = withFile(good(), { filename: "docs/design.md", status: "added" });
  assert.equal(decide(s).ok, false);
  assert.deepEqual(decide(s, { ownerGrant: true }), { ok: true, reasons: [] });
  // ⚠ every other condition still holds under the grant
  const cases = [
    [(x) => { x.pr.labels = [{ name: "do-not-merge" }]; }, /do-not-merge/],
    [(x) => { x.pr.labels = [{ name: "owner-decision" }]; }, /owner-decision/],
    [(x) => { x.comments = [review("NEEDS-FIX")]; }, /does not say exactly "Verdict: PASS"/],
    [(x) => { x.comments = [review("HUMAN-DECISION")]; }, /HUMAN-DECISION/],
    [(x) => { x.pr.statusCheckRollup = [ok("docs-and-tools")]; }, /required check go/],
    [(x) => { x.compare.behind_by = 1; }, /behind main/],
    [(x) => { x.comments = [review("PASS", { sha: "1".repeat(40) })]; }, /not reviewed/],
    [(x) => { x.pr.changedFiles = 101; }, /cannot see the whole change/],
    [(x) => { x.comments = [{ ...review("PASS"), updated_at: "2026-10-18T12:00:00Z" }]; }, /edited after it was posted/],
    [(x) => { x.comments = [review("PASS", { ctx: "same context" })]; }, /fresh context/],
    [(x) => { x.comments = []; }, /no change-review return/],
    [(x) => { x.pr.isDraft = true; }, /draft/],
    [(x) => { x.pr.mergeable = "CONFLICTING"; }, /mergeable=CONFLICTING/],
  ];
  for (const [mutate, re] of cases) {
    const x = withFile(good(), { filename: "docs/design.md", status: "added" });
    mutate(x);
    const r = decide(x, { ownerGrant: true });
    assert.equal(r.ok, false);
    assert.match(r.reasons.join("\n"), re);
  }
});

test("owner-only paths are matched case-insensitively", () => {
  for (const p of [".Claude/rules/x.md", "claude.md", "DOCS/DESIGN.MD", ".GITHUB/workflows/x.yml"])
    assert.equal(isOwnerPath(p), true, p);
});

test("a rename out of an owner-only path refuses", () => {
  const s = withFile(good(), { filename: "docs/old-git.md", previous_filename: ".claude/rules/git.md", status: "renamed" });
  assert.match(reasons(s), /owner-only paths: .*\.claude\/rules\/git\.md/);
});

test("a deleted owner-only file refuses", () => {
  const s = withFile(good(), { filename: ".claude/rules/git.md", status: "removed" });
  assert.match(reasons(s), /owner-only/);
});

test("a file list shorter than the PR's own count refuses", () => {
  const s = good(); s.pr.changedFiles = 101;
  assert.match(reasons(s), /cannot see the whole change/);
});

test("the required check must have run and succeeded", () => {
  const none = good(); none.pr.statusCheckRollup = [];
  assert.match(reasons(none), /required check docs-and-tools \(workflow ci\) did not run/);
  const other = good(); other.pr.statusCheckRollup = [ok("some-other-app")];
  assert.match(reasons(other), /required check docs-and-tools \(workflow ci\) did not run/);
  const otherWorkflow = good(); otherWorkflow.pr.statusCheckRollup = [ok("docs-and-tools", "not-ci")];
  assert.match(reasons(otherWorkflow), /did not run/);
  // ⚠ a commit status of the same name is not the Actions check
  const status = good(); status.pr.statusCheckRollup = [{ __typename: "StatusContext", context: "docs-and-tools", state: "SUCCESS" }];
  assert.match(reasons(status), /required check docs-and-tools \(workflow ci\) did not run/);
  const skipped = good();
  skipped.pr.statusCheckRollup = [{ __typename: "CheckRun", name: "docs-and-tools", workflowName: "ci", status: "COMPLETED", conclusion: "SKIPPED" }];
  assert.match(reasons(skipped), /required check docs-and-tools \(workflow ci\) did not succeed/);
  const neutral = good();
  neutral.pr.statusCheckRollup = [{ __typename: "CheckRun", name: "docs-and-tools", workflowName: "ci", status: "COMPLETED", conclusion: "NEUTRAL" }];
  assert.match(reasons(neutral), /did not succeed/);
});

test("every required check must run: docs-and-tools alone is not enough", () => {
  const s = good(); s.pr.statusCheckRollup = [ok("docs-and-tools")];
  assert.match(reasons(s), /required check go \(workflow ci\) did not run/);
});

test("pending or failed CI refuses, including a StatusContext", () => {
  for (const c of [{ __typename: "CheckRun", name: "x", status: "IN_PROGRESS", conclusion: null },
    { __typename: "CheckRun", name: "x", status: "COMPLETED", conclusion: "FAILURE" },
    { __typename: "StatusContext", context: "x", state: "PENDING" }]) {
    const s = good(); s.pr.statusCheckRollup.push(c);
    assert.match(reasons(s), /CI is not green/);
  }
  const s = good(); s.pr.statusCheckRollup.push({ __typename: "StatusContext", context: "x", state: "SUCCESS" });
  assert.equal(decide(s).ok, true);
});

test("a branch behind main refuses", () => {
  const s = good(); s.compare.behind_by = 2;
  assert.match(reasons(s), /2 commit\(s\) behind main/);
});

test("a review of an older head refuses", () => {
  const s = good(); s.comments = [review("PASS", { sha: "1".repeat(40) })];
  assert.match(reasons(s), /a commit after the review was not reviewed/);
});

test("a short or missing SHA refuses", () => {
  const s = good(); s.comments = [review("PASS", { sha: HEAD.slice(0, 7) })];
  assert.match(reasons(s), /no full 40-character/);
});

test("only the latest review counts", () => {
  const s = good();
  s.comments = [review("PASS", { at: "2026-10-18T10:00:00Z" }), review("NEEDS-FIX", { at: "2026-10-18T11:00:00Z" })];
  assert.match(reasons(s), /does not say exactly "Verdict: PASS" \(NEEDS-FIX\)/);
});

test("a PASS from anyone but a reviewer is not a review", () => {
  const s = good(); s.comments = [review("PASS", { login: "someone-else" })];
  assert.match(reasons(s), /no change-review return/);
});

test("a stranger's later PASS does not hide the reviewer's NEEDS-FIX", () => {
  const s = good();
  s.comments = [review("NEEDS-FIX", { at: "2026-10-18T10:00:00Z" }), review("PASS", { at: "2026-10-18T11:00:00Z", login: "someone-else" })];
  assert.match(reasons(s), /does not say exactly "Verdict: PASS" \(NEEDS-FIX\)/);
});

test("the reviewer's login on another account (a renamed login taken over) is not a review", () => {
  const s = good(); s.comments = [review("PASS", { id: 99999999 })];
  assert.match(reasons(s), /no change-review return/);
});

test("a comment with no author is not a review", () => {
  const s = good(); const { user, ...anon } = review("PASS"); s.comments = [anon];
  assert.match(reasons(s), /no change-review return/);
});

test("the skill's unfilled template is not a PASS", () => {
  const s = good();
  s.comments = [{ created_at: "2026-10-18T10:00:00Z", user: { login: "hidetzu", id: 2199131 },
    body: `Verdict: PASS / NEEDS-FIX / HUMAN-DECISION\n\nReviewed at:\n  ${HEAD}\nReviewed in:\n  fresh context / ⚠ same context (<why>)\n` }];
  const r = reasons(s);
  assert.match(r, /does not say exactly "Verdict: PASS"/);
  assert.match(r, /does not say `Reviewed in: fresh context`/);
});

test("a later comment quoting an earlier PASS is not a review", () => {
  const s = good();
  s.comments = [review("NEEDS-FIX", { at: "2026-10-18T10:00:00Z" }),
    review("PASS", { at: "2026-10-18T11:00:00Z", before: "Answers to the findings. The earlier return was:\n\n" })];
  assert.equal(latestReview(s.comments).verdict, "NEEDS-FIX");
  assert.equal(decide(s).ok, false);
});

test("an edited review refuses", () => {
  const s = good(); s.comments = [{ ...review("PASS"), updated_at: "2026-10-18T12:00:00Z" }];
  assert.match(reasons(s), /edited after it was posted/);
  const same = good(); same.comments = [{ ...review("PASS"), updated_at: "2026-10-18T10:00:00Z" }];
  assert.equal(decide(same).ok, true);
});

test("a same-context review refuses", () => {
  const s = good(); s.comments = [review("PASS", { ctx: "same context (no subagent)" })];
  assert.match(reasons(s), /Reviewed in: fresh context/);
});

test("no review refuses", () => {
  const s = good(); s.comments = [{ created_at: "2026-10-18T10:00:00Z", user: { login: "hidetzu", id: 2199131 }, body: "LGTM" }];
  assert.match(reasons(s), /no change-review return/);
});

test("closed, not mergeable, stop labels, drafts and other bases refuse", () => {
  const cases = [
    [(s) => { s.pr.state = "CLOSED"; }, /not OPEN/],
    [(s) => { s.pr.mergeable = "CONFLICTING"; }, /mergeable=CONFLICTING/],
    [(s) => { s.pr.labels = [{ name: "owner-decision" }]; }, /owner-decision/],
    [(s) => { s.pr.labels = [{ name: "do-not-merge" }]; }, /do-not-merge/],
    [(s) => { s.pr.isDraft = true; }, /draft/],
    [(s) => { s.pr.baseRefName = "release"; }, /base is release/],
  ];
  for (const [mutate, re] of cases) { const s = good(); mutate(s); assert.match(reasons(s), re); }
});

// ⚠ ci.yml's jobs and REQUIRED_CHECKS are two copies of one decision (what "green" means).
//   A job CI runs that the gate does not require is a job whose failure the gate ignores;
//   a required job CI no longer runs makes every PR unmergeable. Hold them together.
test("REQUIRED_CHECKS names exactly the jobs in .github/workflows/ci.yml", () => {
  const yml = readFileSync(join(HERE, "..", "..", ".github", "workflows", "ci.yml"), "utf8");
  const name = /^name:\s*(\S+)\s*$/m.exec(yml)?.[1];
  const jobsAt = yml.search(/^jobs:\s*$/m);
  assert.ok(name && jobsAt >= 0, "ci.yml has no top-level name or jobs — the cross-check would pass vacuously");
  const jobs = [...yml.slice(jobsAt).matchAll(/^  ([A-Za-z0-9_-]+):\s*$/gm)].map((m) => m[1]).sort();
  assert.ok(jobs.length > 0, "found no jobs in ci.yml");
  assert.deepEqual(REQUIRED_CHECKS.map((c) => c.job).sort(), jobs);
  for (const c of REQUIRED_CHECKS) assert.equal(c.workflow, name);
  // ⚠ The gate matches the check-run name, which is the job id only while the job has no `name:`.
  assert.doesNotMatch(yml.slice(jobsAt), /^    name:/m, "a job in ci.yml has a name: — merge-gate matches check-run names, so keep job ids as the names");
});

// ── the CLI against a stub gh ──────────────────────────────────────────────


function runWithStub(state, args, { failMerge = false, failComment = false, secondState = null } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "merge-gate-"));
  const log = join(dir, "calls.log");
  const put = (name, v) => writeFileSync(join(dir, name), JSON.stringify(v));
  put("pr.json", state.pr); put("files.json", [state.files]); put("comments.json", [state.comments]);
  put("compare.json", state.compare);
  if (secondState) put("pr2.json", secondState.pr);
  // ⚠ The second `pr view` returns pr2.json when given — to model a change between the two reads.
  writeFileSync(join(dir, "gh"), `#!/bin/sh
echo "$*" >> "${log}"
case "$*" in
  "pr view"*) if [ -f "${dir}/pr2.json" ] && [ -f "${dir}/seen" ]; then cat "${dir}/pr2.json"; else touch "${dir}/seen"; cat "${dir}/pr.json"; fi ;;
  *"/files"*) cat "${dir}/files.json" ;;
  *"/comments"*) cat "${dir}/comments.json" ;;
  *"/compare/"*) cat "${dir}/compare.json" ;;
  "pr merge"*) ${failMerge ? 'echo "Head branch was modified" >&2; exit 1' : "true"} ;;
  "pr comment"*) ${failComment ? 'echo "HTTP 502" >&2; exit 1' : "true"} ;;
esac
`);
  chmodSync(join(dir, "gh"), 0o755);
  const r = spawnSync(process.execPath, [join(HERE, "merge-gate.mjs"), ...args],
    { env: { ...process.env, PATH: `${dir}:${process.env.PATH}` }, encoding: "utf8" });
  return { ...r, calls: existsSync(log) ? readFileSync(log, "utf8").trim().split("\n") : [] };
}
const called = (r, prefix) => r.calls.some((c) => c.startsWith(prefix));

test("--dry-run on a passing PR never calls merge", () => {
  const r = runWithStub(good(), ["9", "--dry-run"]);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.ok(called(r, "pr view 9"), r.calls.join("\n"));
  assert.ok(!called(r, "pr merge"), r.calls.join("\n"));
});

test("an unknown argument is a usage error and never merges", () => {
  const r = runWithStub(good(), ["9", "--dryrun"]);
  assert.equal(r.status, 2, r.stdout + r.stderr);
  assert.match(r.stderr, /unknown: --dryrun/);
  assert.ok(!called(r, "pr merge"));
});

test("a refused PR never calls merge, and exits 1", () => {
  const r = runWithStub(withFile(good(), { filename: "docs/design.md", status: "modified" }), ["9"]);
  assert.equal(r.status, 1);
  assert.ok(called(r, "pr view 9"));
  assert.ok(!called(r, "pr merge"));
});

test("a passing PR merges pinned to the head, then records it", () => {
  const r = runWithStub(good(), ["9"]);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  const merge = r.calls.find((c) => c.startsWith("pr merge"));
  assert.ok(merge, r.calls.join("\n"));
  assert.match(merge, new RegExp(`--match-head-commit ${HEAD}`));
  assert.ok(r.calls.indexOf(merge) < r.calls.findIndex((c) => c.startsWith("pr comment")));
});

test("a stop label added between the reads stops the merge", () => {
  const second = good(); second.pr.labels = [{ name: "do-not-merge" }];
  const r = runWithStub(good(), ["9"], { secondState: second });
  assert.equal(r.status, 1, r.stdout + r.stderr);
  assert.match(r.stdout, /re-read just before merging/);
  assert.ok(r.calls.filter((c) => c.startsWith("pr view 9")).length === 2);
  assert.ok(!called(r, "pr merge"));
});

test("a failed merge call is exit 2, not a refusal, and posts no record", () => {
  const r = runWithStub(good(), ["9"], { failMerge: true });
  assert.equal(r.status, 2, r.stdout + r.stderr);
  assert.ok(called(r, "pr merge"));
  assert.ok(!called(r, "pr comment"));
});

test("merged but unrecorded is exit 3, and says so loudly", () => {
  const r = runWithStub(good(), ["9"], { failComment: true });
  assert.equal(r.status, 3, r.stdout + r.stderr);
  assert.match(r.stderr, /WAS MERGED/);
});

test("--owner-grant merges an owner-only PR and records the grant", () => {
  const st = withFile(good(), { filename: ".claude/rules/git.md", status: "modified" });
  const refused = runWithStub(st, ["9"]);
  assert.equal(refused.status, 1);
  assert.ok(!called(refused, "pr merge"));
  const r = runWithStub(st, ["9", "--owner-grant"]);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.ok(called(r, "pr merge"));
  const comment = r.calls.find((c) => c.startsWith("pr comment"));
  assert.match(comment, /owner's standing grant/);
});

test("gh failing to read is exit 2, not a refusal", () => {
  const dir = mkdtempSync(join(tmpdir(), "merge-gate-"));
  writeFileSync(join(dir, "gh"), "#!/bin/sh\nexit 1\n"); chmodSync(join(dir, "gh"), 0o755);
  const r = spawnSync(process.execPath, [join(HERE, "merge-gate.mjs"), "9"],
    { env: { ...process.env, PATH: `${dir}:${process.env.PATH}` }, encoding: "utf8" });
  assert.equal(r.status, 2);
});
