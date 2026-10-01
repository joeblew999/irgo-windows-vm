package main

// The MCP page, captured from the binary.
//
// Same rule as the command reference: no tool name, description or annotation
// is transcribed. The generator builds the CLI, runs `irgo-winvm mcp -list`, and
// renders what a connected client would actually be told.
//
// Captured rather than imported. Importing mcpserver would give the same data
// with less machinery — and would drag the protocol SDK and its eight
// dependencies into a module whose go.mod requires one kind of thing: a
// markdown parser, and the goldmark extensions and syntax highlighter that
// render it (render.go). The boundary is worth more than the machinery it
// saves.

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

	help, err := capture(bin, "mcp", "-h")
	if err != nil {
		return "", fmt.Errorf("mcp -h: %w", err)
	}
	instructions, err := agentInstructions(help)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`# The MCP server

` + "`" + `irgo-winvm mcp` + "`" + ` lets an AI agent test a Go desktop app on real Windows. An
agent writing the app on a Mac can run it in the VM, get the output back, and
see the screen when the program hangs.

This page is captured from the compiled binary by listing a live server, so it
shows exactly what a client is told.

## Install

Install the binary first ([the ways to install it](index.html#install)); the
release is all you need, with no checkout, Go toolchain or mise. Then check it:

` + "```" + `sh
irgo-winvm doctor     # what is set up, and the next steps in order
` + "```" + `

## Connect

The server speaks the Model Context Protocol on stdin and stdout. Register it
once with your client.

**Claude Code:**

` + "```" + `sh
claude mcp add irgo-winvm -- irgo-winvm mcp
` + "```" + `

Add ` + "`" + `--scope user` + "`" + ` to have it in every project, not just this one.

**Claude Desktop and other clients** that read an ` + "`" + `mcpServers` + "`" + ` file (Claude
Desktop's is ` + "`" + `~/Library/Application Support/Claude/claude_desktop_config.json` + "`" + `):

` + "```" + `json
{
  "mcpServers": {
    "irgo-winvm": {
      "command": "irgo-winvm",
      "args": ["mcp"]
    }
  }
}
` + "```" + `

- **Use the full path** (` + "`" + `command -v irgo-winvm` + "`" + ` prints it) as ` + "`" + `command` + "`" + ` when the
  client is an app that does not inherit your shell's PATH, as Claude Desktop
  does not.
- **Agents working in this repository** are already connected: ` + "`" + `.mcp.json` + "`" + `
  registers the server.
- **It needs macOS on Apple Silicon** and UTM, which ` + "`" + `vm-create` + "`" + ` installs. A
  client on another platform can start the server but gets nothing useful.
- **Nothing else may write to stdout** while it runs, because stdout is the
  protocol channel. Commands print progress, so the server collects that output
  and returns it in the tool result.

## A typical session

What an agent does to answer "does my app work on Windows?", each a tool call:

1. ` + "`" + `doctor` + "`" + `: what is set up, and the next step.
2. ` + "`" + `vm-create` + "`" + ` with ` + "`" + `vm: "agent1"` + "`" + `: a VM of its own. With a golden image this is a
   clone that answers in seconds. Without one, ` + "`" + `install: true` + "`" + ` returns a job id,
   and ` + "`" + `status` + "`" + ` with that id (in ` + "`" + `args` + "`" + `) says when it is done; it pulls the golden
   image from the [private cache](using.html#the-private-r2-cache) when
   ` + "`" + `IRGO_GOLDEN_URL` + "`" + ` and ` + "`" + `IRGO_GOLDEN_TOKEN` + "`" + ` are set, or installs Windows
   (about 45 minutes, once; ` + "`" + `iso-create` + "`" + ` with ` + "`" + `fetch: true` + "`" + ` first).
3. Build the app with ` + "`" + `GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -o app.exe` + "`" + `.
4. ` + "`" + `app-create` + "`" + ` with ` + "`" + `vm: "agent1"` + "`" + `, ` + "`" + `gui: true` + "`" + ` for a window, and ` + "`" + `args: ["/path/to/app.exe"]` + "`" + `:
   the program's output and exit code.
5. ` + "`" + `vm-screen` + "`" + ` with ` + "`" + `vm: "agent1"` + "`" + `: the screen as an image, when the answer is that
   it hung.
6. ` + "`" + `vm-delete` + "`" + ` with ` + "`" + `vm: "agent1"` + "`" + ` and ` + "`" + `force: true` + "`" + `, when it is finished with it.

## What the agent is told

The server sends this with its initialize response, so a connected agent has
it before its first call. Captured from ` + "`" + `irgo-winvm mcp -h` + "`" + `:

` + "```" + `text
` + instructions + `
` + "```" + `

## Tools

Each tool is one CLI command, generated from the same list. The MCP surface
can't offer anything the CLI can't do, and can't drift from it.

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

Each tool's flags are typed properties with their real defaults. Anything
positional, such as the path to a ` + "`" + `.exe` + "`" + ` or a directory, goes in ` + "`" + `args` + "`" + `.

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
None of this is transcribed. The schema is generated from the same
` + "`" + `flag.FlagSet` + "`" + ` the command line parses, so a default shown here can't differ
from the CLI's: there is one registration, and both read it.

## Handle failures

A failed command returns a **result**, not a protocol error, so the model can
see it and correct itself. The result carries structured content:

` + "```" + `json
{"command": "app-create", "code": 4, "status": "no-agent",
 "meaning": "the VM is there, the guest agent is not answering — wait and try again",
 "retryable": true}
` + "```" + `

- **Match on ` + "`" + `status` + "`" + ` or ` + "`" + `code` + "`" + `**, never on the wording. The wording will change.
- **Check ` + "`" + `retryable` + "`" + ` first.** It is true for two codes: 4 (` + "`" + `no-agent` + "`" + `) and 6
  (` + "`" + `busy` + "`" + `). Windows Update takes the guest agent away for minutes at a time
  while the VM is fine, and another client may hold the mutation lock. An agent
  that can't tell these from "no such VM" either abandons a working VM or
  retries forever against one that will never exist.

Every code is explained in [Using it](using.html#what-it-exits-with).

## See the screen

` + "`" + `vm-screen` + "`" + ` returns the PNG itself, as image content, not a file path. From the
host, a stuck boot and a working one look identical, which is why the tool
exists. A path would be useless to a caller that can't open files, or that is on
another machine.

## Long calls return a job

` + "`" + `vm-create -install` + "`" + ` takes about 45 minutes, and ` + "`" + `iso-create -fetch` + "`" + ` downloads
4.2 GB. Every client times out long before either finishes, so both start the
work and return a job id immediately:

` + "```" + `json
{"command": "vm-create", "job": "vm-create-20260814-150000", "running": true}
` + "```" + `

- **The work outlives the connection.** It runs in its own process group, so
  closing the client doesn't kill the install.
- **Call ` + "`" + `status` + "`" + `** with the id to find out whether it is still running, and
  ` + "`" + `vm-screen` + "`" + ` to see what it is doing.
- **Liveness is measured**, by signalling the process, not read from a file
  that claims it is running. A handle that says "running" forever because
  nothing checked is worse than no handle.
- **Asking twice is safe.** The same command with the same arguments returns
  the job already running instead of starting a second one. A client that timed
  out can simply ask again without starting two installs against one VM.

## Read the reference offline

The server offers one resource, ` + "`" + `irgo-winvm://reference` + "`" + `: every command, flag
and default, generated from the running binary's own flag definitions. Read it
before guessing at arguments.

For the prose documentation, it links to [llms-full.txt](llms-full.txt) rather
than embedding it. Embedding would mean committing a generated file: ` + "`" + `go:embed` + "`" + `
needs it at compile time, and it is built into a directory git ignores, so a
fresh clone wouldn't build. A committed copy goes stale, and a placeholder
overwritten at release means a development build silently serves an empty
document. A link is better than a stale answer.

## Serve over HTTP

` + "`" + `irgo-winvm mcp -http 127.0.0.1:8129` + "`" + ` serves the same tools over Streamable HTTP
instead of stdin and stdout.

> [!WARNING]
> **Read the [threat model](threat-model.html) first.** The tool exists to run
> arbitrary binaries, so anything that can call it can run code of its choice
> in the guest.

| you pass | what happens |
|---|---|
| a loopback address | serves on this machine only |
| a bare port, such as ` + "`" + `:8129` + "`" + ` | resolved to ` + "`" + `127.0.0.1:8129` + "`" + `. A port means this machine, not every interface |
| any other address | refused, unless you also pass ` + "`" + `-allow-remote` + "`" + ` **and** set ` + "`" + `IRGO_WINVM_TOKEN` + "`" + ` |

Off loopback, the token is mandatory: a server that would start unauthenticated
is refused, not warned about. Clients send it as a bearer token, and it is
compared in constant time.

**Upload a binary.** A remote agent with a freshly cross-compiled ` + "`" + `.exe` + "`" + ` and no
shared disk sends it in chunks with ` + "`" + `app-upload` + "`" + `. The server stages it under
` + "`" + `bin/` + "`" + `, named by its SHA-256, and verifies the hash before committing it. Pass
the staged path to ` + "`" + `app-create` + "`" + `.

**One mutation at a time.** A second client that tries to change something while
another is working gets exit code 6 (` + "`" + `busy` + "`" + `, retryable). It is refused, never
silently queued.

**Sessions are stateless**, as the current protocol revision requires; a stateful
server negotiates down to the older one. GET and DELETE return 405 and
server-to-client requests are rejected, which is why a long job is keyed by an
id in the tool arguments rather than by a session.

**DNS-rebinding protection is on.** A request arriving on loopback with a
non-localhost ` + "`" + `Host` + "`" + ` header is rejected. The SDK provides this; the work here
was to leave it alone.

## Verified against a real VM

- **14 August 2026**, over stdio: nine tools listed (the server now has more),
  ` + "`" + `doctor` + "`" + ` returned as a result, ` + "`" + `vm-screen` + "`" + ` returned a 4.4 MB PNG of a live
  Windows desktop, and ` + "`" + `app-create` + "`" + ` pushed a Go binary into Windows on ARM64
  and brought its output back.
- **16 August 2026**, over HTTP: ` + "`" + `app-upload` + "`" + ` staged a binary in four chunks,
  and ` + "`" + `app-create` + "`" + ` ran it in the VM.

The measurements are in [Results](results.html).

**Not yet proven: a long job.** The detached path works and survives the client
exiting, but the 45-minute ` + "`" + `vm-create -install` + "`" + ` it was written for has not been
driven over MCP. See the [roadmap](roadmap.html).
`)
	return b.String(), nil
}

// instructionsHeading is the line in `mcp -h` that the server's instructions
// follow, indented, until the flag list.
const instructionsHeading = "What the server tells the agent when it connects:"

// agentInstructions cuts the server's instructions out of `mcp -h`, without
// their indent. Missing is an error: a page without them would say the server
// tells the agent nothing.
func agentInstructions(help string) (string, error) {
	_, after, ok := strings.Cut(help, instructionsHeading)
	if !ok {
		return "", fmt.Errorf("mcp -h has no %q section", instructionsHeading)
	}
	var lines []string
	for _, l := range strings.Split(after, "\n") {
		if strings.HasPrefix(l, "  -") {
			break // the flags
		}
		lines = append(lines, strings.TrimPrefix(l, "    "))
	}
	s := strings.TrimSpace(strings.Join(lines, "\n"))
	if s == "" {
		return "", fmt.Errorf("mcp -h's %q section is empty", instructionsHeading)
	}
	return s, nil
}
