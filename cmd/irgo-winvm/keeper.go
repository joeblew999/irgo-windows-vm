package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/device"
	"github.com/joeblew999/irgo-windows-vm/internal/keeper"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// The keeper: the Mac awake while a VM runs, VMs marked keep-running started
// again when they stop, and the VMs written down for claude-rig's report.
// docs/guides/using.md, "The keeper".

const keeperAbout = `  Runs until stopped, under pitchfork (keeper-create installs it there).
  Every pass (-every) it keeps the Mac awake while a VM runs on AC power,
  starts again each VM marked keep-running (vm-keep-create) that UTM lists
  stopped, opening UTM first if one needs it, and writes the VMs to
  ~/.config/claude-rig/vms.json (in RIG_CONFIG_HOME, if set) for claude-rig,
  whose report to fleet-api carries them. It sends nothing itself. It never
  stops, deletes or restarts a VM that is not marked, and never quits UTM.
  -once prints what it would write, and changes nothing.
`

// envRigHome is where claude-rig keeps its files; the keeper writes its
// VMs there.
const envRigHome = "RIG_CONFIG_HOME"

// keeperDaemon is the keeper's name in pitchfork's config.
const keeperDaemon = "irgo-winvm-keeper"

// keeperReady is the line the keeper prints once it is running, which
// pitchfork waits for (ready_output).
const keeperReady = "keeper: watching"

// vmsPath is the file the keeper writes its VMs to: claude-rig's
// vms.json, which its report reads.
func vmsPath() string {
	if d := strings.TrimSpace(os.Getenv(envRigHome)); d != "" {
		return filepath.Join(d, "vms.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "claude-rig", "vms.json")
}

// oldFleetDir is where the keeper kept fleet-api's write token and its
// report spool when it reported itself; keeper-create and keeper-delete
// remove it.
func oldFleetDir() string { return filepath.Join(utmvm.Root(), "fleet") }

func keeperFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("keeper", flag.ContinueOnError)
	fs.Duration("every", keeper.DefaultEvery, "time between passes")
	fs.Bool("once", false, "read the VMs and print what the keeper would write; open no UTM, start no VM, hold nothing, write nothing")
	return fs
}

func runKeeper(v values, _ []string) error {
	say := utmvm.Printer("keeper")
	every := v.Duration("every")
	if every < time.Second || every > time.Minute {
		return fmt.Errorf("%w: -every must be from 1s to 1m: claude-rig takes the VMs while they are under 2 minutes old", errUsage)
	}
	path := vmsPath()
	cfg := keeper.Config{
		UTM:   macUTM{say: say},
		Awake: &keeper.Caffeinate{},
		Power: device.Power,
		Write: func(f keeper.VMsFile) error { return keeper.WriteVMs(path, f) },
		Every: every,
		Say:   say,
	}
	if v.Bool("once") {
		b, err := json.MarshalIndent(keeper.New(cfg).Once(), "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	release, err := utmvm.Acquire(utmvm.KeeperLock)
	if err != nil {
		return fmt.Errorf("%w; one keeper runs per Mac (pitchfork status %s)", err, keeperDaemon)
	}
	defer release()
	say("version %s, data in %s", version, utmvm.Home(utmvm.Root()))
	say("writing the VMs every pass to %s, for claude-rig's report", utmvm.Home(path))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	say("%s every %s; stop with pitchfork stop %s, or Ctrl-C", keeperReady, every, keeperDaemon)
	keeper.New(cfg).Run(ctx)
	say("stopped")
	return nil
}

// macUTM is the keeper's UTM: utmvm's, with nothing in it that quits UTM or
// stops a VM.
type macUTM struct{ say func(string, ...any) }

func (macUTM) Installed() bool        { return utmvm.UTMInstalled() }
func (macUTM) Running() (bool, error) { return utmvm.UTMRunning() }
func (macUTM) Open() error            { return utmvm.OpenUTM() }

func (macUTM) List() ([]keeper.VM, error) {
	entries, err := utmvm.ListIfOpen()
	if err != nil {
		return nil, err
	}
	out := make([]keeper.VM, 0, len(entries))
	for _, e := range entries {
		out = append(out, keeper.VM{Name: e.Name, Status: e.Status, Running: utmvm.VMRunning(e.Status)})
	}
	return out, nil
}

func (u macUTM) Start(name string) error { return utmvm.Named(name).StartKeeping(u.say) }

func (macUTM) Lock(name string) (func(), error) { return utmvm.Acquire(utmvm.VMLock(name)) }

func (u macUTM) Records() ([]keeper.Record, error) {
	recs, bad, err := utmvm.VMRecords()
	if err != nil {
		return nil, err
	}
	for _, b := range bad {
		u.say("a VM record cannot be read: %s", b)
	}
	out := make([]keeper.Record, 0, len(recs))
	for _, r := range recs {
		out = append(out, keeper.Record{Name: r.Name, Owner: r.Owner, OS: r.OS, KeepRunning: r.KeepRunning})
	}
	return out, nil
}

// vm-keep-create and vm-keep-delete: the mark the keeper acts on.

func vmKeepFlags(name string) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.String("vm", "", "the VM (required)")
		ownerFlag(fs)
		return fs
	}
}

func runVMKeepCreate(v values, _ []string) error { return setKeep(v, true) }
func runVMKeepDelete(v values, _ []string) error { return setKeep(v, false) }

func setKeep(v values, keep bool) error {
	name := v.String("vm")
	cmdName := map[bool]string{true: "vm-keep-create", false: "vm-keep-delete"}[keep]
	say := utmvm.Printer(cmdName)
	if name == "" {
		return fmt.Errorf("%w: -vm is required", errUsage)
	}
	if keep {
		// The VM must exist: a mark on a name UTM does not know would have
		// the keeper say so every pass.
		if _, err := utmvm.Find(name); err != nil {
			return err
		}
	}
	changed, err := utmvm.SetKeepRunning(name, keep)
	if err != nil {
		return err
	}
	switch {
	case keep && changed:
		say("%s is marked keep-running: the keeper starts it whenever UTM lists it stopped (record: %s)", name, utmvm.Home(utmvm.RecordsDir()))
	case keep:
		say("%s was already marked keep-running", name)
	case changed:
		say("%s is no longer marked keep-running: the keeper leaves it as it is", name)
	default:
		say("%s was not marked keep-running; nothing to undo", name)
	}
	if keep {
		say("the keeper acts on it only while it runs: %s", keeperStateLine())
	}
	return nil
}

// keeperStateLine says whether pitchfork runs the keeper, for a person.
func keeperStateLine() string {
	if _, err := exec.LookPath("pitchfork"); err != nil {
		return "pitchfork is not installed, so no keeper runs (keeper-create)"
	}
	if pitchforkRunning(keeperDaemon) {
		return "pitchfork runs " + keeperDaemon
	}
	return "pitchfork does not run " + keeperDaemon + " now; keeper-create installs and starts it"
}

// keeper-create and keeper-delete: the keeper as a pitchfork daemon.

func keeperCreateFlags() *flag.FlagSet {
	return flag.NewFlagSet("keeper-create", flag.ContinueOnError)
}

func keeperDeleteFlags() *flag.FlagSet {
	return flag.NewFlagSet("keeper-delete", flag.ContinueOnError)
}

// pitchforkConfig is pitchfork's machine-wide config, which claude-rig's
// session uses too.
func pitchforkConfig() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pitchfork", "config.toml")
}

// runKeeperCreate removes what the keeper kept when it reported itself, puts
// the keeper in pitchfork's config, has pitchfork start at boot, and starts
// the keeper; each step only if it is not done, so it can run again.
func runKeeperCreate(_ values, _ []string) error {
	say := utmvm.Printer("keeper-create")
	if _, err := exec.LookPath("pitchfork"); err != nil {
		return fmt.Errorf("%w: pitchfork is not on PATH; install it (mise use -g pitchfork@2.29.0) and run this again", errUsage)
	}
	bin, err := os.Executable()
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err != nil {
		return fmt.Errorf("finding this binary: %w", err)
	}
	if strings.Contains(bin, "go-build") || strings.HasPrefix(bin, os.TempDir()) {
		return fmt.Errorf("%w: this binary (%s) is a temporary build; install irgo-winvm and run keeper-create from it", errUsage, bin)
	}
	if strings.ContainsAny(bin, `'"`) {
		return fmt.Errorf("%w: the binary's path has a quote in it (%s), which pitchfork's run line cannot hold", errUsage, bin)
	}

	say("STEP 1/4  what the keeper kept when it reported to fleet-api itself")
	if removed, err := removeOldFleetDir(); err != nil {
		return err
	} else if removed {
		say("          removed %s: fleet-api's write token and the report spool; claude-rig reports for this Mac", utmvm.Home(oldFleetDir()))
	} else {
		say("          nothing left")
	}

	say("STEP 2/4  the daemon in %s", utmvm.Home(pitchforkConfig()))
	body := fmt.Sprintf("run = '\"%s\" keeper'\nboot_start = true\nretry = true\nready_output = %q\n", bin, keeperReady)
	changed, err := editPitchforkConfig(func(text string) string { return upsertDaemon(text, keeperDaemon, body) })
	if err != nil {
		return err
	}
	if !daemonListed(keeperDaemon) {
		return fmt.Errorf("pitchfork does not list %s after it was written to %s (pitchfork daemons)", keeperDaemon, pitchforkConfig())
	}
	say("          [daemons.%s] runs %s keeper%s", keeperDaemon, utmvm.Home(bin), map[bool]string{true: "", false: "; unchanged"}[changed])

	say("STEP 3/4  pitchfork starts at boot")
	if out, _ := exec.Command("pitchfork", "boot", "status").CombinedOutput(); strings.Contains(string(out), "is enabled") {
		say("          already")
	} else if out, err := exec.Command("pitchfork", "boot", "enable").CombinedOutput(); err != nil {
		return fmt.Errorf("pitchfork boot enable: %w: %s", err, strings.TrimSpace(string(out)))
	} else {
		say("          enabled")
	}

	say("STEP 4/4  the keeper running")
	if pitchforkRunning(keeperDaemon) && !changed {
		say("          already running")
		return nil
	}
	if out, err := exec.Command("pitchfork", "start", keeperDaemon, "--force").CombinedOutput(); err != nil {
		return fmt.Errorf("pitchfork start %s: %w: %s\n  See: pitchfork logs %s", keeperDaemon, err, strings.TrimSpace(string(out)), keeperDaemon)
	}
	if !pitchforkRunning(keeperDaemon) {
		return fmt.Errorf("pitchfork started %s and does not list it running; see: pitchfork logs %s", keeperDaemon, keeperDaemon)
	}
	say("          running; its output: pitchfork logs %s", keeperDaemon)
	say("          The first time, macOS asks whether pitchfork may control UTM.app: answer Allow, or the keeper cannot list or start a VM")
	return nil
}

// runKeeperDelete stops the keeper, takes it out of pitchfork's config and
// removes what it kept when it reported itself. Nothing to remove is success. pitchfork's start at
// boot is left on: other daemons use it.
func runKeeperDelete(_ values, _ []string) error {
	say := utmvm.Printer("keeper-delete")
	if _, err := exec.LookPath("pitchfork"); err == nil && pitchforkRunning(keeperDaemon) {
		say("stopping %s", keeperDaemon)
		if out, err := exec.Command("pitchfork", "stop", keeperDaemon).CombinedOutput(); err != nil {
			return fmt.Errorf("pitchfork stop %s: %w: %s", keeperDaemon, err, strings.TrimSpace(string(out)))
		}
		if pitchforkRunning(keeperDaemon) {
			return fmt.Errorf("pitchfork stop %s returned, and pitchfork still lists it running", keeperDaemon)
		}
	} else {
		say("%s is not running", keeperDaemon)
	}
	changed, err := editPitchforkConfig(func(text string) string { return removeDaemon(text, keeperDaemon) })
	if err != nil {
		return err
	}
	if changed {
		say("removed [daemons.%s] from %s", keeperDaemon, utmvm.Home(pitchforkConfig()))
	} else {
		say("no [daemons.%s] in %s", keeperDaemon, utmvm.Home(pitchforkConfig()))
	}
	if removed, err := removeOldFleetDir(); err != nil {
		return err
	} else if removed {
		say("removed %s: fleet-api's write token and the report spool the keeper once kept", utmvm.Home(oldFleetDir()))
	}
	return nil
}

// removeOldFleetDir removes the folder the keeper kept fleet-api's write
// token and its spool in, and checks it is gone. Nothing there is success.
func removeOldFleetDir() (removed bool, err error) {
	d := oldFleetDir()
	if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err := os.RemoveAll(d); err != nil {
		return false, err
	}
	if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("%s is still there after removing it", d)
	}
	return true, nil
}

// editPitchforkConfig rewrites pitchfork's config through edit, if that
// changes it, by a temporary file and a rename. A missing file is empty.
func editPitchforkConfig(edit func(string) string) (changed bool, err error) {
	p := pitchforkConfig()
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	next := edit(string(b))
	if next == string(b) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(p, []byte(next), 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", p, err)
	}
	return true, nil
}

// upsertDaemon returns text with [daemons.<name>] holding exactly body,
// replacing the section if it is there and appending it if not. Everything
// else in the file is kept as it is.
func upsertDaemon(text, name, body string) string {
	section := "[daemons." + name + "]\n" + body
	start, end, ok := daemonSection(text, name)
	if ok {
		return text[:start] + section + text[end:]
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	return text + section
}

// removeDaemon returns text without [daemons.<name>].
func removeDaemon(text, name string) string {
	start, end, ok := daemonSection(text, name)
	if !ok {
		return text
	}
	out := text[:start] + text[end:]
	return strings.TrimRight(out, "\n") + map[bool]string{true: "", false: "\n"}[strings.TrimSpace(out) == ""]
}

// daemonSection is where [daemons.<name>] starts and ends in text: from its
// header line to the next table header, or the end.
func daemonSection(text, name string) (start, end int, ok bool) {
	header := "[daemons." + name + "]"
	pos := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if ok && strings.HasPrefix(trimmed, "[") {
			return start, pos, true
		}
		if !ok && trimmed == header {
			start, ok = pos, true
		}
		pos += len(line)
	}
	return start, len(text), ok
}

// daemonListed asks pitchfork whether it knows the daemon: whether it read
// the config as written.
func daemonListed(name string) bool {
	out, err := exec.Command("pitchfork", "daemons").Output()
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && (f[0] == name || strings.HasSuffix(f[0], "/"+name)) {
			return true
		}
	}
	return false
}

// pitchforkRunning is whether `pitchfork list` shows the daemon running.
func pitchforkRunning(name string) bool {
	out, err := exec.Command("pitchfork", "list", "--hide-header").Output()
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && (f[0] == name || strings.HasSuffix(f[0], "/"+name)) && f[1] == "running" {
			return true
		}
	}
	return false
}

// writeFileAtomic writes p through a temporary file beside it and a rename,
// checking the write and the close.
func writeFileAtomic(p string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+"-*")
	if err != nil {
		return err
	}
	_, wErr := f.Write(b)
	mErr := f.Chmod(mode)
	cErr := f.Close()
	if err := errors.Join(wErr, mErr, cErr); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}
