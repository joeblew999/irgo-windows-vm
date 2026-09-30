// Separate module: the site generator needs a markdown parser, and that
// dependency has no business in the graph of the binary users download.
// Same reason examples/ is separate.
//
// Everything required here is goldmark or renders for it: its extensions for
// anchors, contents and GitHub alerts, and chroma for code highlighting. See
// render.go for why each one is used. All pure Go; nothing needs cgo.
module github.com/joeblew999/irgo-windows-vm/site

go 1.27.1

require (
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/thiagokokada/goldmark-gh-alerts v0.0.0-20250302164040-cf407c0ddfaf
	github.com/yuin/goldmark v1.8.6
	github.com/yuin/goldmark-highlighting/v2 v2.0.0-20230729083705-37449abec8cc
	go.abhg.dev/goldmark/anchor v0.2.0
	go.abhg.dev/goldmark/toc v0.12.0
)

require github.com/dlclark/regexp2/v2 v2.2.1 // indirect
