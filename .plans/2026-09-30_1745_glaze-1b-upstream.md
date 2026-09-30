# glaze §1b: register custom schemes on Windows (upstream issue + fix, ready to send)

Status: PATCHED LOCALLY, not verified on Windows, not filed · 2026-09-30 · item B of
[`2026-09-30_1730_fix-it-all.md`](2026-09-30_1730_fix-it-all.md)

The bug is [docs/UPSTREAM.md §1b](../docs/UPSTREAM.md#1b-glaze--absolute-app-urls-silently-do-not-load-on-windows).
The fix belongs at crgimenes, not here (docs/DEVELOPMENT.md, "A glaze or native bug is fixed at
crgimenes"). Nothing below has been pushed, filed or proposed: **that needs the owner's go-ahead.**

## Upstream state (checked 2026-09-30)

- **Not known upstream.** `gh issue list/pr list -R crgimenes/glaze --state all` and
  `gh search issues` for "scheme", "app.localhost" and "custom scheme windows": nothing but #27/#28,
  the PR that *added* scheme handlers (merged 2026-07-13). In its review the maintainer asked for
  "one canonical URL on every platform" (point 5). The contributor kept the vhost and rebuilt the
  handler URL, so what the handler sees matches. What the page sees (its origin, and whether
  absolute URLs resolve) was never raised.
- **Not fixed.** `origin/trunk` = `0dce849` = tag `v0.0.61`, still `schemeVHost` +
  `rewriteSchemeURL`. No release fixes it, so this is not a version bump.

## The fix (branch `fix/windows-custom-scheme`, commit `75f3ea1`, on `origin/trunk`)

In the glaze clone (`$UPSTREAM_DIR/glaze`), checked out as a worktree at
`/private/tmp/claude-501/upstream-wt/glaze-windows-custom-scheme`, so the clone's own checkout
(`fix/webview2-stale-registration`) was not touched. The commit is in the clone's refs, so it
survives if `/private/tmp` is cleared (then `git worktree prune`).

- WebView2 reads custom schemes from the options object passed when the environment is created.
  glaze passed `null` (`webview2_windows.go:667` on v0.0.61). The fix passes an options object.
- The SDK's options class is C++ in a header (`WebView2EnvironmentOptions.h`), so glaze implements
  the three interfaces the runtime reads as **read-only COM objects over Go vtables**
  (`purego.NewCallback`). This is the same way it already implements its completion handlers:
  cgo-free, no bundled DLL, no new dependency. The interfaces are
  `ICoreWebView2EnvironmentOptions`, `ICoreWebView2EnvironmentOptions4` and
  `ICoreWebView2CustomSchemeRegistration`. IIDs and vtable order come from `WebView2.h` in NuGet
  `Microsoft.Web.WebView2` 1.0.1587.40, the first stable SDK with the API.
- Each scheme is registered with `TreatAsSecure = TRUE`, `HasAuthorityComponent = TRUE` (so
  `app://home` is a tuple origin, like `http://home`), and `AllowedOrigins = ["<scheme>://*"]`
  (same-scheme requests that carry an `Origin` header, such as a same-origin POST, are allowed;
  https pages are not). `TargetCompatibleBrowserVersion` is reported as `110.0.1587.40`, as the SDK
  object does.
- Requests are intercepted with filter `app:*`, not `app*` (which would catch `apple://`). The
  handler gets the URI unchanged. `schemeVHost`, `rewriteSchemeURL`, `canonicalSchemeURL` and
  `schemeAuthority` are deleted.
- **One behaviour change, deliberate.** WebView2 refuses to create environments with different
  registrations on one browser process, and a shared user data folder means a shared browser
  process. So a window with schemes uses `%APPDATA%\<exe>-schemes-<names>`, and a window without
  schemes keeps `%APPDATA%\<exe>` unchanged. Without this, glaze's own Windows `TestMain`
  (windows without schemes, then `noBridgeScenario` with scheme `probe`) would fail to create the
  environment. The storage this splits is per origin anyway. The alternative (one process-wide
  registration set, error on mismatch) is named in the PR text for the maintainer to choose.
- Runtime older than 110.0.1587.40 with schemes requested: a clear error, not a silent non-load.
  Environment/controller creation failures now include the HRESULT.
- Tests: `TestSchemeAbsoluteURLs` (a Windows GUI scenario in `TestMain`: a page whose only script
  is an **absolute** `app://home/app.js` reports `location.origin` and `isSecureContext` through
  another absolute URL; expects `app://home,true app://home/app.js`). `TestEnvOptionsRegistrations`
  (headless: calls the options object through its vtables, as the runtime does). Also
  `TestUserDataFolder` and `TestRuntimeVersion`.
- README "Custom URL schemes": the Windows paragraph now describes real registration.

### Checks run (Mac, 2026-09-30)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, windows/amd64, windows/arm64 | all build |
| `go vet ./...` (darwin) and `GOOS=windows GOARCH={amd64,arm64} go vet ./...` | clean |
| `GOOS=windows GOARCH={amd64,arm64} go test -c` (the Windows tests compile) | ok |
| `go test -count=1 -timeout 180s ./...` on macOS (their GUI scenarios included) | ok (glaze, editor, menu) |
| `GOOS={darwin,linux,windows} golangci-lint run ./...` (v2.14.0; their CI pins 2.13.1) | 0 issues each |
| `examples/` module: `go build`, `GOOS=windows go build`, `go vet` | ok |
| gosec (`golangci-lint --no-config -E gosec --new-from-rev origin/trunk`, windows) | new non-test code clean (audited `#nosec` with reasons); 3 G103 in the new test's `unsafe` COM calls, like the existing darwin tests; their CI does not run gosec |
| **Runs on Windows** | **NOT RUN.** The VM belongs to item A. Nothing above executes the COM path. |

## (a) Verify on Windows: the VM owner runs this

The clone must have the branch checked out, because `upstream:link` links `$UPSTREAM_DIR/glaze`
itself. A branch cannot be checked out in two worktrees, so drop the worktree first:

```sh
G=$UPSTREAM_DIR/glaze
git -C $G status --short                      # must be empty
git -C $G worktree remove /private/tmp/claude-501/upstream-wt/glaze-windows-custom-scheme
git -C $G switch fix/windows-custom-scheme

mise run upstream:link && mise run glaze:mac && mise run glaze:windows
#   expect: verify PASSes on windows/arm64 ("PASS: page, absolute app:// sub-resources, JS -> Go
#   round trip", origin app://home). On released v0.0.61 it FAILs with
#   "absolute app:// sub-resources never loaded (glaze bug, docs/UPSTREAM.md §1b)".
mise run upstream:test:windows
#   glaze's own suite on the VM; expect TestSchemeAbsoluteURLs, TestEnvOptionsRegistrations,
#   TestNoBridge (scheme "probe") and the rest to pass.
mise run upstream:unlink
git -C $G switch fix/webview2-stale-registration   # where the clone was
```

If it passes, record it in `docs/RESULTS.md` (dated) and in the §1b status in `docs/UPSTREAM.md`.
If `verify` fails, the probe prints which case it hit. Run with `WEBVIEW2_DEBUG=1` for the
HRESULT and runtime version, and fix it on the branch before anything is sent.

## (b) Send it: only when the owner says go

The precedent (glaze#34) and glaze's CONTRIBUTING ("anything larger: open an issue first") both
say **issue first**. The maintainer fixes his own bugs. Offer the branch; open the PR only if he
asks, or if the owner decides otherwise.

```sh
cd ~/workspace/go/src/github.com/joeblew999/irgo-windows-vm
P=.plans/2026-09-30_1745_glaze-1b-upstream.md
section() { awk -v s="<!-- $1 -->" -v e="<!-- /$1 -->" '$0==e{f=0} f{print} $0==s{f=1}' "$P"; }

# 1. the issue
gh issue create -R crgimenes/glaze \
  --title "Windows: absolute app:// URLs never load; custom schemes are emulated over https://<scheme>.localhost" \
  --body-file <(section issue)

# 2. only if the maintainer wants the patch: push to the fork, open the PR (fill in the issue number)
git -C $UPSTREAM_DIR/glaze push fork fix/windows-custom-scheme
gh pr create -R crgimenes/glaze --base trunk --head joeblew999:fix/windows-custom-scheme \
  --title "webview2: register custom schemes instead of emulating them" \
  --body-file <(section pr)
```

Then set §1b's status in `docs/UPSTREAM.md` to `FILED` with the link.

### Issue text

<!-- issue -->
**Symptom.** On Windows, a page served through `Options.SchemeHandlers` loads, but every
sub-resource it names by an **absolute** scheme URL (`<script src="app://home/app.js">`,
`<link href="app://home/app.css">`) is never requested. There is no error and no console message:
the page just has no CSS and no JS. The same program works on macOS and Linux. Relative URLs work
everywhere.

**Minimal reproduction** (v0.0.61, Windows 11 ARM64, Edge WebView2 Runtime 154):

```go
package main

import (
	"fmt"
	"strings"

	"github.com/crgimenes/glaze"
)

const page = `<!doctype html><script src="app://home/app.js"></script><h1 id=h>app.js did not load</h1>`
const js = `document.getElementById('h').textContent = 'app.js loaded, origin ' + location.origin`

func main() {
	w, err := glaze.NewWithOptions(glaze.Options{SchemeHandlers: map[string]glaze.SchemeHandler{
		"app": func(r *glaze.SchemeRequest) *glaze.SchemeResponse {
			fmt.Println("served", r.URL)
			if strings.HasSuffix(r.URL, "app.js") {
				return &glaze.SchemeResponse{Body: []byte(js), MIMEType: "text/javascript"}
			}
			return &glaze.SchemeResponse{Body: []byte(page), MIMEType: "text/html"}
		},
	}})
	if err != nil {
		panic(err)
	}
	defer w.Destroy()
	w.Navigate("app://home/index.html")
	w.Run()
}
```

On macOS (v0.0.61) it prints `served app://home/index.html` then `served app://home/app.js`. On
Windows the second line never comes. The Windows measurement is from a probe built on the same
pattern, which loads each asset twice, once by an absolute URL and once by a relative one:

```
scheme handler served: app://home/index.html -> text/html
scheme handler served: app://home/rel.css    -> text/css          (relative: arrives)
scheme handler served: app://home/rel.js     -> text/javascript   (relative: arrives)
                                                                  (app://home/app.css: never requested)
                                                                  (app://home/app.js:  never requested)
origin:        https://app.localhost
```

**Root cause.** `webview2_scheme_windows.go` does not register the scheme. It serves it over a
virtual host, `schemeVHost` → `https://<scheme>.localhost` (line 23), and `Navigate` rewrites
`app://home/index.html` to `https://app.localhost/index.html` (`rewriteSchemeURL`, line 204;
called from `webview2_windows.go:807`). The document's real origin is therefore
`https://app.localhost`. An absolute `app://home/app.js` inside it names a scheme WebView2 has
never been told about, so the request is never made and the `WebResourceRequested` filter never
sees it. Two more effects of the same rewrite:

- `location.origin` is `app://home` on macOS and Linux but `https://app.localhost` on Windows, so
  origin-dependent code (storage keys, CSP, postMessage checks) diverges.
- The host is dropped: `app://a/x` and `app://b/x` both become `https://app.localhost/x`.

**Proposed fix.** Register the scheme for real, as the macOS and Linux backends do. WebView2 has
done this since SDK 1.0.1587.40 / runtime 110:
`ICoreWebView2EnvironmentOptions4::GetCustomSchemeRegistrations`, with
`ICoreWebView2CustomSchemeRegistration` set to `TreatAsSecure`, `HasAuthorityComponent` and
`AllowedOrigins`. glaze currently passes `null` options to
`CreateWebViewEnvironmentWithOptionsInternal` (`webview2_windows.go:667`). Passing a small options
object implemented over Go vtables keeps it cgo-free with no bundled DLL, the way glaze already
implements its completion handlers. The vhost rewrite then goes away, and the handler URL, the
page origin and absolute URLs are the same on all three platforms.

I have this implemented, with a Windows GUI test for the absolute-URL case, on
`joeblew999/glaze` branch `fix/windows-custom-scheme`, and am happy to open a PR if you would like
it. Until then, the workaround for users is to reference assets relatively.
<!-- /issue -->

### PR description

<!-- pr -->
Fixes #<issue>.

On Windows, `SchemeHandlers` were served over `https://<scheme>.localhost` and `Navigate` rewrote
`app://…` to it. So the page's origin was `https://app.localhost`, absolute `app://` sub-resources
were never requested (silently), `location.origin` differed from macOS and Linux, and the URL's
host was dropped.

This registers each scheme with the WebView2 environment instead, as the other backends do:

- **Options object over Go vtables.** `ICoreWebView2EnvironmentOptions` + `…Options4` +
  `ICoreWebView2CustomSchemeRegistration`, read-only, process-lifetime (cached per scheme set).
  They are built with `purego.NewCallback` like the existing completion handlers: no cgo, no
  bundled DLL, no new dependency. IIDs and vtable order are from `WebView2.h` (SDK 1.0.1587.40).
  The pointer goes to `CreateWebViewEnvironmentWithOptionsInternal` where `null` went.
- **Registration:** `TreatAsSecure`, `HasAuthorityComponent` (so `app://home` is the origin), and
  `AllowedOrigins = ["<scheme>://*"]` (a same-origin POST carries an `Origin` header and needs
  it; https pages stay out). `TargetCompatibleBrowserVersion` = `110.0.1587.40`, as the SDK's own
  object reports.
- **Serving:** filter `<scheme>:*`, and the request URI goes to the handler unchanged.
  `schemeVHost`, `rewriteSchemeURL`, `canonicalSchemeURL` and `schemeAuthority` are removed.
- **User data folder.** WebView2 fails environment creation when environments on one browser
  process (one user data folder) have different registrations. A window with schemes therefore
  uses `%APPDATA%\<exe>-schemes-<names>`, and one without keeps `%APPDATA%\<exe>`. Without this,
  the existing Windows `TestMain` (plain windows, then `noBridgeScenario` with `probe`) cannot
  create the environment. If you prefer one process-wide registration set with an error on
  mismatch, that is a small change; I went with the one that cannot fail.
- **Errors:** a runtime older than 110.0.1587.40 with schemes requested gets a clear error instead
  of a scheme that silently does not load. Environment/controller creation failures now include
  the HRESULT.

Tests:

- `TestSchemeAbsoluteURLs` (Windows GUI, in `TestMain`): a page whose only script is an absolute
  `app://home/app.js` reports `location.origin` + `isSecureContext` through another absolute URL.
  Expects `app://home,true`, and the handler seeing `app://home/app.js`.
- `TestEnvOptionsRegistrations` (headless): calls the options object through its vtables as the
  runtime does.
- `TestUserDataFolder`, `TestRuntimeVersion`.

Checked: `CGO_ENABLED=0 go build` for all six targets, `go vet` (darwin, windows amd64/arm64),
`golangci-lint` clean for darwin/linux/windows, `go test ./...` on macOS, and the examples module.
Run on Windows 11 ARM64: <fill in after (a)>.
<!-- /pr -->

## When it lands upstream

Bump glaze in `examples/go.mod`, run `mise run glaze:windows` against the release (verify must
PASS with no link), set §1b to `FIXED UPSTREAM` with the version, and move this plan to `done/`.
