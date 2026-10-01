# Drive a Mac from Windows, Linux and GitHub

Status: proposed · 2026-10-01

## Symptom
Only someone sitting at this Mac can use irgo-winvm. Developers and agents on Windows or Linux
machines, and GitHub Actions in other repos, cannot send a build to be tested on the Mac's VMs.

## Constraints (measured)
- The Mac accepts no inbound connections (macOS firewall stealth mode) — the Mac must connect OUT.
- `irgo-winvm mcp -http` exists with token auth, but needs an inbound port (THREAT-MODEL.md).
- The Worker is already deployed with R2 (golden cache, glaze status) and a token model.
- The tool cross-compiles for linux/windows today; releases ship darwin only.

## Proposed design (to be validated by an agent)
- The Mac runs `irgo-winvm serve` (outbound only): holds a connection to the Worker (Durable Object
  queue or WebSocket) and takes jobs.
- Clients (Windows/Linux/macOS release of the same binary, or MCP over HTTP to the Worker) submit
  jobs: upload a binary/test bundle to R2 through the Worker, request "run on a fresh clone,
  -gui, args, screenshots", stream logs, get the test2json/verdict/screenshots back.
- Each job runs on its own clone from the golden image (23 s), leased and recorded in the ledger,
  deleted after (shared-Mac plan).
- GitHub: a composite action `joeblew999/irgo-windows-vm/run@v1` that submits a job and fails the
  step on the verdict — other repos test on real Windows ARM64 (alongside GitHub's own runners).
- Release: GoReleaser adds windows and linux (amd64/arm64) for the client commands; Mac-only
  commands say so.

## Verify
A Linux box and a GitHub workflow in a scratch repo submit a glaze test; it runs on a clone here;
results and screenshots come back; ledger shows the job; clone deleted.
