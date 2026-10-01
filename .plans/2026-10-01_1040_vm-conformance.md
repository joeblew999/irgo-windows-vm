# VM conformance: the glaze suite's machinery, pointed at the VM

Status: in progress (agent BB) · 2026-10-01

## Symptom
Everything we fixed on the VM today (Windows Update, Windows key, BitLocker, SMB share, WebView2,
password expiry, desktop pop-ups) is asserted nowhere; a VM or golden image can drift silently.

## Change
Generalise internal/glazecheck into a conformance runner; a VM suite (go test, run in the guest)
asserting each property; `vm-check` / `vm-status` (CLI + MCP) writing docs/VM-STATUS.md with the
same verdicts; `vm-golden-create` refuses to publish an image that fails; site page.

## Verify
Live on irgo-win11 (read-only) and a disposable clone; glaze suite unchanged.
