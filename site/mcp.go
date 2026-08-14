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

Every tool takes one argument, ` + "`args`" + `: the command line as an array of
strings, exactly as it would be typed.

`)
	for _, t := range tools {
		if p, ok := t.InputSchema.Properties["args"]; ok && p.Description != "" {
			b.WriteString(fmt.Sprintf("- `%s` — %s\n", t.Name, p.Description))
		}
	}

	b.WriteString(`
One schema for all of them is a deliberate trade. A typed schema per command
would declare each command's flags a second time — once for the CLI, once for
MCP — and the two would disagree the first time a default changed. The cost is
that the schema helps an agent less, and the
[command reference](reference.html) is where the flags are.

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

## What it does not do yet

` + "`vm-create -install`" + ` takes about 45 minutes and every client times out long
before that. Until long-running work returns a handle, that call blocks and the
install carries on without anything to ask about it. That is the next piece of
work, not a property of the design.

Remote access — the server over HTTP rather than stdin and stdout — is not
built. It is remote code execution by design and needs its own authentication,
upload path and locking, so it waits until this half has run against a real VM.
`)
	return b.String(), nil
}
