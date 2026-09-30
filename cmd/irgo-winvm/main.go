// Command irgo-winvm brings up a Windows 11 ARM64 VM on Apple Silicon so an
// irgo desktop build can be tested on the machine that produced it.
//
// Everything is plain Go: no hdiutil, no plutil, no shell. Run it with no
// arguments for the list of commands, or `irgo-winvm help` for the walkthrough.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// version is set at build time by .goreleaser.yaml: the tag for a release,
// "dev" otherwise.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(int(exitCode(err)))
	}
}

// run is the command line: a bare invocation prints the usage on stdout and
// succeeds, an unknown command prints it on stderr and fails, and -h is
// answered without an error.
func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(command.UsageText())
		return nil
	}
	if _, ok := find(args[0]); !ok {
		fmt.Fprint(os.Stderr, command.UsageText())
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
	if err := runTool(args[0], args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		return err
	}
	return nil
}

// runTool is the one path every command takes: the CLI, an MCP tool call, and
// the detached job child, which re-runs this binary. A command that changes
// state on disk takes its mutation locks after its flags parse, so -h is
// answered even while another mutation holds them; a second mutation is
// refused, never queued.
func runTool(name string, args []string) error {
	c, ok := find(name)
	if !ok {
		return fmt.Errorf("%w: no such command %q", errUsage, name)
	}
	v, rest, err := c.parse(args)
	if err != nil {
		return err
	}
	if c.Mutates() {
		release, err := utmvm.Acquire(locksFor(c.Command, v)...)
		if err != nil {
			return err
		}
		defer release()
	}
	return c.run(v, rest)
}

// locksFor is the locks c takes with these parsed flags: LockVM becomes the
// lock of the VM its -vm flag names, or of the default VM when it has no -vm
// or it is empty, so `-vm a1`, `-vm=A1` and a UUID all land on one lock.
func locksFor(c command.Command, v values) []utmvm.Lock {
	var locks []utmvm.Lock
	if c.Locks&command.LockMachine != 0 {
		locks = append(locks, utmvm.MachineLock)
	}
	if c.Locks&command.LockStage != 0 {
		locks = append(locks, utmvm.StageLock)
	}
	if c.Locks&command.LockVM != 0 {
		vm := utmvm.DefaultVMName
		if v.fs != nil {
			if f := v.fs.Lookup("vm"); f != nil && f.Value.String() != "" {
				vm = f.Value.String()
			}
		}
		locks = append(locks, utmvm.VMLockFor(vm))
	}
	return locks
}

// cmd is a declared command joined to what runs it.
//
// What a command is (name, summary, whether it mutates) is declared in package
// command, because the MCP server imports that list. How it runs is here.
type cmd struct {
	command.Command
	impl
}

// impl is how a command runs.
type impl struct {
	// flags declares the command's flags. Nil means it takes none, and its
	// arguments reach run unparsed: `version -h` prints the version.
	flags func() *flag.FlagSet
	// about, if set, replaces the "Usage of" preamble in -h output.
	about string
	// run performs the command, given its parsed flags and the remaining
	// arguments.
	run func(v values, args []string) error
}

// parse reads c's flags from args and returns them with what is left over.
func (c cmd) parse(args []string) (values, []string, error) {
	if c.flags == nil {
		return values{}, args, nil
	}
	fs := c.flags()
	if c.about != "" {
		fs.Usage = func() {
			_, _ = fmt.Fprintf(fs.Output(), "Usage of %s:\n%s", fs.Name(), c.about)
			fs.PrintDefaults()
		}
	}
	if err := fs.Parse(args); err != nil {
		return values{}, nil, err
	}
	return values{fs}, fs.Args(), nil
}

// commands is command.All, in its order, each joined to its implementation.
// Built in init because the table refers to runMCP, which reaches back here
// through runTool, and Go rejects that as an initialization cycle.
var commands []cmd

func init() {
	impls := map[string]impl{
		"iso-create": {flags: isoCreateFlags, run: runISOCreate},
		"vm-create":  {flags: vmCreateFlags, run: runVMCreate},
		"app-create": {flags: appCreateFlags, run: runAppCreate},
		"app-upload": {flags: appUploadFlags, run: runAppUpload},

		"iso-delete": {flags: isoDeleteFlags, run: runISODelete},
		"vm-delete":  {flags: vmDeleteFlags, run: runVMDelete},
		"app-delete": {flags: appDeleteFlags, run: runAppDelete},

		"vm-golden-create": {flags: vmGoldenCreateFlags, run: runVMGoldenCreate},
		"vm-golden-delete": {flags: vmGoldenDeleteFlags, run: runVMGoldenDelete},

		"vm-screen":    {flags: vmScreenFlags, run: runVMScreen},
		"vm-repair":    {flags: vmRepairFlags, run: runVMRepair},
		"doctor":       {flags: doctorFlags, run: runDoctor},
		"status":       {flags: statusFlags, about: statusAbout, run: runStatus},
		"glaze-check":  {flags: glazeCheckFlags, run: runGlazeCheck},
		"glaze-status": {run: runGlazeStatus},
		"help":         {run: runHelp},
		"version":      {run: runVersion},
		"commands":     {run: runCommands},
		"mcp":          {flags: mcpFlags, about: mcpAbout, run: runMCP},
	}
	var err error
	if commands, err = join(command.All, impls); err != nil {
		panic(err)
	}
}

// join pairs every declared command with its implementation, and fails if
// either side names something the other does not.
func join(all []command.Command, impls map[string]impl) ([]cmd, error) {
	out := make([]cmd, 0, len(all))
	declared := make(map[string]bool, len(all))
	for _, c := range all {
		im, ok := impls[c.Name]
		if !ok || im.run == nil {
			return nil, fmt.Errorf("command %q is declared but has no implementation", c.Name)
		}
		out = append(out, cmd{c, im})
		declared[c.Name] = true
	}
	for name := range impls {
		if !declared[name] {
			return nil, fmt.Errorf("%q is implemented but not declared in package command", name)
		}
	}
	return out, nil
}

// find returns the command by name. -h and --help are spellings of help on a
// command line, so they are resolved here rather than declared.
func find(name string) (cmd, bool) {
	if name == "-h" || name == "--help" {
		name = "help"
	}
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
	}
	return cmd{}, false
}
