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
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/job"
	"github.com/joeblew999/irgo-windows-vm/internal/ledger"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

// version is set at build time by .goreleaser.yaml: the tag for a release,
// "dev" otherwise.
var version = "dev"

func init() { version = moduleVersion(version, debug.ReadBuildInfo) }

// moduleVersion is the tag `go install ...@v0.5.0` records in the binary, for
// a build GoReleaser did not stamp. A local build's VCS pseudo-version is not
// a release and stays "dev", as CONTRIBUTING.md says it does.
func moduleVersion(stamped string, read func() (*debug.BuildInfo, bool)) string {
	if stamped != "dev" {
		return stamped
	}
	bi, ok := read()
	if !ok || !releaseTag.MatchString(bi.Main.Version) {
		return stamped
	}
	return bi.Main.Version
}

// releaseTag is a plain vX.Y.Z: not a pseudo-version, not +dirty.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func main() {
	// The ledger (docs/ARCHITECTURE.md, "The ledger client"): off unless
	// IRGO_LEDGER_URL and IRGO_LEDGER_TOKEN are set. At exit it gets at most
	// 2 s to send; what it cannot send stays spooled for the next run.
	ledger.Configure(ledger.FromEnv(utmvm.Root(), version))
	err := run(os.Args[1:])
	ledger.DrainDefault(2 * time.Second)
	if err != nil {
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
	args = remoteSpelling(args)
	if _, ok := find(args[0]); !ok {
		fmt.Fprint(os.Stderr, command.UsageText())
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
	if err := runTool(args[0], args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		return err
	}
	return nil
}

// runTool is the command line, and the detached job child, which re-runs
// this binary: with no MCP client, except in a job child started over MCP,
// which is told its client's name (job.ClientEnv) so the ledger records it.
func runTool(name string, args []string) error { return runToolFor(job.Client(), name, args) }

// runToolFor is the one path every command takes: the CLI, an MCP tool call
// (mcpClient is the client's name from its initialize request), and the job
// child. After the flags parse it decides who is calling, refuses a caller
// other than the owner who would land on the owner's VM by default, takes the
// command's mutation locks and records the VM as used. So -h is answered even
// while another mutation holds the locks; a second mutation is refused, never
// queued.
func runToolFor(mcpClient, name string, args []string) (err error) {
	c, ok := find(name)
	if !ok {
		return fmt.Errorf("%w: no such command %q", errUsage, name)
	}
	if c.OverMCP && recordExits {
		// Recorded for `report`, which names the last commands and how they
		// ended. Before this, an error reached stderr and nowhere else.
		defer func() { logExit(utmvm.Logger(), name, args, err) }()
	}
	v, rest, err := c.parse(args)
	if err != nil {
		return err
	}
	v.caller = callerFor(v, mcpClient)
	// Recorded before admission, so a refusal is in the ledger too.
	ended := recordCommand(mcpClient, c.Command, v)
	defer func() { ended(err) }()
	if err := macOnly(c.Command, runtime.GOOS); err != nil {
		return err
	}
	if err := admit(c, v); err != nil {
		return err
	}
	if c.Mutates() {
		release, err := utmvm.Acquire(locksFor(c.Command, v)...)
		if err != nil {
			return err
		}
		defer release()
	}
	if vm, ok := usedVM(c, v); ok {
		// The lease starts again. A failure is said, not fatal: the command
		// itself is fine, and the worst outcome is a VM reaped early.
		if err := utmvm.TouchVM(vm); err != nil {
			_, _ = fmt.Fprintf(utmvm.Out, "warning: could not record %s as used: %v\n", vm, err)
		}
	}
	return c.run(v, rest)
}

// callerFor is who is running this command: its -owner flag, if it has one,
// else IRGO_WINVM_OWNER, else the MCP client, else the person at the terminal.
func callerFor(v values, mcpClient string) utmvm.Caller {
	owner := ""
	if v.fs != nil {
		if f := v.fs.Lookup("owner"); f != nil {
			owner = f.Value.String()
		}
	}
	return utmvm.CallerFromEnv(owner, mcpClient)
}

// vmFlag is the VM a command's -vm flag names, the default VM when it was
// left empty, and whether it was passed at all. ok is false for a command
// with no -vm flag, and for glaze-check without -windows, which touches no VM.
func vmFlag(c cmd, v values) (vm string, given, ok bool) {
	if v.fs == nil {
		return "", false, false
	}
	f := v.fs.Lookup("vm")
	if f == nil {
		return "", false, false
	}
	if c.Name == "glaze-check" && !v.Bool("windows") {
		return "", false, false
	}
	v.fs.Visit(func(set *flag.Flag) { given = given || set.Name == "vm" })
	vm = f.Value.String()
	if vm == "" {
		if f.DefValue == "" {
			// No default VM (vm-golden-create): the command asks for one.
			return "", given, false
		}
		vm = utmvm.DefaultVMName
	}
	return vm, given, true
}

// admit refuses a call before it takes a lock or touches UTM: a caller other
// than the owner who did not name a VM (utmvm.CheckVMChoice).
func admit(c cmd, v values) error {
	vm, given, ok := vmFlag(c, v)
	if !ok {
		return nil
	}
	return utmvm.CheckVMChoice(v.caller, vm, given)
}

// usedVM is the VM whose lease this command renews. vm-create records its own
// VM (utmvm.BeginCreate), and vm-delete removes the record instead.
func usedVM(c cmd, v values) (string, bool) {
	if c.Name == "vm-create" || c.Name == "vm-delete" {
		return "", false
	}
	vm, _, ok := vmFlag(c, v)
	return vm, ok
}

// locksFor is the locks c takes with these parsed flags: LockVM becomes the
// lock of the VM its -vm flag names, or of the default VM when it has no -vm
// or it is empty, so `-vm a1`, `-vm=A1` and a UUID all land on one lock, and
// LockStage the lock on the caller's own staged binaries.
func locksFor(c command.Command, v values) []utmvm.Lock {
	var locks []utmvm.Lock
	if c.Locks&command.LockMachine != 0 {
		locks = append(locks, utmvm.MachineLock)
	}
	if c.Locks&command.LockStage != 0 {
		locks = append(locks, utmvm.StageLockFor(v.caller.ID))
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
	return values{fs: fs}, fs.Args(), nil
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

		"vm-golden-push": {flags: vmGoldenPushFlags, run: runVMGoldenPush},
		"vm-golden-pull": {flags: vmGoldenPullFlags, run: runVMGoldenPull},

		"iso-delete": {flags: isoDeleteFlags, run: runISODelete},
		"vm-delete":  {flags: vmDeleteFlags, run: runVMDelete},
		"app-delete": {flags: appDeleteFlags, run: runAppDelete},

		"vm-ssh-create": {flags: vmSSHCreateFlags, run: runVMSSHCreate},
		"vm-ssh-delete": {flags: vmSSHDeleteFlags, run: runVMSSHDelete},

		"vm-golden-create": {flags: vmGoldenCreateFlags, run: runVMGoldenCreate},
		"vm-golden-delete": {flags: vmGoldenDeleteFlags, run: runVMGoldenDelete},
		"vm-reap":          {flags: vmReapFlags, run: runVMReap},
		"prune":            {flags: pruneFlags, about: pruneAbout, run: runPrune},

		"vm-screen":    {flags: vmScreenFlags, run: runVMScreen},
		"vm-repair":    {flags: vmRepairFlags, run: runVMRepair},
		"doctor":       {flags: doctorFlags, run: runDoctor},
		"capacity":     {flags: capacityFlags, run: runCapacity},
		"report":       {flags: reportFlags, about: reportAbout, run: runReport},
		"status":       {flags: statusFlags, about: statusAbout, run: runStatus},
		"glaze-check":  {flags: glazeCheckFlags, run: runGlazeCheck},
		"glaze-status": {run: runGlazeStatus},
		"vm-check":     {flags: vmCheckFlags, run: runVMCheck},
		"vm-status":    {flags: vmStatusFlags, run: runVMStatus},
		"help":         {run: runHelp},
		"version":      {run: runVersion},
		"commands":     {run: runCommands},
		"mcp":          {flags: mcpFlags, about: mcpAbout, run: runMCP},

		"remote-submit": {flags: remoteSubmitFlags, about: remoteSubmitAbout, run: runRemoteSubmit},
		"remote-cancel": {flags: remoteIDFlags("remote-cancel"), run: runRemoteCancel},
		"remote-status": {flags: remoteIDFlags("remote-status"), run: runRemoteStatus},
		"remote-logs":   {flags: remoteLogsFlags, run: runRemoteLogs},
		"remote-result": {flags: remoteResultFlags, run: runRemoteResult},
		"serve":         {flags: serveFlags, about: serveAbout, run: runServe},
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

// remoteSpelling lets `irgo-winvm remote submit ...` mean `remote-submit
// ...`, the spelling a person reaches for; the commands themselves, and the
// MCP tools, are the hyphenated names. Only the remote-* commands, so
// `remote` alone still prints the usage as unknown.
func remoteSpelling(args []string) []string {
	if len(args) >= 2 && args[0] == "remote" && !strings.HasPrefix(args[1], "-") {
		if _, ok := find("remote-" + args[1]); ok {
			return append([]string{"remote-" + args[1]}, args[2:]...)
		}
	}
	return args
}

// macOnly refuses a command that drives UTM anywhere but macOS, after its
// flags parse (so -h still answers) and before it touches anything. It says
// what to do instead, because the person on Linux or Windows who typed it
// wants the work done, not a lecture on UTM.
func macOnly(c command.Command, goos string) error {
	if !c.MacOnly || goos == "darwin" {
		return nil
	}
	return fmt.Errorf("%w: %s drives UTM, which needs macOS on Apple Silicon, and this is %s. "+
		"To run a Windows binary from here on a Mac elsewhere: irgo-winvm remote submit [-gui] <app.exe> [args...] "+
		"(with %s and %s set; irgo-winvm remote-submit -h)", errUsage, c.Name, goos, wire.EnvRemoteURL, "IRGO_REMOTE_TOKEN")
}
