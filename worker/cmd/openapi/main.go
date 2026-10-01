// Command openapi writes wire's OpenAPI document to the file it is given, the
// one the Worker embeds and serves at /api/openapi.json. `mise run
// worker:wasm` runs it before every build, so a deploy carries the table it
// was built from; TestOpenAPIIsCurrent fails while the committed file is
// stale.
package main

import (
	"fmt"
	"os"

	"github.com/joeblew999/irgo-windows-vm/wire/openapi"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: openapi <file>")
		os.Exit(2)
	}
	b, err := openapi.Document()
	if err == nil {
		err = os.WriteFile(os.Args[1], b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s: %d bytes\n", os.Args[1], len(b))
}
