// Separate module: the site generator needs a markdown parser, and that
// dependency has no business in the graph of the binary users download.
// Same reason probe/, glaze-probes/ and examples/ are separate.
module github.com/joeblew999/irgo-windows-vm/site

go 1.26.5

require github.com/yuin/goldmark v1.8.5
