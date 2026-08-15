// Package command is the list of commands irgo-winvm has, and nothing else.
//
// It holds what a command *is* — its name, what it does, and what undoes it —
// and deliberately not what it does when run. The handlers stay in package
// main, which wires each name to the function that performs it.
//
// The split exists because four things need to know which commands exist:
//
//   - dispatch, and the usage text it generates
//   - `irgo-winvm commands`, which prints one name per line
//   - the site's flag reference and the CI documentation check
//   - the MCP server, which registers one tool per command
//
// The first three get the list by running the binary, and that is what makes
// the reference page trustworthy: it reports what the compiled tool actually
// accepts rather than what a file says it should. The fourth cannot — tool
// registration happens in-process, so it has to import the list. A list living
// inside package main is unreachable from anywhere else in the module, and the
// server would have had to declare its own copy.
//
// So: one list, read through two doors. Nobody holds a second one.
package command

import (
	"fmt"
	"strings"
)

// Command is what a command is, without what it does.
type Command struct {
	Name    string
	Summary string

	// Undo names the command that reverses this one, so the usage can pair
	// them. Every make has one; that is the repository's rule, not a detail.
	Undo string

	// IsUndo keeps a reversing command out of the first column, since it is
	// already printed beside the command it undoes.
	IsUndo bool

	// ReadOnly marks a command that changes nothing: it reports, it does not
	// act. An agent can call one of these to find out where it is without
	// having to reason about consequences.
	ReadOnly bool

	// Destructive marks a command that removes something expensive to get
	// back — a 45-minute install, a 4.2 GB download from a rate-limited
	// source. These keep -force as an argument that is never defaulted.
	//
	// Declared rather than inferred. The obvious rule, "IsUndo means
	// destructive", is close enough to look right and wrong in both
	// directions: it would be a guess, and the guess is shipped to an agent
	// as a claim about what is safe to call.
	Destructive bool

	// Mutates marks a command that changes state on disk — media, a VM, or a
	// staged binary — and therefore needs the mutation lock.
	//
	// Declared, not inferred. The obvious rule, "!ReadOnly means mutates", is
	// wrong in exactly one place: `mcp` changes nothing itself but serves
	// mutations, so it is not read-only and also not a mutation. One
	// exception is enough to make the rule a guess.
	Mutates bool

	// Detach names the flag that makes this command long-running.
	//
	// With that flag present, an MCP call starts the work and returns a handle
	// instead of blocking: vm-create -install is about 45 minutes and every
	// client times out long before that. Without it the same command is quick
	// and runs inline, so the caller gets its answer rather than a job to poll.
	//
	// A flag rather than a duration, because the duration is a property of what
	// was asked for, not of the command. `iso-create` rebuilds from a local
	// .esd in about 50 seconds; `iso-create -fetch` downloads 4.2 GB first.
	Detach string

	// OverMCP is false for a command that makes no sense as a tool.
	//
	// `commands` and `version` exist for tooling that has to scrape a binary;
	// a connected client already has the tool list and the server's version
	// from the protocol. `help` is the usage text, which a client gets as tool
	// descriptions. Exposing them would be three tools that answer questions
	// the transport already answered.
	OverMCP bool
}

// All is the only place a command is declared.
//
// It was a switch and a hand-typed usage block — two copies of the same list,
// already disagreeing about whether `-h` worked on every command. A flag
// reference page, a CI check and an MCP tool list would have made five copies.
// One list is the only way this stays true: a command that is not here does not
// exist, and one that is here cannot be missing from the usage or the docs.
//
// Order is the order the usage prints, which is the order they are run in.
var All = []Command{
	{Name: "iso-create", Summary: "the Windows installer", Undo: "iso-delete", Mutates: true, Detach: "-fetch", OverMCP: true},
	{Name: "vm-create", Summary: "a VM with Windows on it, from that", Undo: "vm-delete", Mutates: true, Detach: "-install", OverMCP: true},
	{Name: "app-create", Summary: "your .exe pushed to that VM and run", Undo: "app-delete", Mutates: true, OverMCP: true},
	{Name: "app-upload", Summary: "stage a binary for app-create, from bytes over MCP", Undo: "app-delete", Mutates: true, OverMCP: true},

	{Name: "iso-delete", Summary: "remove the installer", IsUndo: true, Mutates: true, Destructive: true, OverMCP: true},
	{Name: "vm-delete", Summary: "remove the VM", IsUndo: true, Mutates: true, Destructive: true, OverMCP: true},
	{Name: "app-delete", Summary: "remove your .exe from the VM", IsUndo: true, Mutates: true, Destructive: true, OverMCP: true},

	{Name: "vm-screen", Summary: "photograph the VM, for when it is stuck", ReadOnly: true, OverMCP: true},
	{Name: "doctor", Summary: "what is here, and where the log and screenshots are", ReadOnly: true, OverMCP: true},
	{Name: "status", Summary: "long-running work: what is going, what finished, how long", ReadOnly: true, OverMCP: true},
	{Name: "help", Summary: "the three steps explained, and what your .exe has to be", ReadOnly: true},
	{Name: "version", Summary: "what this binary is", ReadOnly: true},
	{Name: "commands", Summary: "one command name per line, for tooling", ReadOnly: true},
	{Name: "mcp", Summary: "serve these commands to an agent over MCP, on stdin and stdout"},
}

// DetachedBy reports whether these arguments make this command long-running.
//
// Matching -install and -install=true and -install true alike, because a caller
// writes whichever it prefers and a missed match means a 45-minute call that
// blocks — the exact failure jobs exist to prevent.
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

// Find returns the command by name.
//
// Spelling aliases are not handled here: `-h` meaning `help` is a fact about a
// command line, not about the list, and the MCP server has no such thing.
func Find(name string) (Command, bool) {
	for _, c := range All {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// UsageText is generated from All, so it cannot list a command that does not
// exist or omit one that does.
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
