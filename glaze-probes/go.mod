// Separate module: these are reference programs that build against glaze, and
// they should not drag that dependency into the VM tooling.
module github.com/joeblew999/irgo-windows-vm/glaze-probes

go 1.26.5

require github.com/crgimenes/glaze v0.0.47

require github.com/ebitengine/purego v0.10.2 // indirect
