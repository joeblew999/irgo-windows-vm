# Issue intake and triage for agent-filed issues

Status: done 1 Oct — intake merged; triage routine live

## Symptom
Other repos' agents will file issues here. Without structure they arrive missing the facts needed
to act (version, VM state, exact command/output), and upstream glaze/native/UTM bugs get filed as ours.

## Done
- `irgo-winvm report [-issue bug|feature|upstream]` (CLI + MCP): paste-ready diagnostic block,
  redacted (tested with planted secrets); every command now logs its exit line.
- Issue forms (bug / feature / upstream) + `config.yml`; `gh issue create` recipe for agents.
- 12 labels in `.github/labels.tsv`, synced with `mise run gh:labels` (synced 1 Oct).
- docs/CONTRIBUTING.md "Reporting issues (for agents)".

## Next
A scheduled cloud agent (routine) that triages new issues: label, ask for the report block when
missing (`needs-report`), route upstream bugs to the ledger, reproduce on a disposable VM where
possible, and open PRs for clear fixes — PRs reviewed, never pushed to main directly.

## Verify
File a test issue with the recipe as an agent would; the routine labels and answers it.

## Closed — 1 Oct 2026

Triage routine created: "irgo-windows-vm issue triage" (trig_018vorGqAkPDU9eY31yWwbi7), every 6 h at :36
UTC, Sonnet 5.5, GitHub connector. Labels, needs-report requests, upstream routing, duplicates
(closes only clear ones), small-fix PRs on triage/issue-N branches; never pushes to main or merges.
https://claude.ai/code/routines/trig_018vorGqAkPDU9eY31yWwbi7
