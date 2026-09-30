# GUI probes hang: the `dev` password expired, so auto-logon stopped working

Status: planned · 2026-09-30

## Symptom

`mise run app:create:verify` (and `verify-events`, `glaze-all`) pushed the binary, printed
`launching in dev's desktop session`, then only `still running (… of 10m0s)` until the timeout.
The headless `app:create:probe` in the same run passed. `irgo-winvm` had reported the VM **ready**.

## Evidence

- `~/Library/Application Support/irgo-winvm/shots/irgo-win11-20260930-115924-ready.png` — the
  screen at the moment `EnsureReady` declared "ready": the sign-in UI for `dev` showing
  **"Your password has expired and must be changed."** with OK / Cancel.
- `…-20260930-120341-vm-screen.png` (4 minutes into `verify`) — the Windows lock screen. That is
  what is left after the expiry dialog; nobody is logged in.
- The VM was installed 2026-08-15 (`…-20260815-150721-ready.png` is the first ready shot). Windows'
  default maximum password age for local accounts is **42 days**, so `dev`'s password expired on
  ~2026-09-26. The run on 2026-09-30 is the first boot after that.

## Cause

Three things combine:

1. **Nothing sets "password never expires".** `utmvm/assets/autounattend.xml` creates `dev`
   (`LocalAccount`, plaintext `dev`) and enables `AutoLogon` (`LogonCount` 999), but no
   `FirstLogonCommands` entry touches password age. After 42 days auto-logon hits the expiry
   dialog and stops, so there is **no interactive session**.
2. **"Ready" does not mean "someone is logged in".** `EnsureReady` (`utmvm/vm.go`) returns when
   `AgentReady()` answers. The QEMU guest agent runs as `NT AUTHORITY\SYSTEM` in session 0, which
   is up long before, and independently of, a desktop session. So "ready" was reported while the
   screen showed the expiry dialog.
3. **The `-gui` path cannot tell.** `AppCreate` with `GUI` goes through `appExecInteractive`, a
   scheduled task that runs in the logged-in user's session. With no session the task never runs
   the program, no exit-code file is written, and `waitForGuest` polls until `Timeout` (10 min).
   The failure is indistinguishable from a slow program.

Not the cause: the lock screen, power settings (`powercfg … 0` is already set), WebView2, glaze.

## Changes

### 1. New VMs: the password never expires — `utmvm/assets/autounattend.xml`

Add to `FirstLogonCommands`, before the final `unattend-complete` marker, keeping `Order` sequential:

```xml
<SynchronousCommand wcm:action="add">
  <Order>…</Order>
  <Description>Local passwords never expire (auto-logon breaks after 42 days otherwise)</Description>
  <CommandLine>net accounts /maxpwage:unlimited</CommandLine>
</SynchronousCommand>
<SynchronousCommand wcm:action="add">
  <Order>…</Order>
  <Description>dev: password never expires</Description>
  <CommandLine>powershell -NoProfile -Command Set-LocalUser -Name dev -PasswordNeverExpires $true</CommandLine>
</SynchronousCommand>
```

(No quotes in the command lines: they survive `autounattend` as written. `net accounts` covers
every local account; `Set-LocalUser` pins `dev` explicitly in case policy is changed later.)

### 2. Existing VMs: repair in place

A VM built before change 1 must be fixed from outside, because nobody can log in. The guest agent
still works (the headless probe ran), so run a batch through the existing `pushScript` / `appExec`
path (session 0, SYSTEM):

```bat
net user dev dev
net accounts /maxpwage:unlimited
powershell -NoProfile -Command Set-LocalUser -Name dev -PasswordNeverExpires $true
shutdown /r /t 5
```

`net user dev dev` re-sets the password, which clears the expired state; the reboot lets
`AutoLogon` run again. Expose it as a verb, e.g. `irgo-winvm vm-repair` (+ `mise run vm:repair`),
idempotent and safe to run on a healthy VM.

### 3. Fail fast instead of hanging — "ready" must include the desktop session

- **`EnsureReady`**: after `AgentReady()`, wait (bounded, e.g. 90 s) for an interactive session
  for the configured user, checked headlessly via `appExec` with
  `tasklist /v /fi "imagename eq explorer.exe"` (lists the owning user; present on every edition,
  unlike `quser`). Take a `desktop` shot either way. If it never appears: return an error that
  says so and names the newest shot, instead of "ready".
- **`AppCreate` with `GUI`**: run the same check before `appExecInteractive`; if there is no
  session for `o.User`, error immediately ("no desktop session for dev — see <shot>; try
  vm-repair") rather than entering `waitForGuest`.
- `doctor`: add a row "desktop session: dev logged in / not logged in".

### 4. Docs

- `AGENTS.md`, auto-logon paragraph: state that `dev`'s password is set never to expire and why
  (42-day default silently ends auto-logon).
- `RESULTS.md`: record this failure mode and the glaze v0.0.61 / native v0.1.15 GUI results once
  re-run.

## Verification

1. Repair the current VM (change 2), then `mise run vm:screen` → the desktop, not a sign-in screen.
2. `mise run app:create:verify`, `app:create:verify-events`, `app:create:glaze-all` pass against
   glaze v0.0.61 (bumped in 5a73e31; only the headless probe was re-run so far).
3. Fail-fast check: on the VM run `net user dev /logonpasswordchg:yes`, reboot → `EnsureReady` /
   `app:create:verify` must error within ~90 s naming the missing session (not hang 10 min).
   Then `vm:repair` again.
4. Fresh VM (change 1) — only when a VM is next rebuilt: after install,
   `net user dev` shows `Password expires  Never`.

## Open questions

- Does `Set-LocalUser` exist on the Windows 11 ARM build used here? (`net user dev /expires:never`
  sets *account* expiry, not password expiry — not a substitute.) Fallback: `wmic useraccount where
  name='dev' set PasswordExpires=false`, if `wmic` is still present on this build.
- `explorer.exe` as the session signal: fine for the stock shell; revisit if the shell is ever replaced.
