// docsite renders a project's docs folder into a static site. Its own module
// so the markdown stack stays out of the graph of whatever project uses it,
// and so it can move to its own repository: nothing here imports the
// repository it currently lives in.
module github.com/joeblew999/irgo-windows-vm/docsite

go 1.27.1

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/thiagokokada/goldmark-gh-alerts v0.0.0-20250302164040-cf407c0ddfaf
	github.com/yuin/goldmark v1.8.6
	go.abhg.dev/goldmark/anchor v0.2.0
	go.abhg.dev/goldmark/toc v0.12.0
)

require github.com/dlclark/regexp2/v2 v2.2.1 // indirect
