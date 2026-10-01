# VM conformance: the glaze suite's machinery, pointed at the VM

Status: done on the branch, not merged (agent BB) · 2026-10-01 — irgo-win11 checked; the clone run is blocked (below)

## Symptom
Everything we fixed on the VM today (Windows Update, Windows key, BitLocker, SMB share, WebView2,
password expiry, desktop pop-ups) is asserted nowhere; a VM or golden image can drift silently.

## Change
Generalise internal/glazecheck into a conformance runner; a VM suite (go test, run in the guest)
asserting each property; `vm-check` / `vm-status` (CLI + MCP) writing docs/VM-STATUS.md with the
same verdicts; `vm-golden-create` refuses to publish an image that fails; site page.

**Screenshots, like the glaze suite (owner requirement, 1 Oct):** every test with something visible
gets its own picture at the meaningful moment (the desktop session, the clean desktop, a WebView2
window rendering, the Windows Update settings page, device-encryption status, the SMB share, …),
captured in-guest with PrintWindow (reusing examples/conformance's capture code) or host-side
vm-screen; blank frames are "not captured (reason)", never published; every window opened is closed.
Checks with nothing visual record the value read as evidence. Pictures go to
docs/screens/vm-conformance/<vm>/ and a Screenshots table in docs/VM-STATUS.md (a column per VM),
published on the site — so other devs can see what happened.

## Verify
Live on irgo-win11 (read-only) and a disposable clone; glaze suite unchanged.

## Done
- `glazecheck.Suite`: one runner, two suites (`Glaze`, `VM`); glaze's record is byte for byte
  what it was (round-tripped against the committed GLAZE-STATUS.md). Runs in parts, host-side
  results, `fact:` and `evidence:` lines. `examples/shots` shared by both suites.
- `examples/vmconformance`, `vm-check`, `vm-status` (CLI + MCP), docs/VM-STATUS.md with 12
  pictures per VM, site page "VM status", `vm-golden-create -check`, `vm-repair -check`.
- The Windows Update settings page was not opened for a picture: a check from that page counts
  as user-initiated and can download and install, which would change irgo-win11. The policy
  values are shown as `reg query` prints them instead.
- irgo-win11 (1 Oct, read-only): NO — PreventDeviceEncryption unset and C: fully BitLocker
  encrypted; hibernation on (3.4 GB hiberfil.sys). Everything else passes. Not fixed here:
  the main session decides (vm-golden-seal's decrypt and hibernate steps are what would fix it).

## Open
- The disposable-clone run: Z's capacity guard refuses vc1 while irgo-win11 runs on this
  16 GiB Mac (8 + 8 leaves 0 of the 4 GiB macOS reserve), and -overcommit was not allowed.
  Run `vm-create -vm vc1`, `vm-check -vm vc1`, `vm-delete -vm vc1 -force` with irgo-win11 stopped.

## Closed — 1 Oct 2026

Done: VM suite (44 tests) with per-test screenshots, vm-check/vm-status, golden-create gate, VM status page. irgo-win11 re-check after decryption → follow-ups.
