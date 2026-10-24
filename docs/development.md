# DEVELOPMENT — a guide for developers and the owner

A guide to the documents for the people who build tenken. The explanation for users is the English [`README.md`](../README.md).
What can be claimed today is [`test-perspectives.md`](test-perspectives.md) §6; what comes next is the GitHub issues.
The test site is [`fixture/`](../fixture/) (how to use it: [`fixture/README.md`](../fixture/README.md)).
The README demo (`img/demo.gif`) is re-recorded with [`img/record-demo.sh`](../img/record-demo.sh) when the look of the output changes (what it needs, and what it does, are at the top of the script).

## Documents

| File | What it holds |
|---|---|
| [`docs/cli-reference.md`](cli-reference.md) | Everything a user sees: flags, rule IDs, exit codes, the shape of the output, the words. The owner decides it. |
| [`docs/discovery.md`](discovery.md) | What is not decided yet, known limitations. The place for proposals to change design.md §0 (what to build) |
| [`docs/test-perspectives.md`](test-perspectives.md) | Test perspectives. Before the implementation, what it means to say it "works" |
| [`docs/design.md`](design.md) | Design: what to build, how it is built now and why |
| [`CLAUDE.md`](../CLAUDE.md) | How to work (English) |
| [`.claude/rules/`](../.claude/rules/) | How to write it (English. The index is [`.claude/rules/README.md`](../.claude/rules/README.md)) |

The explanation for users is the English [`README.md`](../README.md) and [`docs/cli-reference.md`](cli-reference.md).
Do not write the same thing in two places. The scope goes only in design.md §0, what is undecided and the known limitations only in discovery.md, and what can be claimed only in test-perspectives.md §6.
The language rule is [`.claude/rules/README.md`](../.claude/rules/README.md) § Language.
