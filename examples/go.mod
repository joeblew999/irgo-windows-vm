// The four programs this repo runs on Windows (and on the Mac) to find out what
// breaks in glaze and native: probe, verify, verify-events, glaze-all. Each
// exits non-zero when anything it checks failed. Separate module so glaze and
// native stay out of the VM tooling's dependency graph.
module github.com/joeblew999/irgo-windows-vm/examples

go 1.27.1

require (
	github.com/crgimenes/glaze v0.0.61
	github.com/crgimenes/native v0.1.15
)

require github.com/ebitengine/purego v0.11.1 // indirect
