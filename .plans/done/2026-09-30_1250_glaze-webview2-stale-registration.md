# glaze: survive a stale WebView2 registration (upstream PR to crgimenes/glaze)

Status: CLOSED (reported upstream) ([glaze#34](https://github.com/crgimenes/glaze/issues/34)) · 2026-09-30 · fix lives upstream — "a glaze or native bug is fixed at crgimenes, not here"

## Problem

On `irgo-win11` every glaze window failed with
`webview2: Edge WebView2 Runtime not found (install it)` while a working runtime was installed.
The Evergreen runtime had self-updated to `154.0.4258.37` (files complete, including
`EBWebView\arm64\EmbeddedBrowserWebView.dll`) and deleted the `151.0.4129.86` folder, but the
registration was left behind:

```
HKLM\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\ClientState\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}
    EBWebView = C:\Program Files (x86)\Microsoft\EdgeWebView\Application\151.0.4129.86   ← folder gone
HKLM\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{…same GUID…}
    pv        = 151.0.4129.86
```

Same failure on glaze v0.0.47 and v0.0.61 (A/B on the same VM), so it is long-standing, not a
regression. Likely trigger: an update interrupted by a shutdown (inferred). End users can hit it
after a power cut or forced shutdown mid-update; the app then refuses to start although nothing
needs installing.

## Where it breaks

`webview2_windows.go`, `findEmbeddedBrowserDLL` (v0.0.61, ~line 568): for HKLM then HKCU it reads
`EBWebView` under `edgeClientStateKey`, takes `filepath.Base` as the version, and `os.Stat`s
`<EBWebView>\EBWebView\<arch>\EmbeddedBrowserWebView.dll`. The registry value is the **only**
source; if its folder is missing the loop ends and it returns "not found (install it)". The comment
says it reimplements `loader.hh` (webview/webview's loader), so apps on that loader likely share
the behaviour — unverified.

## Change (minimal, cgo-free, one function)

After the registry loop finds nothing, before returning the error:

1. For each install root — `%ProgramFiles(x86)%\Microsoft\EdgeWebView\Application` (system) and
   `%LOCALAPPDATA%\Microsoft\EdgeWebView\Application` (per-user) — list subdirectories.
2. Keep names that parse as versions, pass `versionBuildAtLeast(v, minAPIVersion)`, and contain
   `EBWebView\<arch()>\EmbeddedBrowserWebView.dll`.
3. Return the highest such version's DLL.
4. If still nothing: an error that distinguishes the cases, e.g.
   `webview2: registered runtime folder %s is missing and no runtime was found beside it (reinstall the Evergreen runtime)`
   vs the existing "not installed" message when there is no registration at all.

No new API, no options, no knobs (their YAGNI rule): the registry stays authoritative; the scan only
runs when the registered folder is missing. stdlib only (`os.ReadDir`, `os.Getenv`, `filepath`).

## Reproduce and verify (on irgo-win11, via the agent as SYSTEM)

Use the batch-file route (`utmctl file push` + `utmctl exec`), as in the password plan.

1. **Break it deliberately:** `reg add "HKLM\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\ClientState\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" /v EBWebView /t REG_SZ /d "C:\Program Files (x86)\Microsoft\EdgeWebView\Application\1.0.0.0" /f`
2. `mise run app:create:verify` with released glaze → FAIL "not found (install it)" (reproduces).
3. `mise run upstream:link`, then `mise run app:create:verify` against the patched local glaze →
   PASS; the DLL comes from `154.0.4258.37`. `mise run upstream:unlink` when done.
4. **Restore:** run `…\Application\154.0.4258.37\Installer\setup.exe --msedgewebview --system-level`
   (exit 0 re-registers). Re-run `verify` with the patch → PASS (registry path, unchanged behaviour).
5. Glaze's own checks: `CGO_ENABLED=0 go build` for every GOOS/GOARCH they list, `go vet`, their tests.

## Where we work (this repo's own upstream workflow)

- **Home base: this repo** — plans, `UPSTREAM.md` (the ledger: add this finding as `FOUND HERE`,
  then `PATCHED LOCALLY`, then `reported` with the issue/PR links), probes, VM tasks.
- **Code: the local clone** `$UPSTREAM_DIR/glaze` = `~/workspace/go/src/github.com/crgimenes/glaze`
  (and `…/native`). The fork `joeblew999/glaze` is only a remote there, for the PR branch.
- **Link: `mise run upstream:link` / `upstream:verify` / `upstream:unlink`** write and remove a
  `go.work` so our probes build against the local clone (`go.work` is already gitignored).
  **Step 0: restore these tasks** — they were deleted with the old `mise.toml` in `e533764`
  (2026-08-13); recover them from `git show e533764^:mise.toml` (`UPSTREAM_DIR`, `upstream:link`,
  `upstream:verify`, `upstream:unlink`) and point `upstream:verify` at today's tasks
  (`app:create:verify` etc.) instead of the removed `vm:run`.

## Setup (glaze side — verified 2026-09-30)

- Local clone: `~/workspace/go/src/github.com/crgimenes/glaze`. Upstream's default branch is now
  **`trunk`**; the clone was left on the old `master` (last commit 2026-08-03). First:
  `git fetch && git switch trunk && git pull` (`git remote set-head origin -a` already done).
- No fork exists yet: `gh repo fork crgimenes/glaze --remote --remote-name fork` → `joeblew999/glaze`.
- Branch from `trunk`: `git switch -c fix/webview2-stale-registration`.
- glaze has no mise (a `Makefile`); do not add one — their repo, their conventions. `trunk`'s go.mod
  says `go 1.27.1`, which the global mise Go (1.27.1) satisfies.
- Their gates (CONTRIBUTING + Makefile): `make all` (build, vet, test), `make lint`, `CGO_ENABLED=0`
  cross-build for every GOOS/GOARCH, no new dependency (purego is the only one), one runnable test.

## Upstream steps

1. Open an issue on crgimenes/glaze: symptom, the two registry values, the A/B (v0.0.47 and v0.0.61),
   the one-command reproduction (step 1). Short — the maintainer declined broad collaboration in #30
   but merges focused fixes (#24, #28).
2. PR from a fork branch `fix/webview2-stale-registration`, linking the issue: the one-function
   change + a unit test for the directory-scan helper (temp dir with version folders; picks newest
   valid, skips below `minAPIVersion`, skips folders without the DLL).
3. After it lands: bump glaze in `glaze-probes` / `examples`, re-run the GUI probes, move this plan to
   `done/`.

## Open question

Does Microsoft's own `WebView2Loader.dll` fall back the same way? If yes, the PR text can say
"match the official loader"; if no, it is simply "don't refuse a runtime that is present". Optional
check: load `WebView2Loader.dll` (NuGet SDK) with purego in a scratch probe on the broken registry
state and call `GetAvailableCoreWebView2BrowserVersionString`.

## Related

`.plans/2026-09-30_1215_gui-probes-blocked-by-password-expiry.md` (how this surfaced; `vm-repair`
and `doctor` follow-ups for WebView2 registration live there).

## Outcome (2026-09-30)

- **Reported, not fixed by us:** [crgimenes/glaze#34](https://github.com/crgimenes/glaze/issues/34) —
  evidence, the one-command reproduction, and an offer to re-test. The maintainer fixes his own bugs;
  we do not send a PR unless asked.
- **Diagnosis confirmed** with a local patch (branch `fix/webview2-stale-registration` in the clone,
  also on the fork `joeblew999/glaze`): on the VM, with `EBWebView` pointed at the deleted `151…`
  folder, released v0.0.61 `verify` FAILS ("not found"); linked to the patch it PASSES (finds
  `154.0.4258.37` beside it). glaze's full Windows test suite passes with the patch
  (`upstream:test:windows`), lint clean darwin + windows (`upstream:lint`).
- **Repeatable now:** `upstream:lint` and `upstream:test:windows` were added so this testing is a
  task, not a one-off script.

**When glaze releases a fix:** bump glaze in `glaze-probes` / `examples`, break the registration as
in *Reproduce and verify* step 1, run `mise run app:create:verify` (must PASS on the release),
restore with `setup.exe`, run `mise run upstream:test:windows`, then move this plan to `done/`.

## Closed (2026-09-30)

Our side is done: reported as glaze#34 with evidence and a reproduction; `vm-repair` works around it on our VM. Re-check when glaze releases a fix (steps above), via `upstream:test:windows` and the probes.
