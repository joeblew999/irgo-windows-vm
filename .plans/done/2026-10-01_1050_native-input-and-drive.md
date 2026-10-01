# Native input/screen + Playwright-for-native driver

Status: done (P, T, U, W) · 2026-10-01; upstreaming pending

## What exists
- Owner's native fork: input/ + screen/ — PR #1 (macOS, background CGEventPostToPid +
  ScreenCaptureKit) and PR #2 (Windows, PostMessage to WebView2 + PrintWindow); both green, mergeable.
- examples/drive: launch a glaze app as a separate process, locate by DOM, act with real OS input,
  wait/expect events, screenshots. Mac and Windows CI green (W: 3 runs in a row).

## Next
1. Owner merges fork PR #1 then #2; later propose upstream to crgimenes/native (owner's call).
2. Linux backend (separate PR) if remote/cross-platform work needs it.
3. Modifier keys on Windows (Shift arrives as nothing) — measure SendInput inside the VM.

## Verify
`mise run glaze:mac`; conformance CI on windows-11-arm.

## Closed — 1 Oct 2026

Done: fork PRs #1 (macOS) and #2 (Windows) green; examples/drive with real OS input, reliable on Windows CI. Owner merges the fork PRs → follow-ups.
