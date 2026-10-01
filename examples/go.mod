// What this repo runs on Windows and on the Mac to find out what breaks in
// glaze and native: conformance, the test suite glaze-check runs, and
// glaze-all, the demo you drive by hand. Separate module so glaze and native
// stay out of the VM tooling's dependency graph.
module github.com/joeblew999/irgo-windows-vm/examples

go 1.27.1

require (
	github.com/crgimenes/glaze v0.0.61
	github.com/crgimenes/native v0.1.15
	github.com/ebitengine/purego v0.11.1
)

// native's input and screen packages (background OS input and window capture,
// which examples/drive is built on) are not in a crgimenes release yet: they
// are on the owner's fork: PR #1 (macOS, feat/input-screen-darwin) and PR #2
// (Windows, feat/input-screen-windows, which builds on #1). This takes native
// at PR #2's commit: trunk v0.1.15 plus those two packages and nothing else.
//
// When the packages are in a crgimenes/native release: delete this replace and
// require that release. When the fork's Windows backend
// (feat/input-screen-windows) lands, move the pseudo-version to it.
//
// A go.work replace overrides this one, so `mise run upstream:link` still
// builds against the local clone — which then needs input/ and screen/ too
// (upstream:link says so when they are missing).
replace github.com/crgimenes/native => github.com/joeblew999/native v0.1.16-0.20261001015633-2fbbf2d09e65
