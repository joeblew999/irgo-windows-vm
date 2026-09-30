// Package command declares which commands irgo-winvm has and what each one is,
// but not what it does: the implementations are in package main.
//
// It is a separate package so the MCP server can import the list in-process.
// Everything else (the usage text, `irgo-winvm commands`, the site's flag
// reference, the docs check) reads it from the built binary.
package command

import (
	"fmt"
	"strings"
)

// Command is what a command is, without what it does.
type Command struct {
	Name    string
	Summary string

	// Undo names the command that reverses this one. Every make has one.
	Undo string

	// IsUndo marks a reversing command, which the usage prints beside the
	// command it undoes rather than in its own row.
	IsUndo bool

	// ReadOnly marks a command that changes nothing.
	ReadOnly bool

	// Destructive marks a command that removes something expensive to get
	// back, such as a 45-minute install or a 4.2 GB download. It requires
	// -force, which is never defaulted. Declared, not inferred from IsUndo,
	// because it is shipped to an agent as a claim about what is safe.
	Destructive bool

	// Mutates marks a command that changes state on disk and so takes the
	// mutation lock. Not simply !ReadOnly: `mcp` changes nothing itself.
	Mutates bool

	// Detach names the flag that makes this command long-running. With it,
	// an MCP call starts a job and returns a handle instead of blocking past
	// every client's timeout. A flag rather than a property of the command,
	// because `iso-create` takes about 40 s (docs/RESULTS.md) and `-fetch` downloads
	// 4.2 GB first.
	Detach string

	// OverMCP is false for commands a connected client has no use for:
	// `commands`, `version` and `help` answer what the protocol already does.
	OverMCP bool
}

// All is every command, in the order the usage prints them. A command that is
// not here does not exist.
var All = []Command{
	{Name: "iso-create", Summary: "the Windows installer", Undo: "iso-delete", Mutates: true, Detach: "-fetch", OverMCP: true},
	{Name: "vm-create", Summary: "a VM with Windows on it, from that", Undo: "vm-delete", Mutates: true, Detach: "-install", OverMCP: true},
	{Name: "app-create", Summary: "your .exe pushed to that VM and run", Undo: "app-delete", Mutates: true, OverMCP: true},
	{Name: "app-upload", Summary: "stage a binary for app-create, from bytes over MCP", Undo: "app-delete", Mutates: true, OverMCP: true},

	{Name: "iso-delete", Summary: "remove the installer", IsUndo: true, Mutates: true, Destructive: true, OverMCP: true},
	{Name: "vm-delete", Summary: "remove the VM", IsUndo: true, Mutates: true, Destructive: true, OverMCP: true},
	{Name: "app-delete", Summary: "remove your .exe from the VM", IsUndo: true, Mutates: true, Destructive: true, OverMCP: true},

	{Name: "vm-screen", Summary: "photograph the VM, for when it is stuck", ReadOnly: true, OverMCP: true},
	{Name: "vm-repair", Summary: "fix an expired password and a stale WebView2 registration, as SYSTEM", Mutates: true, OverMCP: true},
	{Name: "doctor", Summary: "what is here, and where the log and screenshots are", ReadOnly: true, OverMCP: true},
	{Name: "status", Summary: "long-running work: what is going, what finished, how long", ReadOnly: true, OverMCP: true},
	// glaze-check and glaze-status work only in a checkout of this repository.
	// glaze-check is not Mutates: the Mac run touches no VM, and taking the
	// lock would block it for the whole of an install. -windows takes the lock
	// itself, and takes a minute and a half or more, so over MCP it is a job.
	{Name: "glaze-check", Summary: "does glaze work? run the conformance suite here or -windows, record every test", Detach: "-windows", OverMCP: true},
	{Name: "glaze-status", Summary: "the recorded glaze verdict, Mac and Windows, and whether it still holds", ReadOnly: true, OverMCP: true},
	{Name: "help", Summary: "the three steps explained, and what your .exe has to be", ReadOnly: true},
	{Name: "version", Summary: "what this binary is", ReadOnly: true},
	{Name: "commands", Summary: "one command name per line, for tooling", ReadOnly: true},
	{Name: "mcp", Summary: "serve these commands to an agent over MCP, on stdin and stdout"},
}

// DetachedBy reports whether args include c's Detach flag, in any of the forms
// the flag package accepts (-install, -install=true).
func (c Command) DetachedBy(args []string) bool {
	if c.Detach == "" {
		return false
	}
	for _, a := range args {
		if a == c.Detach || strings.HasPrefix(a, c.Detach+"=") {
			return true
		}
	}
	return false
}

// Find returns the command with the given name. Command-line aliases such as
// -h are not commands and are resolved by the caller.
func Find(name string) (Command, bool) {
	for _, c := range All {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// UsageText is the list of commands a bare `irgo-winvm` prints, generated from
// All.
func UsageText() string {
	var b strings.Builder
	b.WriteString("irgo-winvm — build a Go program on your Mac, run it on real Windows.\n\n")
	b.WriteString("  MAKE                                                 UNDO\n")
	for _, c := range All {
		if c.Undo == "" {
			continue
		}
		fmt.Fprintf(&b, "  %-12s %-39s %s\n", c.Name, c.Summary, c.Undo)
	}
	b.WriteString("\n")
	for _, c := range All {
		if c.Undo != "" || c.IsUndo {
			continue
		}
		fmt.Fprintf(&b, "  %-12s %s\n", c.Name, c.Summary)
	}
	b.WriteString("\nRun them in the order above. Each takes -h for its flags.\n")
	return b.String()
}
