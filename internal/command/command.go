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

	// Locks is which mutation locks the command takes; zero means it changes
	// nothing on disk. Not simply !ReadOnly: `mcp` changes nothing itself.
	// The locks themselves are in internal/utmvm/lock.go.
	Locks Locks

	// Detach names the flag that makes this command long-running, or is
	// DetachAlways. With it, an MCP call starts a job and returns a handle
	// instead of blocking past every client's timeout. A flag rather than a
	// property of the command, because `iso-create` takes about 40 s
	// (docs/RESULTS.md) and `-fetch` downloads 4.2 GB first.
	Detach string

	// OverMCP is false for commands a connected client has no use for:
	// `commands`, `version` and `help` answer what the protocol already does.
	OverMCP bool
}

// Locks is a set of mutation locks.
type Locks uint8

const (
	// LockMachine guards what every VM shares: the media and the golden image.
	LockMachine Locks = 1 << iota
	// LockVM guards the one VM the command's -vm flag names.
	LockVM
	// LockStage guards the caller's part of bin/, the binaries it staged for
	// app-create.
	LockStage
	// LockEachVM is a command that takes each VM's lock itself, one at a time,
	// as it works through them (vm-reap), so it holds up no VM it is not
	// touching. The dispatcher takes nothing for it.
	LockEachVM
)

// Mutates reports whether c changes state on disk, which is whether it takes
// any lock.
func (c Command) Mutates() bool { return c.Locks != 0 }

// DetachAlways is Detach for a command with no quick form. It is not a flag
// and cannot be passed as one.
const DetachAlways = "(always)"

// All is every command, in the order the usage prints them. A command that is
// not here does not exist.
var All = []Command{
	{Name: "iso-create", Summary: "the Windows installer, from Microsoft with -fetch", Undo: "iso-delete", Locks: LockMachine, Detach: "-fetch", OverMCP: true},
	{Name: "vm-create", Summary: "a Windows VM, cloned from the golden image or -install", Undo: "vm-delete", Locks: LockVM, Detach: "-install", OverMCP: true},
	{Name: "app-create", Summary: "your .exe pushed into that VM and run, output back", Undo: "app-delete", Locks: LockVM, OverMCP: true},
	{Name: "app-upload", Summary: "stage a binary for app-create, from bytes over MCP", Undo: "app-delete", Locks: LockStage, OverMCP: true},
	// Sealing is many minutes even with nothing to decrypt, so it is always a
	// job over MCP.
	{Name: "vm-golden-create", Summary: "seal a disposable VM into the image vm-create clones", Undo: "vm-golden-delete", Locks: LockMachine | LockVM, Detach: DetachAlways, OverMCP: true},
	// The golden image's private R2 cache. Gigabytes either way, so always a
	// job over MCP. An Undo with a flag names the command and the flag.
	{Name: "vm-golden-push", Summary: "upload the golden image to your private R2 bucket", Undo: "vm-golden-push -delete", Locks: LockMachine, Detach: DetachAlways, OverMCP: true},
	{Name: "vm-golden-pull", Summary: "pull the golden image from your private R2 bucket", Undo: "vm-golden-pull -delete", Locks: LockMachine, Detach: DetachAlways, OverMCP: true},

	{Name: "iso-delete", Summary: "remove the installer", IsUndo: true, Locks: LockMachine, Destructive: true, OverMCP: true},
	{Name: "vm-delete", Summary: "remove the VM", IsUndo: true, Locks: LockVM, Destructive: true, OverMCP: true},
	{Name: "app-delete", Summary: "remove your .exe from the VM", IsUndo: true, Locks: LockVM | LockStage, Destructive: true, OverMCP: true},
	{Name: "vm-golden-delete", Summary: "remove the golden image", IsUndo: true, Locks: LockMachine, Destructive: true, OverMCP: true},
	// Not an undo of one command: it removes whatever clones callers left
	// behind. Dry run unless -force, like every destructive command.
	{Name: "vm-reap", Summary: "remove clones idle past their lease; never irgo-win11 or the golden image", Locks: LockEachVM, Destructive: true, OverMCP: true},

	{Name: "vm-screen", Summary: "photograph the VM, for when it is stuck", ReadOnly: true, OverMCP: true},
	{Name: "vm-repair", Summary: "fix an expired password and a stale WebView2 registration, as SYSTEM", Locks: LockVM, OverMCP: true},
	{Name: "doctor", Summary: "what is here, and where the log and screenshots are", ReadOnly: true, OverMCP: true},
	// report gathers what an issue needs, redacted, for pasting into one.
	{Name: "report", Summary: "a redacted, paste-ready diagnostic block for an issue: versions, doctor, the last errors, glaze", ReadOnly: true, OverMCP: true},
	{Name: "status", Summary: "every VM with its owner and last use, and long-running work: what is going, what finished", ReadOnly: true, OverMCP: true},
	// glaze-check and glaze-status work only in a checkout of this repository.
	// glaze-check takes no lock here: the Mac run touches no VM, and a lock
	// would block it for the whole of an install. -windows takes that VM's lock
	// itself, and takes a minute and a half or more, so over MCP it is a job.
	{Name: "glaze-check", Summary: "does glaze work? its conformance suite, here or -windows (needs the source checkout)", Detach: "-windows", OverMCP: true},
	{Name: "glaze-status", Summary: "the recorded glaze verdict and whether it still holds (needs the source checkout)", ReadOnly: true, OverMCP: true},
	// vm-check and vm-status work only in a checkout too: the suite is
	// examples/vmconformance. vm-check runs it in the VM, as SYSTEM and in the
	// desktop session, changing nothing there; a minute or two, so a job over
	// MCP.
	{Name: "vm-check", Summary: "does the VM have what this project relies on? run the VM suite in it, record every check", Locks: LockVM, Detach: DetachAlways, OverMCP: true},
	{Name: "vm-status", Summary: "the recorded VM verdicts, one per VM, and how far each still holds", ReadOnly: true, OverMCP: true},
	{Name: "help", Summary: "the three steps explained, and what your .exe has to be", ReadOnly: true},
	{Name: "version", Summary: "what this binary is", ReadOnly: true},
	{Name: "commands", Summary: "one command name per line, for tooling", ReadOnly: true},
	{Name: "mcp", Summary: "serve these commands to an agent over MCP, on stdin and stdout"},
}

// DetachedBy reports whether args include c's Detach flag, in any of the forms
// the flag package accepts (-install, -install=true), or c is DetachAlways.
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
// All. The columns are sized from the list, so a long name does not push its
// own row out of line.
func UsageText() string {
	name, summary := 0, 0
	for _, c := range All {
		name = max(name, len(c.Name))
		if c.Undo != "" {
			summary = max(summary, len(c.Summary))
		}
	}
	var b strings.Builder
	b.WriteString("irgo-winvm — build a Go program on your Mac, run it on real Windows.\n\n")
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
	b.WriteString("\nNew here? Run `irgo-winvm doctor`: it says what to do next, in order.\n" +
		"`irgo-winvm help` explains the steps, and every command takes -h for its flags.\n")
	return b.String()
}
