# VM conformance: the glaze suite's machinery, pointed at the VM

Status: in progress (agent BB) · 2026-10-01

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
