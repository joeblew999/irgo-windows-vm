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

	// Locks says which mutation locks the command takes, and so whether it
	// changes state on disk — media, a VM, the golden image or a staged
	// binary. Zero means it takes none.
	//
	// Declared, not inferred. The obvious rule, "!ReadOnly means mutates", is
	// wrong in exactly one place: `mcp` changes nothing itself but serves
	// mutations, so it is not read-only and also not a mutation. One
	// exception is enough to make the rule a guess.
	//
	// It was a bool, Mutates, when there was one lock for the whole machine.
	// With a lock per VM the question is no longer whether but which, and a
	// bool beside a list of locks would be two answers that can disagree.
	Locks Locks

	// Detach names the flag that makes this command long-running, or
	// DetachAlways for a command that is long-running whatever it is given.
	//
	// With that flag present, an MCP call starts the work and returns a handle
	// instead of blocking: vm-create -install is about 45 minutes and every
	// client times out long before that. Without it the same command is quick
	// and runs inline, so the caller gets its answer rather than a job to poll.
	//
	// A flag rather than a duration, because the duration is a property of what
	// was asked for, not of the command. `iso-create` rebuilds from a local
	// .esd in about 50 seconds; `iso-create -fetch` downloads 4.2 GB first.
	// vm-golden-create has no quick form — it seals Windows, which is minutes
	// of decryption and component cleanup at best — so it is DetachAlways.
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

// Locks is which mutation locks a command takes. The locks themselves, and
// what each one guards, are in internal/utmvm/lock.go; this only declares
// which a command needs, so the MCP server and the CLI read one answer.
type Locks uint8

const (
	// LockMachine is for what every VM shares: the media and the golden image.
	LockMachine Locks = 1 << iota

	// LockVM is for the one VM the command's -vm flag names.
	LockVM

	// LockStage is for the binaries staged under bin/ for app-create.
	LockStage
)

// Mutates reports whether the command changes state on disk, which is the
// same as taking any lock.
func (c Command) Mutates() bool { return c.Locks != 0 }

// DetachAlways is Detach for a command that is long-running whatever it is
// given. Not a real flag, and never matched against one: it cannot be spelled
// on a command line.
const DetachAlways = "(always)"

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
	{Name: "iso-create", Summary: "the Windows installer", Undo: "iso-delete", Locks: LockMachine, Detach: "-fetch", OverMCP: true},
	{Name: "vm-create", Summary: "a VM with Windows on it, from that", Undo: "vm-delete", Locks: LockVM, Detach: "-install", OverMCP: true},
	{Name: "app-create", Summary: "your .exe pushed to that VM and run", Undo: "app-delete", Locks: LockVM, OverMCP: true},
	{Name: "app-upload", Summary: "stage a binary for app-create, from bytes over MCP", Undo: "app-delete", Locks: LockStage, OverMCP: true},
	// A golden image is a sealed copy of an installed VM, which vm-create then
	// clones in seconds instead of installing for 45 minutes. Optional, and a
	// make like any other, so it has an undo.
	{Name: "vm-golden-create", Summary: "seal a disposable VM into the image vm-create clones", Undo: "vm-golden-delete", Locks: LockMachine | LockVM, Detach: DetachAlways, OverMCP: true},

	{Name: "iso-delete", Summary: "remove the installer", IsUndo: true, Locks: LockMachine, Destructive: true, OverMCP: true},
	{Name: "vm-delete", Summary: "remove the VM", IsUndo: true, Locks: LockVM, Destructive: true, OverMCP: true},
	{Name: "app-delete", Summary: "remove your .exe from the VM", IsUndo: true, Locks: LockVM | LockStage, Destructive: true, OverMCP: true},
	{Name: "vm-golden-delete", Summary: "remove the golden image", IsUndo: true, Locks: LockMachine, Destructive: true, OverMCP: true},

	{Name: "vm-screen", Summary: "photograph the VM, for when it is stuck", ReadOnly: true, OverMCP: true},
	{Name: "vm-repair", Summary: "fix an expired password and a stale WebView2 registration, as SYSTEM", Locks: LockVM, OverMCP: true},
	{Name: "doctor", Summary: "what is here, and where the log and screenshots are", ReadOnly: true, OverMCP: true},
	{Name: "status", Summary: "long-running work: what is going, what finished, how long", ReadOnly: true, OverMCP: true},
	// Only in a checkout of this repository: they build and read examples/.
	// See internal/glazecheck for why they are in the shipped binary at all.
	//
	// glaze-check takes no lock here: the Mac run touches no VM, and making it
	// take one would refuse the fifteen-second inner loop for the whole of a
	// 45-minute install. The Windows run takes that VM's lock itself, once,
	// around all four app-create runs. -windows is about a minute and a half,
	// often more when the guest has to be recovered first, so over MCP it is a
	// job.
	{Name: "glaze-check", Summary: "does glaze work? run the four examples here or -windows, record the verdict", Detach: "-windows", OverMCP: true},
	{Name: "glaze-status", Summary: "the recorded glaze verdict, Mac and Windows, and whether it still holds", ReadOnly: true, OverMCP: true},
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
	switch c.Detach {
	case "":
		return false
	case DetachAlways:
		return true
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
	// Columns sized from the list, so a longer name (vm-golden-create) widens
	// them rather than pushing its own row out of line.
	name, summary := 0, 0
	for _, c := range All {
		name = max(name, len(c.Name))
		if c.Undo != "" {
			summary = max(summary, len(c.Summary))
		}
	}
	fmt.Fprintf(&b, "  %-*s %-*s %s\n", name, "MAKE", summary, "", "UNDO")
	for _, c := range All {
		if c.Undo == "" {
			continue
		}
		fmt.Fprintf(&b, "  %-*s %-*s %s\n", name, c.Name, summary, c.Summary, c.Undo)
	}
	b.WriteString("\n")
	for _, c := range All {
		if c.Undo != "" || c.IsUndo {
			continue
		}
		fmt.Fprintf(&b, "  %-*s %s\n", name, c.Name, c.Summary)
	}
	b.WriteString("\nRun them in the order above. Each takes -h for its flags.\n")
	return b.String()
}
