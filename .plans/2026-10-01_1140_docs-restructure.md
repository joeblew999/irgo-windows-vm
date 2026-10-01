# Docs restructure: one coherent set for users, agents and contributors

Status: proposed → agent EE · 2026-10-01

## Symptom
docs/ has grown by accretion: DEVELOPMENT.md is one 15-section file mixing architecture, commands,
conformance, golden image, R2, the Worker, drive, desktop hygiene and traps; CONTRIBUTING.md mixes
setup, issues, releases and the site; new features (golden image, Worker, ledger, remote, capacity,
VM conformance, report, shared Mac) land as sections wherever an agent put them. The owner: "the
docs are so so out of date. the structure too."

## Change
Restructure into focused pages by audience, each the single source for its topic, all rendered by
the site with a clear nav: e.g. Getting started (install, first VM, first app), Using it (commands,
exit codes, golden image, sharing a Mac, capacity), For agents (MCP, report/issues, remote),
Testing (glaze + VM conformance, drive, status pages), Architecture (layout, packages, locks, Worker,
ledger, data on disk), Contributing (setup, checks, releases, upstream workflow), Reference (traps,
results, upstream ledger, threat model, roadmap). Update every cross-link and anchor; keep the docs
tests (commands named exist, exit codes, anchors, links) green; README stays short.

## Order
Merged LAST in this round: agents Z, AA, BB, CC, DD are adding doc sections now; EE builds the new
structure, then merges main repeatedly and folds each new section in before finishing.

## Verify
site:build; link + anchor tests; every command documented; a newcomer read-through with screenshots.
