package main

import (
	"flag"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/ledger"
)

// unrecorded are the commands the ledger is not told about: they touch no VM
// and say nothing about who is using what. mcp and serve are here because
// they serve for hours and their start would read as work never ended; every
// tool call mcp serves, and every job serve runs (remote.Record), is
// recorded on its own. glaze-status reads a committed file, and
// takes no flags, so `glaze-status -h` runs it: the site build did that a
// dozen times per build and every one reached the live ledger (measured
// 1 Oct 2026).
var unrecorded = map[string]bool{"mcp": true, "serve": true, "keeper": true, "help": true, "version": true, "commands": true, "glaze-status": true}

// recordCommand tells the ledger a command is starting, and returns what
// tells it how the command ended. client is the MCP client's name, or empty
// on the command line. Neither call waits on the network or can fail.
func recordCommand(client string, c command.Command, v values) func(error) {
	if unrecorded[c.Name] {
		return func(error) {}
	}
	op, vm, began := ledger.NewID(), vmOf(c, v), time.Now()
	ledger.Emit(ledger.Event{Type: ledger.Start, Op: op, Client: client, VM: vm, Command: c.Name})
	return func(err error) {
		code := int64(exitCode(err))
		took := time.Since(began).Milliseconds()
		e := ledger.Event{Type: ledger.End, Op: op, Client: client, VM: vm, Command: c.Name, Exit: &code, DurationMS: &took}
		if err != nil {
			e.Detail = err.Error() // redacted by the ledger before it is stored
		}
		ledger.Emit(e)
	}
}

// vmOf is the VM a command acts on, for the ledger: its -vm flag when the
// command takes a VM lock (so the default counts) or when -vm was given, and
// empty otherwise. glaze-check has a -vm that means nothing without
// -windows, and must not put a VM it never touched in the record.
func vmOf(c command.Command, v values) string {
	if v.fs == nil {
		return ""
	}
	f := v.fs.Lookup("vm")
	if f == nil {
		return ""
	}
	if c.Locks&command.LockVM != 0 {
		return f.Value.String()
	}
	set := false
	v.fs.Visit(func(g *flag.Flag) { set = set || g.Name == "vm" })
	if set {
		return f.Value.String()
	}
	return ""
}
