---
name: implementation-review-loop
description: Delegate a code change to an implementation agent, run independent code-review and code-quality agents in parallel, delegate fixes, and repeat both reviews until both return GREEN. Use when a user explicitly requests an agent-driven implementation with iterative review and quality approval before handoff.
---

# Implementation Review Loop

Orchestrate the work; do not collapse implementation and approval into one agent.

## 1. Establish the contract

1. Read the repository instructions, relevant specifications, and current working-tree state.
2. Identify the requested behavior, explicit exclusions, affected hosts or surfaces, and required quality gates.
3. Preserve unrelated user changes. Do not commit unless the user explicitly requests it.
4. Tell the user that the loop is starting and what counts as approval.

## 2. Delegate implementation

Launch one implementation agent with the complete contract and relevant repository context. Require it to:

- reproduce the current behavior at the closest practical end-to-end seam;
- implement the smallest complete change across every affected surface;
- update tests and source-of-truth documentation;
- run focused tests and applicable repository gates;
- avoid commits and unrelated edits; and
- report changed files, decisions, and exact validation results.

Inspect the resulting diff before review. Resolve only orchestration problems yourself; delegate product-code corrections through the loop.

## 3. Review in parallel

After the implementation agent finishes, launch exactly two independent agents concurrently. Give both the same fixed diff and requirements, and prohibit edits.

### Code reviewer

Ask for correctness against the request, repository standards, and specification. Require checks for missing behavior, incorrect behavior, regressions, scope creep, security, and cross-surface consistency. Use the repository's code-review skill when available.

### Code-quality reviewer

Ask for maintainability, simplicity, robustness, secret handling, error semantics, test quality, race safety, and all applicable quality gates. Require it to run the gates rather than trust the implementer's report.

Each reviewer must return exactly `GREEN` when it has no actionable findings. Otherwise it must provide severity, file and line, evidence, and the required correction. A vague concern is not a failed review.

## 4. Correct and repeat

If either reviewer reports a finding:

1. Combine both reports without discarding findings from either axis.
2. Launch a fresh correction agent with the current diff, original contract, and the complete findings.
3. Require focused regression tests and applicable gates; prohibit commits and unrelated edits.
4. When correction finishes, launch fresh code-review and code-quality agents in parallel over the entire current diff.
5. Repeat until both reviewers return `GREEN` in the same round.

Do not treat one prior `GREEN` as permanent after the diff changes. Do not ask reviewers to edit their own findings. If a finding conflicts with the user contract or repository rules, resolve the conflict explicitly before starting another round.

## 5. Final verification and handoff

After the double-green round:

1. Run any mandatory repository gates not already covered in that round.
2. Check formatting, the final diff, and working-tree status.
3. Report the implemented outcome, both green verdicts, validation commands and results, remaining limitations, and whether changes are uncommitted.

Never claim completion while either review is pending, any applicable gate is failing, or required work remains.
