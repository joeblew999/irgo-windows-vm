# Drive a Mac from Windows, Linux and GitHub

Status: built, live clone proof pending · 2026-10-01

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

## Built (1 Oct 2026, branch worktree-agent-ab6405a7ff21f8e0f)

Design changes from the proposal, with reasons:
- **Queue = one R2 object under compare-and-swap, not a Durable Object.** workers-go can call a
  DO but not define one (its class would be JavaScript outside `go test`); R2's conditional put
  (`onlyIf etagMatches` / `etagDoesNotMatch "*"`) gives a linearisable index. Measured live: 15
  parallel submits, 15 in the index, 0 refused.
- **The Mac polls (3 s), no WebSocket.** One class B R2 read per poll; nothing held open.
- **Commands are `remote-submit/-status/-logs/-result/-cancel`**, with `remote submit` accepted as
  a spelling, so they are MCP tools and reference entries with no second mechanism.
- **Exit code 8 `not-run`** (7 became Z's `no-room` on main first).
- **Worker is also a remote MCP server** (`POST /api/mcp`, stateless Streamable HTTP), each tool one
  in-process request to the HTTP API with the caller's token.
- **Each job is admitted like vm-create** (`BeginCreate`: room check, owner `remote:<caller>/<job>`)
  and cloned with `CloneFromGolden`; no golden image refuses rather than installing.

Where: `worker/jobs*.go` (queue, MCP), `internal/remote` (client + runner loop, OS-neutral),
`cmd/irgo-winvm/remote.go`, `serve.go`, `MacOnly` in `command.All`, `.github/actions/run`,
`.github/workflows/remote.yml`; docs in FOR-AGENTS, WORKER, ARCHITECTURE, USING, THREAT-MODEL.

Measured / proven:
- Deployed (bucket `irgo-jobs`, 7-day lifecycle, three secrets); no token → 401 on every queue path.
- Found live: `time.Sleep` inside a TinyGo Worker request never returns (hung 12 of 20 requests);
  replaced with a setTimeout wait. Recorded in WORKER.md traps.
- Linux client (linux/arm64 in an alpine container) → live Worker: submit, upload, queued,
  status, cancel → exit 8; `vm-create` there refuses with exit 2 pointing at remote-submit;
  listing with a caller token refused 403.

Pending:
- Live end-to-end on a clone (Linux client → Mac `serve` → clone → result → clone deleted) —
  waiting for the single VM slot.
- GitHub action run from a scratch branch (`scratch/remote-*`), needs `serve` running; the repo
  secret `IRGO_REMOTE_TOKEN` (the `ci` caller) is set.
- Joins: X's GoReleaser windows/linux targets (the action already tries `irgo-winvm-<os>-<arch>[.exe]`
  and falls back to `go install` at its own ref); FF's `wire/` route table (handlers are one
  function per route in `worker/jobs.go`, client one method per route in `internal/remote/client.go`);
  ledger events for remote jobs come for free through the commands' own recording on the Mac only
  for runTool paths — `serve`'s executor calls the library, so a `remote-job` ledger event is not
  yet emitted.
