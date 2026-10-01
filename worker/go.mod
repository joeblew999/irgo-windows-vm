// Separate module: the Cloudflare Worker that serves the site, the live glaze
// status and the golden image's signed links. workers-go and the Wasm build have
// no business in the graph of the binary users download, the same reason site/
// and examples/ are separate.
module github.com/joeblew999/irgo-windows-vm/worker

go 1.27.1

require (
	github.com/joeblew999/irgo-windows-vm v0.0.0
	github.com/syumai/workers-go v0.36.0
)

tool github.com/syumai/workers-go/cmd/workers-assets-gen

replace github.com/joeblew999/irgo-windows-vm => ../
