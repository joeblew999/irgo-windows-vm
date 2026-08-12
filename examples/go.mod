// Runnable examples. Separate module so glaze and native stay out of the VM
// tooling's dependency graph.
module github.com/joeblew999/irgo-windows-vm/examples

go 1.26.5

require (
	github.com/crgimenes/glaze v0.0.47
	github.com/crgimenes/native v0.1.7
)

require github.com/ebitengine/purego v0.10.2 // indirect
