package main

// The MCP page, captured from the binary.
//
// Same rule as the command reference: no tool name, description or annotation
// is transcribed. The generator builds the CLI, runs `irgo-winvm mcp -list`, and
// renders what a connected client would actually be told.
//
// Captured rather than imported. Importing mcpserver would give the same data
// with less machinery — and would drag the protocol SDK and its eight
// dependencies into a module whose go.mod requires exactly one thing, a
// markdown parser. The boundary is worth more than the machinery it saves.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// mcpTool is the part of a tool listing this page renders.
//
// Deliberately not the SDK's mcp.Tool: that would be the import this file
// exists to avoid. The fields are read out of the JSON the binary printed.
type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Annotations struct {
		ReadOnlyHint    bool `json:"readOnlyHint"`
		DestructiveHint bool `json:"destructiveHint"`
	} `json:"annotations"`
	InputSchema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	} `json:"inputSchema"`
}

// generateMCP builds the markdown for the MCP page.
//
// An empty listing is fatal. The reference generator learned this the hard way:
// dropping stderr published a page claiming seven flag-bearing commands had no
// flags, and it built fine. A page that says "no tools" is worse than no page,
// because it reads as an answer.
func generateMCP(root string) (string, error) {
	bin, cleanup, err := buildCLI(root)
	defer cleanup()
	if err != nil {
		return "", err
	}

	raw, err := capture(bin, "mcp", "-list")
	if err != nil {
		return "", fmt.Errorf("listing the MCP tools: %w", err)
	}

	var tools []mcpTool
	if err := json.Unmarshal([]byte(raw), &tools); err != nil {
		return "", fmt.Errorf("the tool listing is not the documented JSON: %w", err)
	}
	if len(tools) == 0 {
		return "", fmt.Errorf("the binary listed no MCP tools; publishing that would claim the server offers nothing")
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	var b strings.Builder
	b.WriteString(`# The MCP server

An agent writing a Go desktop app on a Mac cannot find out whether it works on
Windows. This lets it ask, get an answer from real Windows, and see the screen
when the answer is that it hung.

Everything on this page was captured from the compiled binary by listing a live
server, so it is what a client is actually told rather than what a file says it
should be.

## Connecting

The server speaks the Model Context Protocol on stdin and stdout. A client
spawns it:

` + "```json" + `
{
  "mcpServers": {
    "irgo-winvm": {
      "command": "irgo-winvm",
      "args": ["mcp"]
    }
  }
}
` + "```" + `

It needs macOS on Apple Silicon and UTM, which ` + "`vm-create`" + ` installs itself. A
client on any other platform can start it and get nothing useful from it.

Nothing else may write to stdout while it runs: that is the protocol channel.
Commands print their progress, which is why the server collects that output and
returns it in the result instead.

## What the tools are

The tools are the commands, generated from one list — there is no separate MCP
surface that could offer something the CLI cannot do, or drift from it.

`)

	b.WriteString("| tool | what it does | safe to call |\n|---|---|---|\n")
	for _, t := range tools {
		safety := "changes things"
		switch {
		case t.Annotations.DestructiveHint:
			safety = "**destroys something** — needs `-force`"
		case t.Annotations.ReadOnlyHint:
			safety = "reports only"
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", t.Name, t.Description, safety))
	}

	b.WriteString(`
## Arguments

Each tool's flags are typed properties, with their real defaults, plus
` + "`args`" + ` for anything positional — the path to a ` + "`.exe`" + `, or a directory.

`)
	for _, t := range tools {
		var names []string
		for n := range t.InputSchema.Properties {
			if n != "args" {
				names = append(names, "`-"+n+"`")
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			b.WriteString(fmt.Sprintf("- `%s` — no flags\n", t.Name))
			continue
		}
		b.WriteString(fmt.Sprintf("- `%s` — %s\n", t.Name, strings.Join(names, ", ")))
	}

	b.WriteString(`
None of that is transcribed. The schema is generated from the same
` + "`flag.FlagSet`" + ` the command line parses, so a default shown here cannot differ
from the one the CLI uses — not because a test compares them, but because there
is only one registration and both read it.

## What a failure looks like

A command that fails returns a **result**, not a protocol error, so the model
can see it and correct itself. The result carries structured content:

` + "```json" + `
{"command": "app-create", "code": 4, "status": "no-agent",
 "meaning": "the VM is there, the guest agent is not answering — wait and try again",
 "retryable": true}
` + "```" + `

Match on ` + "`status`" + ` or ` + "`code`" + `, never on the wording — the wording will change.

**` + "`retryable`" + ` is the field that matters.** Windows Update takes the guest agent
away for minutes at a time and the VM is fine. An agent that cannot tell that
from "no such VM" either abandons a working VM or retries forever against one
that will never exist. It is true for exactly one code, and the full table is in
the [README](index.html).

## Seeing the screen

` + "`vm-screen`" + ` returns the PNG itself, as image content. From the host a stuck
boot and a working one are identical — that is why the command exists — and a
file path is useless to a caller that cannot open one, or that is on another
machine.

## The long calls do not block

` + "`vm-create -install`" + ` takes about 45 minutes and ` + "`iso-create -fetch`" + ` downloads
4.2 GB. Both start the work and return a job id immediately, because every
client times out long before that:

` + "```json" + `
{"command": "vm-create", "job": "vm-create-20260814-150000", "running": true}
` + "```" + `

The work runs in its own process group and outlives the connection that asked
for it — closing the client does not kill the install. Call ` + "`status`" + ` with the id
to find out whether it is still alive, and ` + "`vm-screen`" + ` to see what it is doing.

Whether a job is alive is measured by signalling the process, not by reading a
file that claims it is running. A handle that says "running" forever because
nothing checked is worse than no handle.

Starting the same command with the same arguments twice returns the job already
running rather than beginning a second one — a client that timed out simply asks
again, and two installs against one VM is the failure that would cause.

## Reading the documentation without a network

The server offers one resource, ` + "`irgo-winvm://reference`" + `: every command, every
flag and every default, generated from the running binary's own flag
definitions. An agent can read it before guessing at arguments.

It is generated rather than embedded, and that was a decision rather than an
oversight. Embedding the whole documentation would mean committing a generated
file — ` + "`go:embed`" + ` needs it present at compile time and it is produced into a
directory git ignores, so a fresh clone would not build. Committing it makes a
second copy that goes stale; a placeholder overwritten at release means a
development build serves an empty document and says nothing about it.

So the binary serves what it genuinely knows and links to
[llms-full.txt](llms-full.txt) for the prose it does not carry. A stale answer
is worse than a link.

## Has this been run for real?

Yes, on 14 August 2026, from a real client against a real VM: nine tools listed,
` + "`doctor`" + ` returned as a result, ` + "`vm-screen`" + ` returned a 4.4 MB PNG of a live
Windows desktop, and ` + "`app-create`" + ` pushed a Go binary into Windows on ARM64 and
brought its output back. The measurements are in [Results](results.html).

One thing is still unproven: a **genuinely long** job. The detached path works
and survives the client exiting, but the 45-minute ` + "`vm-create -install`" + ` it was
written for has not been driven over MCP.

## Over HTTP, on this machine only

` + "`irgo-winvm mcp -http 127.0.0.1:8129`" + ` serves the same tools over HTTP instead
of stdin and stdout. **Read the [threat model](threat-model.html) first**: the
product is "run this arbitrary binary on my machine", so anything reaching that
port can execute code of its choosing in the guest.

A non-loopback address is **refused**, not warned about — authentication is not
built yet, so the only defence is that nothing off this machine can reach it. A
bare ` + "`:8129`" + ` is refused too: it reads like a local default and binds every
interface, and it is rejected rather than quietly rewritten, because rewriting
would make the flag do something other than what it says.

The session is stateless, which the current protocol revision requires — a
stateful server negotiates down to the older one. GET and DELETE return 405, and
server-to-client requests are rejected outright, which is why a long job is
keyed by an id in the tool arguments rather than by a session.

DNS rebinding protection is on: a request arriving on loopback with a
non-localhost ` + "`Host`" + ` header is rejected. That defence comes from the SDK, and
the work here was to leave it alone.

## What it does not do yet

**Authentication.** Until it exists, the HTTP transport is loopback-only by
refusal rather than by default.

**Uploads.** ` + "`app-create`" + ` takes a path on the server's filesystem, so a remote
agent with a freshly cross-compiled binary and no shared disk cannot yet do the
one thing this tool is for.

**A lock.** Two clients could drive one VM at once.
`)
	return b.String(), nil
}
