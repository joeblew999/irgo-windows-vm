// Runnable examples. Separate module so glaze and native stay out of the VM
// tooling's dependency graph.
module github.com/joeblew999/irgo-windows-vm/examples

go 1.27.1

require (
	github.com/crgimenes/glaze v0.0.61
	github.com/crgimenes/native v0.1.15
)

require github.com/ebitengine/purego v0.11.0 // indirect
