# Plans

One file per plan, named `YYYY-MM-DD_HHMM_slug.md`. Finished plans move to `done/`.
A plan must stand on its own: symptom, evidence, cause, the exact change, and how to verify it.

## Work order (start at the top)

| # | Plan | Why this order | State |
|---|---|---|---|
| 1 | [`done/2026-09-30_1320_restore-upstream-link-verify.md`](done/2026-09-30_1320_restore-upstream-link-verify.md) | Restores `upstream:clone/link/verify/unlink`. Every upstream fix below is tested through it. Small: `mise.toml` only. | **done** |
| 2 | [`2026-09-30_1250_glaze-webview2-stale-registration.md`](2026-09-30_1250_glaze-webview2-stale-registration.md) | The glaze fix (fallback when the WebView2 registration is stale) → issue + PR to crgimenes/glaze. Needs 1. | planned |
| 3 | [`2026-09-30_1215_gui-probes-blocked-by-password-expiry.md`](2026-09-30_1215_gui-probes-blocked-by-password-expiry.md) | Harden the VM itself: password never expires in `autounattend.xml`, `vm-repair`, fail-fast "ready" + `-gui`, `doctor` rows. Independent of 1–2. | partly done — `irgo-win11` repaired by hand 2026-09-30; code changes open |

## Start here

```sh
mise run upstream:clone      # after plan 1 lands; brings glaze + native clones to trunk
mise run upstream:verify     # their tests + go:check against the local clones
mise run app:create:verify   # the real proof, on the Windows VM (must be logged in: see plan 3)
mise run upstream:unlink
```

The VM (`irgo-win11`) is healthy as of 2026-09-30: `dev` auto-logs in (password never expires) and
WebView2 is registered at 154.0.4258.37. All four GUI/headless probes pass on glaze v0.0.61 /
native v0.1.15.
