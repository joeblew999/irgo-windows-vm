# Release prime time: everything a dev or agent needs from a download

Status: in progress (agent X) · 2026-10-01

## Symptom
Someone with only a release binary (no repo, no Go, no mise) — a developer or an AI agent — has no
obvious install path, `doctor` assumes a checkout in places, messages print repo-relative paths
(`docs/UPSTREAM.md`), the golden image is only usable if you build it yourself, and there is no
user-facing guide for MCP clients.

## Change
1. Install: Homebrew tap config (GoReleaser; tap repo `joeblew999/homebrew-tap` created by the main
   session), `curl | sh` installer that verifies SHA256SUMS and clears quarantine, `go install`.
2. First run: no-arg and `doctor` give the next step in order; repo-only commands say so; URLs not
   repo paths outside a checkout.
3. `vm-create` with no golden image pulls it from the private Worker cache when configured
   (IRGO_GOLDEN_URL/TOKEN), imports it as `irgo-golden`, clones; licence notice; else installs and
   says how to make the next VM fast.
4. Agent guide shipped with the release (MCP registration for Claude Code and other clients, tools,
   flows, exit codes, security model).
5. Changelog grouped; release notes; propose **v0.5.0**.
6. README short: purpose, install, 3-step start, agents link.

## Verify
`goreleaser check`; snapshot build run from outside the repo (`help`, `doctor`, `mcp -list`);
`go:check`/`go:lint`/`site:build`; the main session tests the golden auto-pull live, then tags v0.5.0.

## Closed — 1 Oct 2026

Done: v0.5.0 released 1 Oct (macOS + Linux/Windows clients, install.sh verified from a clean dir, cask in Casks/, agent guide, doctor next steps).
