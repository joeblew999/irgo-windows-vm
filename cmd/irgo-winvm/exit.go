package main

import (
	"errors"
	"flag"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

var (
	// errUsage is the command being called wrongly, such as a missing
	// argument. The flag package reports malformed flags itself.
	errUsage = errors.New("usage")

	// errRefused is a destructive command declining to act without -force.
	errRefused = errors.New("refused without -force")
)

// exitCode maps an error to the process exit status. The codes and their
// meanings are declared in package command, which the MCP server shares.
//
// It matches sentinels, never message text: messages are for people and get
// reworded.
func exitCode(err error) command.Code {
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return command.CodeOK
	case errors.Is(err, errRefused):
		return command.CodeNeedForce
	case errors.Is(err, utmvm.ErrNoVM):
		return command.CodeNoVM
	case errors.Is(err, utmvm.ErrNoAgent):
		return command.CodeNoAgent
	case errors.Is(err, utmvm.ErrMutationInProgress):
		return command.CodeBusy
	case errors.Is(err, errUsage):
		return command.CodeUsage
	default:
		return command.CodeFailed
	}
}
