// Command irgo-winvm brings up a Windows 11 ARM64 VM on Apple Silicon so an
// irgo desktop build can be tested on the machine that produced it.
//
// It exists because the GUI path is a lot of clicking that cannot be scripted,
// reviewed or run twice the same way. Everything here is plain Go: no hdiutil,
// no plutil, no shell.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/command"
	"github.com/joeblew999/irgo-windows-vm/mcpserver"
	"github.com/joeblew999/irgo-windows-vm/utmvm"
)

// version is set at build time by go:build. 'dev' when built by hand.
var version = "dev"

// What this process exits with, and why each one is worth telling apart.
//
// The numbers and their meanings are declared in package command, because the
// MCP server reports the same classification to an agent and the two must not
// disagree about which one is worth retrying. What stays here is the mapping
// from *this program's errors* to those codes, which is where the sentinels
// live.
//
// Everything used to exit 1. So a script could not distinguish "the program I
// asked you to run failed" from "that VM does not exist" from "the guest agent
// is busy" — and the last of those is the one worth retrying, since Windows
// Update takes the agent away for minutes at a time.
//
// It matters more here than in most tools because `utmctl` itself exits 0 on
// failure, documented in AGENTS.md. This CLI is the only honest signal a caller
// gets, so it had better say something.

// errRefused is a destructive command declining to act without -force.
//
// Not a failure of the machine — the tool did exactly what it should — but a
// caller scripting a teardown needs to tell it from one, and both -force
// refusals are raised here in the CLI rather than in utmvm.
var errRefused = errors.New("refused without -force")

// exitCode classifies an error for the shell.
//
// Sentinels, not string matching: these messages are written for people and get
// reworded, and a classification that breaks silently when a sentence changes
// is worse than none.
func exitCode(err error) command.Code {
	switch {
	case err == nil:
		return command.CodeOK
	case errors.Is(err, flag.ErrHelp):
		return command.CodeOK
	case errors.Is(err, errRefused):
		return command.CodeNeedForce
	case errors.Is(err, utmvm.ErrNoVM):
		return command.CodeNoVM
	case errors.Is(err, utmvm.ErrNoAgent):
		return command.CodeNoAgent
	case errors.Is(err, errUsage):
		return command.CodeUsage
	default:
		return command.CodeFailed
	}
}

// errUsage is the command being called wrongly — a missing argument, rather
// than a malformed flag, which the flag package catches itself.
var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(int(exitCode(err)))
	}
}

// cmd pairs a declared command with the function that performs it.
//
// The declaration lives in package command, which the MCP server imports; the
// handler lives here, because running a command is this program's job and
// nothing else's. Nothing enumerates command names twice: `commands` below is
// built by walking command.All, and `handlers` is keyed by the names that list
// already declares.
type cmd struct {
	command.Command
	Run func(args []string) error
}

// Assigned in init, not at declaration: the table contains runCommands, which
// reads the table, and Go rejects that as an initialization cycle.
var commands []cmd

// handlers is the wiring: one entry per declared command, keyed by its name.
//
// Package level rather than a local in init, so the test that checks it against
// command.All can see it. There is no initialization cycle: it refers to
// runCommands, which reads `commands`, and `commands` has no initializer — it
// is filled in by init below.
var handlers = map[string]func([]string) error{
	"iso-create": runISOCreate,
	"vm-create":  runVMCreate,
	"app-create": runAppCreate,

	"iso-delete": runISODelete,
	"vm-delete":  runVMDelete,
	"app-delete": runAppDelete,

	"vm-screen": runVMScreen,
	"doctor":    runDoctor,
	"help":      runHelp,
	"version":   runVersion,
	"commands":  runCommands,
	"mcp":       runMCP,
}

func init() {
	// Walked in the list's order, so the usage and `irgo-winvm commands` print
	// what package command declares rather than what a map happened to iterate.
	//
	// Both directions are gated: a declared command with no handler leaves Run
	// nil, which TestEveryCommandIsReachable fails on, and a handler naming nothing
	// declared is caught by TestEveryHandlerIsADeclaredCommand. Half a check
	// leaves half the drift invisible.
	for _, c := range command.All {
		commands = append(commands, cmd{Command: c, Run: handlers[c.Name]})
	}
}

// find returns the command by name. Aliases for help are accepted here rather
// than in the list, because they are spellings on a command line, not commands.
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

// usage goes to stderr, for the case where the user got it wrong. The same
// text on stdout is what a bare `irgo-winvm` prints.
func usage() { fmt.Fprint(os.Stderr, usageText()) }

// usageText is generated from the list, so it cannot name a command that does
// not exist or omit one that does.
func usageText() string { return command.UsageText() }

func runVersion([]string) error { fmt.Println(version); return nil }

// screenshotForMCP runs vm-screen and hands back the PNG itself.
//
// It runs the same handler the CLI runs — same flags, same VM resolution, same
// errors — rather than reimplementing it, which is the rule mcpserver is built
// on. What it adds is knowing where the file went, because the CLI only says so
// in prose and prose is not an interface.
//
// The file is read and removed. An agent cannot open a path, and over a remote
// transport the path is on a machine it has no access to; the bytes are the
// answer. A caller that passes its own -o keeps the file, because then it asked
// for one on purpose.
func screenshotForMCP(_ context.Context, args []string) ([]byte, string, error) {
	// -promote publishes shots already taken and photographs nothing, so there
	// is no image to return and pretending otherwise would be a lie.
	for _, a := range args {
		if a == "-promote" || strings.HasPrefix(a, "-promote=") {
			out, err := utmvm.Capture(func() error { return runVMScreen(args) })
			return nil, out, err
		}
	}

	dst, keep := explicitOutput(args)
	if dst == "" {
		f, err := os.CreateTemp("", "irgo-mcp-shot-*.png")
		if err != nil {
			return nil, "", err
		}
		dst = f.Name()
		_ = f.Close()
		args = append(append([]string{}, args...), "-o", dst)
	}
	if !keep {
		defer func() { _ = os.Remove(dst) }()
	}

	out, err := utmvm.Capture(func() error { return runVMScreen(args) })
	if err != nil {
		return nil, out, err
	}
	png, err := os.ReadFile(dst)
	if err != nil {
		// The command reported success and produced no file. Saying so is the
		// whole point of this repository's rule about checking.
		return nil, out, fmt.Errorf("vm-screen reported success but wrote no readable PNG at %s: %w", dst, err)
	}
	return png, out, nil
}

// explicitOutput reports the -o the caller passed, if any.
func explicitOutput(args []string) (path string, given bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "-o="); ok {
			return v, true
		}
		if a == "-o" && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// runMCP serves the commands to an agent.
//
// The handler each tool calls is the same function this program dispatches to,
// through the same table — so a tool cannot do anything the CLI cannot, and
// cannot do it differently. That is the rule mcpserver's doc comment states,
// and this closure is where it is actually enforced.
//
// utmvm.Capture is not optional here. stdout is the JSON-RPC channel, and every
// command announces its progress; without the capture the first vm-create would
// write status lines into the middle of the protocol stream. The captured text
// becomes the tool's result, which is where an agent needs it anyway.
func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage of mcp:\n"+
			"  Serves the commands above to an agent over the Model Context Protocol,\n"+
			"  on stdin and stdout. It takes no flags: a client spawns it and speaks\n"+
			"  JSON-RPC. Nothing else should be written to stdout while it runs.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	return mcpserver.Serve(context.Background(), mcpserver.Deps{
		Version:    version,
		Classify:   exitCode,
		Screenshot: screenshotForMCP,
		Run: func(_ context.Context, name string, args []string) (string, error) {
			c, ok := find(name)
			if !ok || c.Run == nil {
				return "", fmt.Errorf("%w: no such command %q", errUsage, name)
			}
			return utmvm.Capture(func() error { return c.Run(args) })
		},
	})
}

// runCommands prints one name per line.
//
// This is the interface the site's flag reference and the CI documentation
// check both read. They could have scraped the usage text instead, and that is
// exactly the fragility worth avoiding: a layout change would silently drop a
// command from the reference, which is the failure this whole exercise is
// about.
func runCommands([]string) error {
	for _, c := range commands {
		fmt.Println(c.Name)
	}
	return nil
}

// runHelp is the explanation. Separate from usage because a person who typed
// the command wrong wants the list, and a person who typed `help` wants the
// story — and putting the story in front of the first group buries the list
// they were looking for.
func runHelp([]string) error {
	// The download size is asked for, not typed in. It is a constant in utmvm
	// that iso-create's own -fetch usage already reports, and this text carried
	// a second hand-written copy of it — the same number in two places, which is
	// how one of them ends up stale.
	fmt.Printf(`irgo-winvm — build a Go program on your Mac, run it on real Windows.

Three steps, in this order. Each one is cheap to repeat: if it is already
done, it says so and stops.

  1  iso-create   get the Windows installer (%s from Microsoft, or
                  built locally from an .esd you already have)
  2  vm-create    make a VM and install Windows on it (about 45 minutes,
                  unattended — you do not click anything)
  3  app-create   push your .exe into that VM, run it, print what it said

Undo, in the same shape:

     iso-delete   remove the installer
     vm-delete    remove the VM
     app-delete   remove your .exe from the VM

When something is wrong:

     vm-screen    save a PNG of the VM's screen — the only way to see a
                  boot that is stuck, since it looks identical from here
     doctor       what is installed, what is missing, and where this run
                  wrote its log and screenshots

Your .exe is anything you built with GOOS=windows GOARCH=arm64. The probes
in probe/ and glaze-probes/ are examples of that, and what this repository
uses to find out what breaks in glaze and native on Windows.

Every command takes -h for its flags.
`, utmvm.ISODownloadSize())
	return nil
}

func run(args []string) error {
	// No arguments is somebody asking what this is, not an error. It prints the
	// list on stdout and exits 0, so `irgo-winvm | head` works and a shell
	// script does not see a failure for asking.
	if len(args) == 0 {
		fmt.Print(usageText())
		return nil
	}
	c, ok := find(args[0])
	if !ok {
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}

	// -h is a request, not a failure.
	//
	// The flag package returns ErrHelp from Parse when it has already printed
	// the usage, and every command here passed that straight out as an error.
	// So asking for help printed the flags and then `error: flag: help
	// requested`, and exited 1 — on vm-create, vm-delete, app-create and
	// vm-screen, which used ContinueOnError, while the other three used
	// ExitOnError and exited 0. Two error modes, no principle, and the same
	// question answered two ways depending on which command you asked.
	//
	// One mode everywhere now, and ErrHelp stops here.
	if err := c.Run(args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		return err
	}
	return nil
}

// runVMCreate is the one command a new developer runs.
//
// Everything it does was already possible as eight separate calls in an order
// you had to know, with a UTM restart in the middle that nobody discovers
// alone. Every stage is idempotent, so running it twice is safe and the second
// run takes seconds — which matters because the two expensive stages are a
// 4.2 GB download and a 45-minute install.
func runVMCreate(args []string) error {
	fs := flag.NewFlagSet("vm-create", flag.ContinueOnError)
	var (
		name    = fs.String("vm", utmvm.DefaultVMName, "VM name")
		install = fs.Bool("install", false, "run the unattended Windows install (about 45 minutes)")
		timeout = fs.Duration("timeout", 60*time.Minute, "overall limit for the install")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := utmvm.Printer("vm-create")

	// Same shape as iso-create: name every location first, then narrate each
	// step before doing it. This is the command that can take 45 minutes, so
	// silence here is the difference between waiting and wondering.
	b, _ := utmvm.BundlePath(*name)
	say("vm:     %s", *name)
	say("bundle: %s", utmvm.Home(b))
	say("media:  %s", utmvm.Home(utmvm.ISODir()))

	res, err := utmvm.VMCreate(utmvm.VMCreateOptions{
		VMName:  *name,
		Install: *install,
		Timeout: *timeout,
	}, func(line string) { say("%s", line) })
	if err != nil {
		return err
	}
	if res.Ready {
		say("%s is ready", res.VM)
		return nil
	}
	say("%s is not ready yet — see the steps above for what remains", res.VM)
	return nil
}

func runDoctor([]string) error {
	out := utmvm.Reporter("doctor")

	// One table. It was five formats -- prose, a table, another prose block, a
	// "N missing" list repeating what the table already said, and a records
	// section -- so the same fact appeared twice in different words and the
	// thing you were looking for was never where you looked.
	type row struct{ what, state, where string }
	var rows []row
	add := func(what, state, where string) { rows = append(rows, row{what, state, where}) }

	// The tool first, because it is the one thing in this table doctor cannot
	// be wrong about and the first thing a bug report needs. It was the only
	// thing missing from it: `version` printed the number and nothing else did,
	// so doctor output pasted into an issue did not say what produced it —
	// and "dev" versus a tag is the difference between a build somebody made
	// and one that shipped.
	//
	// Built here rather than in utmvm, which cannot see main.version.
	self, err := os.Executable()
	if err != nil {
		self = "(this binary)"
	}
	add("irgo-winvm", version, utmvm.Home(self))

	for _, e := range utmvm.Externals() {
		state := "MISSING"
		if e.Path != "" {
			if _, sErr := os.Stat(e.Path); sErr == nil {
				state = "ok"
				if e.Bytes > 0 {
					state = utmvm.HumanBytes(e.Bytes)
				}
			}
		}
		add(e.Name, state, utmvm.Home(e.Path))
	}

	for _, t := range utmvm.ISOTools() {
		state := "MISSING"
		if t.Found() {
			state = "ok"
		}
		add(t.Name, state, utmvm.Home(t.Where()))
	}

	for _, r := range utmvm.Records() {
		state := "not yet"
		if fi, sErr := os.Stat(r.Path); sErr == nil {
			state = "written"
			if !r.Dir {
				state = utmvm.HumanBytes(fi.Size())
			}
		}
		add(r.Name, state, utmvm.Home(r.Path))
	}

	out("%-22s %-10s %s", "WHAT", "STATE", "WHERE")
	var missing int
	for _, r := range rows {
		out("%-22s %-10s %s", r.what, r.state, r.where)
		if r.state == "MISSING" {
			missing++
		}
	}
	out("")
	if missing == 0 {
		out("nothing missing.")
		return nil
	}
	// What to run, once, rather than a paragraph per missing thing.
	out("%d missing. In order: irgo-winvm iso-create -fetch, then vm-create -install.", missing)
	return nil
}

func runVMDelete(args []string) error {
	fs := flag.NewFlagSet("vm-delete", flag.ContinueOnError)
	name := fs.String("vm", utmvm.DefaultVMName, "VM name")
	force := fs.Bool("force", false, "actually delete; without this it only lists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := utmvm.Printer("vm-delete")

	b, _ := utmvm.BundlePath(*name)
	say("STEP 1/2  the VM")
	say("          %s", utmvm.Home(b))

	e, fErr := utmvm.Find(*name)
	if fErr != nil {
		say("          UTM knows no VM %q; nothing to delete", *name)
		return nil
	}
	r, iErr := utmvm.InspectRemoval(*name)
	if iErr != nil {
		return iErr
	}
	say("          %s, %s", e.Status, utmvm.HumanBytes(r.TotalBytes))

	say("STEP 2/2  would delete:")
	say("          %-9s %s", utmvm.HumanBytes(r.TotalBytes), utmvm.Home(r.Path))
	if r.Running {
		say("          it is running and will be stopped first")
	}
	if !*force {
		return fmt.Errorf("%s of VM, and the Windows on it.\n"+
			"  Reinstalling takes about 45 minutes. Pass -force to do it (%w)",
			utmvm.HumanBytes(r.TotalBytes), errRefused)
	}

	say("STEP 2/2  deleting")
	out, err := utmvm.Delete(*name, true, func(f string, a ...any) { say("          "+f, a...) })
	if err != nil {
		return err
	}
	say("removed %s — %s reclaimed", utmvm.Home(out.Path), utmvm.HumanBytes(out.TotalBytes))
	return nil
}

// runAppCreate is the inner loop: build on the Mac, run on Windows, read output back.
func runAppCreate(args []string) error {
	say := utmvm.Printer("app-create")
	fs := flag.NewFlagSet("app-create", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to allow the guest command")
	name := fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	gui := fs.Bool("gui", false, "run on the guest's desktop (required for anything with a window)")
	user := fs.String("user", "dev", "guest account for -gui")
	detach := fs.Bool("detach", false, "leave it running and return, instead of waiting for it to exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() == 0 {
		return fmt.Errorf("%w: irgo-winvm app-create -vm <name> <local.exe> [args...]", errUsage)
	}
	e, err := utmvm.Find(*name)
	if err != nil {
		return err
	}
	vm := utmvm.Named(e.UUID)

	// Recover the VM if it is not answering. A Windows guest reboots on its own
	// — Windows Update does it — and lands back in the UEFI shell, so a run
	// cannot assume the VM is still reachable just because it was earlier.
	if !vm.AgentReady() {
		say("VM not answering; recovering")
		if err := utmvm.EnsureReady(e.UUID, bundleOf(e), 10*time.Minute, say); err != nil {
			return err
		}
	}

	local := fs.Arg(0)
	say("vm:     %s", e.Name)
	say("binary: %s", local)
	res, err := utmvm.AppCreate(e.UUID, local, utmvm.AppOptions{
		Args:    fs.Args()[1:],
		GUI:     *gui,
		User:    *user,
		Detach:  *detach,
		Timeout: *timeout,
		// The printer goes in, so the push, the launch and the wait are all
		// visible and all logged. Without it the library half was silent and a
		// run that hung recorded one line, "started", and nothing else.
		Say: say,
	})
	if res.Stdout != "" {
		// Through the printer: what the guest printed is the result of the
		// whole three-stage chain, and a log that records every step except
		// the answer is missing the part somebody comes back for.
		for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
			say("%s", line)
		}
	}
	if err != nil {
		return err
	}
	// Named here rather than in utmvm, which only ever sees the UUID: every
	// command resolves the name to one before calling in, so a hint printed
	// from in there tells you to run `-vm 38791348-ED91-...`.
	if *detach {
		say("watch it with:    irgo-winvm vm-screen -vm %s", e.Name)
		say("take it off with: irgo-winvm app-delete -vm %s %s", e.Name, filepath.Base(local))
		return nil
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s exited %d in the guest", filepath.Base(local), res.ExitCode)
	}
	return nil
}

// bundleOf is the CLI's one route to a VM's bundle. The layout belongs to
// utmvm; every command used to rebuild the path itself.
func bundleOf(e utmvm.Entry) string {
	p, err := utmvm.BundlePath(e.Name)
	if err != nil {
		return ""
	}
	return p
}

// runAppDelete removes what `app-create` put on the guest: the binaries it pushed and
// any scratch files a run that did not finish left behind.
func runAppDelete(args []string) error {
	say := utmvm.Printer("app-delete")
	fs := flag.NewFlagSet("app-delete", flag.ContinueOnError)
	name := fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Only reachable via an explicit `-vm ""`, since the flag defaults now.
	if *name == "" {
		return fmt.Errorf("app-delete: -vm was given an empty name")
	}
	say("vm:     %s", *name)
	say("guest:  %s and %s", `C:\Windows\Temp`, `C:\Users\Public`)
	e, err := utmvm.Find(*name)
	if err != nil {
		// No VM means nothing was ever put on it. An undo that fails when
		// there is nothing to undo cannot be run twice.
		say("UTM knows no VM %q; nothing to delete", *name)
		return nil
	}
	if err := utmvm.AppDelete(e.UUID, func(f string, a ...any) { say("  "+f, a...) }, fs.Args()...); err != nil {
		return err
	}
	say("cleaned %s", e.Name)
	return nil
}

// runISOCreate gets the Windows media, which is the slowest and most rate-limited
// step in `vm`. Separate so it can be done once and kept.
func runISOCreate(args []string) error {
	fs := flag.NewFlagSet("iso-create", flag.ContinueOnError)
	fetch := fs.Bool("fetch", false, "download from Microsoft ("+utmvm.ISODownloadSize()+") if nothing local works")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Elapsed time on every line. A multi-gigabyte download and an ISO build take
	// minutes, and without this there is no way to tell a slow step from a
	// stuck one — which is how a 77-second cached-media check went unnoticed.
	say := utmvm.Printer("iso-create")
	say("media:  %s", utmvm.Home(utmvm.ISODir()))

	say("STEP 0/4  the two programs an ISO build needs")
	// The two external programs building an ISO needs. Installed here, and
	// removed by iso-delete — what `iso` puts on the machine, `iso-delete`
	// takes off. Every path printed, because "installed" without a location
	// cannot be checked and cannot be undone by hand.
	for _, t := range utmvm.ISOTools() {
		say("tool:   %-16s %s", t.Name, utmvm.Home(t.Where()))
		if t.Found() {
			continue
		}
		if err := t.Ensure(); err != nil {
			return err
		}
		say("  ✓ %-16s installed at %s", t.Name, utmvm.Home(t.Path))
	}

	// No UTM, no guest tools, no VM. Getting Windows media is a download or an
	// ESD expansion; a hypervisor is not involved, and this used to run the
	// whole setup chain — so fetching an ISO required UTM to be installed first.
	iso, detail, skipped, err := utmvm.ISOGet(utmvm.ISOGetOptions{Fetch: *fetch}, say)
	if err != nil {
		return err
	}
	if skipped {
		say("media: %s (already there — %s)", utmvm.Home(iso), detail)
		return nil
	}
	say("media: %s (%s)", utmvm.Home(iso), detail)
	return nil
}

// runISODelete removes the media, which is protected on purpose, so it says so
// rather than failing with EPERM.
func runISODelete(args []string) error {
	fs := flag.NewFlagSet("iso-delete", flag.ContinueOnError)
	force := fs.Bool("force", false, "actually delete; without this it only lists")
	all := fs.Bool("all", false, "also delete the .esd, the one thing that cannot be rebuilt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := utmvm.Printer("iso-delete")

	// Everything that would go, listed as what it is — things about to be
	// deleted, not an inventory. The earlier version printed the same lines
	// under "media:" and "tool:" and then refused, which read as a status
	// report followed by an unrelated complaint.
	say("STEP 1/3  media in %s", utmvm.Home(utmvm.ISODir()))
	var files []string
	var bytes int64
	wanted := utmvm.ISODerived()
	if *all {
		wanted = utmvm.ISOFiles()
	}
	for _, f := range wanted {
		fi, err := os.Stat(f)
		if err != nil {
			continue // absent files are not news; the directory is named above
		}
		files = append(files, f)
		// Sidecars go with the file they describe rather than as entries of
		// their own: 32 bytes of cached scan result each, and listing them
		// separately made a two-file directory look like four.
		if strings.HasSuffix(f, ".scan") {
			continue
		}
		say("          %-9s %s", utmvm.HumanBytes(fi.Size()), filepath.Base(f))
		bytes += fi.Size()
	}
	if len(files) == 0 {
		say("          none")
	}
	// The .esd is reported even when it is not being deleted: "what is on this
	// machine" is the question, and a silent 4.2 GB is the answer nobody
	// expects.
	if !*all {
		if fi, err := os.Stat(utmvm.ISOSourcePath()); err == nil {
			say("          %-9s %s  (kept — pass -all to delete it)",
				utmvm.HumanBytes(fi.Size()), filepath.Base(utmvm.ISOSourcePath()))
		}
	}
	say("STEP 2/3  looking for the tools iso installed")
	tools := utmvm.ISOTools()
	var installed []int
	for i := range tools {
		if tools[i].Found() {
			say("          found %-16s %s", tools[i].Name, utmvm.Home(tools[i].Path))
			installed = append(installed, i)
			continue
		}
		say("          not installed: %s", tools[i].Name)
	}

	if len(files) == 0 && len(installed) == 0 {
		say("nothing to delete")
		say("  media would be at %s", utmvm.Home(utmvm.ISODir()))
		for i := range tools {
			say("  %s would be at %s", tools[i].Name, utmvm.Home(tools[i].Where()))
		}
		return nil
	}

	say("STEP 3/3  nothing deleted yet — this is what would go:")
	for _, f := range files {
		if strings.HasSuffix(f, ".scan") {
			continue
		}
		fi, _ := os.Stat(f)
		say("          %-9s %s", utmvm.HumanBytes(fi.Size()), filepath.Base(f))
	}
	for _, i := range installed {
		say("          %-9s %s (uninstalls %s)", "tool", utmvm.Home(tools[i].Path), tools[i].Formula)
	}

	if !*force {
		var what []string
		if len(files) > 0 {
			// Sidecars are not counted: they are 32 bytes of cache and saying
			// "2 files" for one ISO plus its scan result is just wrong.
			n := 0
			for _, f := range files {
				if !strings.HasSuffix(f, ".scan") {
					n++
				}
			}
			what = append(what, fmt.Sprintf("%d file(s), %s", n, utmvm.HumanBytes(bytes)))
		}
		if len(installed) > 0 {
			what = append(what, fmt.Sprintf("%d tool(s)", len(installed)))
		}
		msg := strings.Join(what, " and ")
		if len(files) > 0 {
			if *all {
				msg += "\n  Includes the .esd: " + utmvm.ISODownloadSize() + " to re-fetch from a source that rate-limits."
			} else {
				// 40s, measured — see RESULTS.md. It said "about three minutes"
				// from before the figure was taken.
				msg += "\n  The .esd is kept, so iso-create rebuilds this in about 40s with\n" +
					"  no network. Add -all to delete that too."
			}
		}
		return fmt.Errorf("%s\n  Pass -force to do it (%w)", msg, errRefused)
	}

	say("STEP 3/3  deleting")
	for _, f := range files {
		say("          clearing the immutable flag on %s", utmvm.Home(f))
		_ = utmvm.ISOUnprotect(f) // uchg blocks unlink
		if err := os.Remove(f); err != nil {
			return err
		}
		say("  · deleted %s", utmvm.Home(f))
	}
	// The tools go whether or not the media was there: an undo has to run to
	// completion from any starting point.
	for i := range tools {
		if tools[i].Found() {
			say("          uninstalling %s (brew uninstall %s)", tools[i].Name, tools[i].Formula)
		}
		where, err := tools[i].Remove()
		switch {
		case err != nil:
			say("          · %s left in place: %v", tools[i].Name, err)
		case where != "":
			say("          · uninstalled %s from %s", tools[i].Name, utmvm.Home(where))
		}
	}
	return nil
}

// runVMScreen photographs the guest's display.
//
// The only thing that answers "what is it actually doing" when a VM is stuck:
// a failed boot leaves a UEFI prompt nobody sees, and a stalled install looks
// exactly like a working one from the host.
func runVMScreen(args []string) error {
	fs := flag.NewFlagSet("vm-screen", flag.ContinueOnError)
	name := fs.String("vm", utmvm.DefaultVMName, "VM name")
	out := fs.String("o", "", "where to write the PNG (default: the shots directory)")
	promote := fs.String("promote", "", "copy the newest shot of each stage into this directory, named for the stage")
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := utmvm.Printer("vm-screen")

	// Promoting takes no new picture. It publishes the ones the run already
	// took, under stable names, because runtime shots are timestamped and
	// nothing in a README can reference a filename that changes every boot.
	if *promote != "" {
		stages, err := utmvm.Promote(*promote)
		if err != nil {
			return err
		}
		say("from:   %s", utmvm.Home(utmvm.ShotDir()))
		say("into:   %s", utmvm.Home(*promote))
		for _, s := range stages {
			say("  · %s.png", s)
		}
		say("%d stage(s) published", len(stages))
		return nil
	}

	// Resolved before photographing, only so a name that does not exist reports
	// the same way here as everywhere else.
	//
	// Without it this named a VM that is absent and a VM that is running
	// headless identically — "no UTM window titled ..." — and exited 1 where
	// app-create exits 3 for the same typo. A contract that holds for some
	// commands is not one.
	if _, err := utmvm.Find(*name); err != nil {
		return err
	}
	say("vm:     %s", *name)

	// Into shots/, outside the repository, and timestamped.
	//
	// This wrote docs/screens/<vm>.png, which is a tracked file: every
	// diagnostic screenshot overwrote committed evidence and left the working
	// tree dirty, and taking two in a row silently destroyed the first. Those
	// two directories are not the same thing — docs/screens is evidence chosen
	// to be kept, shots/ is every look at a running VM.
	if *out == "" {
		p, err := utmvm.Shot(*name, "vm-screen")
		if err != nil {
			return err
		}
		say("shot:   %s", utmvm.Home(p))
		say("written")
		return nil
	}
	say("shot:   %s", utmvm.Home(*out))
	if err := utmvm.Screenshot(*name, *out); err != nil {
		return err
	}
	say("written")
	return nil
}
