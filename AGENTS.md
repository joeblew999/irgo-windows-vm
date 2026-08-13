# Working on this repository

For contributors, human and AI. Agents load this file automatically; read it
before writing code, not after.

Most of the duplication this repo has had to clean up was written by an AI agent
that did not check what already existed — three wait-for-agent loops next to an
existing `WaitForAgent` method, a fourth byte formatter, and `EnsureReady` used
where `RunInstall` was needed, which would have left a fresh VM sitting at a
UEFI prompt until timeout. None of it was caught by tests. All of it was caught
by someone reading the code later.

These rules exist to make that failure mode expensive up front instead of
expensive later.

## Before you write a function, look for it

The package is small enough to grep and has already converged on helpers.
Reaching for a new one usually means the existing one was not found:

| you need | it exists |
|---|---|
| wait for the guest agent | `VM.WaitForAgent` / `WaitForAgentEvery` (`control.go`) |
| format a byte count | `HumanBytes` (`external.go`) |
| find or install an external tool | `Tool.resolve`, `Tool.Ensure`, `BrewInstall` (`brew.go`) |
| run something in the guest | `pushScript` + `batchFile` (`run.go`) — never `exec` a quoted command line |
| a path for cache/work/bin/screens | `Paths` (`paths.go`) — never hardcode |
| refuse to clobber media in use | `Paths.CheckWritable` (`paths.go`) |
| download with resume + verification | `Download` (`fetch.go`) |

If you are about to write a second implementation of any of these, that is the
signal to stop and use the first.

## The comments are findings, not decoration

Long comments here record things that cost hours and are not recoverable from
the code:

- why the display is `virtio-ramfb-gl` and not `virtio-gpu-pci`
- why ESD image 3 must be exported with `--boot`
- why `utmctl suspend --save-state` must never be called
- why guest commands go through a pushed batch file rather than `exec`

Move them with their code. Do not compress, summarise or "tidy" them. If a
comment is wrong, fix the fact — do not delete the explanation.

## Idempotency is a contract

Every expensive operation here is one somebody will re-run: a 4.2 GB download, a
45-minute install, a VM that reboots itself mid-session. Re-running must be safe
and cheap.

- Already done is **success**, not an error. Report which it was.
- Refusing for **safety** is different and stays — writing over immutable or
  hardlinked media is refused deliberately (`CheckWritable`).
- `boot` is the exception: re-driving a live Setup sends keystrokes into its UI
  and has destroyed an install. Idempotent there means *detecting it need not
  act*, never repeating the action.

## The binary is the product; mise is for maintainers only

A person using the tool never installs mise. A developer on a project that
*uses* this tool never installs mise. Only someone changing **this** repository
does — to build, test, lint and work on the upstream clones.

So: if a user or developer needs it, it is a **CLI subcommand**. `mise.toml`
holds maintainer tasks and the `IRGO_*` defaults, and nothing else.

This has already gone wrong once. Cross-compiling the Windows probes lived as 13
lines of shell in a mise task, so anyone who followed the README's
`go install …` could not build probes at all — the binary was incomplete and the
task runner hid it. If you find yourself writing shell logic in `mise.toml` that
a user would want, it belongs in the CLI.

## One fact, one declaration

Names, paths, prefixes and timeouts get declared once. The failure mode is not
hypothetical: guest temp files were named by concatenation in `run.go` while
`cleanup.go` kept its own list of those prefixes for `Prune`, so adding a prefix
would have silently stopped it being cleaned up.

Timeouts get a named constant with the reason attached. `45*time.Minute` at a
call site tells the next person nothing about why 45.

## Verify against the VM, not the compiler

Unit tests cover config generation, ISO inspection, the Microsoft catalog, and
prune. **Everything else is only verified by running a real VM**, and those
paths fail silently — a wrong boot command produces a prompt nobody sees, a
truncated ISO produces a VM that will not boot.

If you touch boot, run, media or setup:

```sh
mise run check                                     # never sufficient on its own
irgo-winvm setup                                   # must skip every stage
irgo-winvm run -timeout 3m -vm irgo-win11 .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-win11 .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11   # ~400 ms
```

`RESULTS.md` is the contract. If a number there changes, either you broke
something or you have a new measurement to record — decide which before
committing.

## A glaze or native bug is fixed at crgimenes

Non-negotiable, and the reason this project exists. See the README section of
the same name, and `UPSTREAM.md` for the ledger. A bug worked around in an
example is still shipped to everyone using those libraries, and the workaround
hides it.

## Do not

- Split `utmvm` into sub-packages. Its parts are genuinely coupled and a split
  would force the exported surface to stay large.
- Touch `assets/`, the answer file, or the plist template in `config.go` without
  running a real install. UTM rejects a bad config with one generic *"cannot
  import this VM"* that names no field; `config_test.go` exists because that
  failure is undebuggable.
- Add an exported symbol the CLI does not use. The package has one consumer.
- Land a refactor in one commit. One concern per commit, each verified.
