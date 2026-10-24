#!/usr/bin/env node
// Hold the documents to what they say about themselves.
//
// ⚠ **These are the cases this template can assert about any project that copies it.**
//   ⚠ **They are not this project's verification** — that is `.claude/skills/verify/SKILL.md`,
//   ⚠ **and every project writes its own** (README §3).
//
// ⚠ **Read-only.** Nothing here writes, and nothing here reaches the network.
//
// ## Usage
//
//   node .claude/tools/docs-check.mjs                 run every case
//   node .claude/tools/docs-check.mjs --list          name the cases without running them
//   node .claude/tools/docs-check.mjs --only=links    run one case
//
// ⚠ **The count is announced here, at the moment it runs** (`rules/evidence.md`).
// ⚠ **Never copy it into a document.**
//
// Exit: 0 when every case that ran passed, 1 otherwise.

import { readFileSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";

const ROOT = process.env.CLAUDE_PROJECT_DIR
  ?? join(dirname(fileURLToPath(import.meta.url)), "..", "..");

const read = (p) => readFileSync(join(ROOT, p), "utf8");

// ⚠ Strip before reading a document, or a check picks up the very words written to describe it
//   (`CLAUDE.md` §5). ⚠ Fenced blocks go too: an example of a bad shape is not the bad shape.
// ⚠ Keeps the newlines of what it strips, so a line number counted afterwards is the file's.
const blank = (m) => m.replace(/[^\n]/g, "");
const stripMarkdown = (s) => s
  .replace(/```[\s\S]*?```/g, blank)
  .replace(/<!--[\s\S]*?-->/g, blank);

// ⚠ `-z`: without it, git quotes and octal-escapes any non-ASCII path (`core.quotePath`, on by
//   default), and the escaped string is not a file. ⚠ No tracked file has a non-ASCII name today; the -z handling stays for the next one
//   (CI sets `core.quotePath true`, so the escaped form is what a run sees whatever the machine's default).
const markdownFiles = () =>
  execFileSync("git", ["ls-files", "-z", "*.md"], { cwd: ROOT, encoding: "utf8" })
    .split("\0").filter(Boolean);

// ── the cases ──────────────────────────────────────────────────────────────
// Each returns { ok, said } — `said` is what gets printed either way, so a pass
// and a failure are read the same way.

const CASES = [
  {
    name: "links",
    // ⚠ A link that does not resolve sends the reader to a file that is not there. The rules
    //   cross-reference constantly ("owned by X"), so a rename quietly breaks the ownership map.
    run() {
      const broken = [];
      let checked = 0;
      for (const f of markdownFiles()) {
        for (const m of stripMarkdown(read(f)).matchAll(/\[[^\]]*\]\(([^)\s]+)\)/g)) {
          const target = m[1];
          if (/^(https?:|mailto:|#)/.test(target)) continue;
          const path = target.split("#")[0];
          if (!path) continue;
          checked++;
          if (!existsSync(resolve(join(ROOT, dirname(f)), path))) broken.push(`${f} -> ${target}`);
        }
      }
      return broken.length
        ? { ok: false, said: `${broken.length} of ${checked} relative links do not resolve:\n      ` + broken.join("\n      ") }
        : { ok: true, said: `every relative link resolves (${checked} checked, in ${markdownFiles().length} files)` };
    },
  },
  {
    name: "no-count-in-measurements",
    // ⚠ Grounds: `rules/evidence.md` — a count written into a document is stale from the moment
    //   it is written, and it makes every parallel change conflict. ⚠ Without this, that rule is
    //   a promise and not a wall (evidence.md says so itself).
    // ⚠ The section read: the claims, docs/test-perspectives.md § 6.
    // ⚠ Add this project's own shapes here. ⚠ Never remove one to make a document pass.
    run() {
      const COUNT = [
        /\b\d+\s+(?:cases?|checks?|tests?|rows?|entry\s+points?|files?)\b/i,
        /\d+\s*(?:件|本|か所)/,
      ];
      // ⚠ The claims section. ⚠ A missing file or section fails: reading nothing and finding
      //   nothing look the same.
      const SECTIONS = [
        { f: "docs/test-perspectives.md", heading: /^## 6\. What is implemented/, name: "## 6. What is implemented" },
      ];
      const hits = [];
      for (const { f, heading, name } of SECTIONS) {
        if (!existsSync(join(ROOT, f))) return { ok: false, said: `${f} is missing, so nothing was read` };
        const lines = stripMarkdown(read(f)).split("\n");
        const start = lines.findIndex((l) => heading.test(l));
        if (start < 0) return { ok: false, said: `${f} has no "${name}" section, so nothing was read` };
        const rest = lines.slice(start + 1);
        const end = rest.findIndex((l) => /^## /.test(l));
        (end < 0 ? rest : rest.slice(0, end)).forEach((l, i) => {
          if (COUNT.some((re) => re.test(l))) hits.push(`${f}:${start + i + 2}: ${l.trim().slice(0, 70)}`);
        });
      }
      return hits.length
        ? { ok: false, said: `a count is written into what may be claimed (${hits.length}). ⚠ Whatever produced it announces it:\n      ` + hits.join("\n      ") }
        : { ok: true, said: "no count is written into docs/test-perspectives.md § 6 (counts are announced by the runner)" };
    },
  },
];

// ── the runner ─────────────────────────────────────────────────────────────

const args = process.argv.slice(2);
const only = (args.find((a) => a.startsWith("--only=")) ?? "").slice("--only=".length);

// ⚠ Counting must not load anything heavy (`rules/verification.md`).
if (args.includes("--list")) {
  console.log(`docs-check: ${CASES.length} cases, none run`);
  for (const c of CASES) console.log(`  ${c.name}`);
  process.exit(0);
}

const chosen = only ? CASES.filter((c) => c.name === only) : CASES;
if (only && !chosen.length) {
  console.error(`docs-check: no case named "${only}". ⚠ --list names them.`);
  process.exit(1);
}

// ⚠ Announce the subset on the first line, before anything else (`rules/verification.md`).
console.log(only
  ? `docs-check: running 1 of ${CASES.length} cases (--only=${only})`
  : `docs-check: running ${chosen.length} of ${CASES.length} cases`);

let failed = 0;
for (const c of chosen) {
  let r;
  try { r = c.run(); }
  catch (e) { r = { ok: false, said: `the case could not run: ${e.message}` }; }
  if (!r.ok) failed++;
  console.log(`  ${r.ok ? "\x1b[32mok\x1b[0m  " : "\x1b[31mFAIL\x1b[0m"} ${c.name} — ${r.said}`);
}

console.log(failed
  ? `\ndocs-check: ${failed} of ${chosen.length} cases failed`
  : `\ndocs-check: ${chosen.length} of ${chosen.length} cases passed`);
process.exit(failed ? 1 : 0);
