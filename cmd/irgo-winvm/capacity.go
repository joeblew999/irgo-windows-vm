package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/ledger"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

func capacityFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("capacity", flag.ContinueOnError)
	fs.Bool("json", false, "print the whole report as JSON, for scripts and agents")
	return fs
}

// runCapacity reports this Mac's disk and memory: what each VM and each part
// of the tool's data holds, who owns what, and how many more VMs fit, by the
// same model vm-create's guard decides with.
func runCapacity(v values, _ []string) error {
	r := utmvm.Capacity()
	sendCapacity(r)
	if v.Bool("json") {
		enc := json.NewEncoder(utmvm.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	out := utmvm.Reporter("capacity")
	for _, line := range capacityLines(r) {
		out("%s", line)
	}
	return nil
}

// gib is bytes as GiB with one decimal, the unit every capacity number uses,
// so the table and the guard's messages read alike.
func gib(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

// capacityLines is the report as text.
func capacityLines(r utmvm.CapacityReport) []string {
	var l []string
	add := func(f string, a ...any) { l = append(l, fmt.Sprintf(f, a...)) }
	p := r.Policy
	if r.DiskErr != "" {
		add("disk:    cannot tell: %s", r.DiskErr)
	} else {
		add("disk:    %s free of %s on the volume holding the VMs (%s); %s kept for macOS",
			gib(r.DiskFree), gib(r.DiskTotal), utmvm.Home(r.Volume), gib(p.DiskReserve))
	}
	if r.MemoryErr != "" {
		add("memory:  cannot tell: %s", r.MemoryErr)
	} else {
		add("memory:  %s in this Mac, %s configured for running VMs, %s kept for macOS; a clone takes %s, an install %s, a Linux VM %s",
			gib(r.Memory), gib(r.RunningMemory), gib(p.MemoryReserve), gib(p.CloneMemory), gib(p.VMMemory), gib(p.LinuxMemory))
	}
	add("")
	add("%-18s %-8s %-7s %-8s %-34s %-6s %-10s %-10s %-9s %s", "VM", "STATE", "KIND", "OS", "OWNER", "MEMORY", "ITS OWN", "PROMISED", "IDLE", "")
	if r.VMsErr != "" {
		add("cannot list UTM's VMs: %s", r.VMsErr)
	}
	for _, vm := range r.VMs {
		owner := vm.Owner
		switch {
		case owner != "":
		case vm.Kind == "owner":
			owner = "(the machine's owner)"
		case vm.Kind == "golden":
			owner = "(shared)"
		default:
			owner = "(no record)"
		}
		own, prom := gib(vm.Private), gib(vm.Promised)
		if vm.Err != "" {
			own, prom = "?", "?"
		}
		idle, note := "", ""
		if vm.IdleS > 0 {
			idle = (time.Duration(vm.IdleS) * time.Second).Round(time.Minute).String()
		}
		switch {
		case vm.Err != "":
			note = "cannot measure: " + vm.Err
		case vm.Stale:
			note = "stale: vm-reap -force would remove it"
		case vm.Kind == "golden":
			note = fmt.Sprintf("its %s are shared with its clones", gib(vm.Allocated))
		}
		add("%-18s %-8s %-7s %-8s %-34s %-6d %-10s %-10s %-9s %s", vm.Name, vm.Status, vm.Kind, vm.OS, trimTo(owner, 34),
			vm.MemoryMiB, own, prom, idle, note)
	}
	add("")
	add("%-18s %-10s %-10s %s", "TOOL DATA", "ITS OWN", "SHARED", "BOUNDED BY")
	for _, d := range r.Data {
		add("%-18s %-10s %-10s %s", d.What, gib(d.Private), gib(d.Allocated-d.Private), d.Bound)
	}
	add("in %s", utmvm.Home(utmvm.Root()))
	add("")
	add("Bytes are APFS's own accounting: ITS OWN is what deleting it frees; blocks clones share are counted once (%s).", gib(r.Shared))
	if r.DiskErr == "" {
		add("The VMs and the tool's data hold %s of the %s in use on that volume.", gib(r.Accounted), gib(r.DiskTotal-r.DiskFree))
	}
	add("")
	if len(r.Owners) > 0 {
		add("%-40s %-4s %-10s %s", "OWNER", "VMS", "HOLDS", "STALE")
		for _, o := range r.Owners {
			add("%-40s %-4d %-10s %d", trimTo(o.Owner, 40), o.VMs, gib(o.Held), o.Stale)
		}
	}
	quota := p.Quota.String()
	if p.QuotaErr != "" {
		quota = "cannot tell: " + p.QuotaErr
	}
	add("quota:   each owner may have %s (%s, %s); a VM holds its own bytes or its %s reserve, whichever is more",
		quota, utmvm.QuotaVMsEnv, utmvm.QuotaGiBEnv, gib(p.CloneReserve))
	add("")
	add("room for another clone: %s", strings.ToUpper(r.Room.Clone))
	add("  %s", r.Room.Why)
	add("  by disk, %d more clone(s) fit (%s each, after %s still promised to the VMs here); by memory, %d more clone(s) can run",
		r.Room.MoreClones, gib(p.CloneReserve), gib(r.Promised), r.Room.MoreRunning)
	return l
}

func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// capacitySummary is doctor's one row.
func capacitySummary(r utmvm.CapacityReport) doctorRow {
	// No Path: Present says the numbers are known, not that a file exists.
	row := doctorRow{What: "capacity"}
	switch {
	case r.DiskErr != "" || r.MemoryErr != "":
		row.State = "cannot tell"
	default:
		row.State, row.Present = gib(r.DiskFree)+" free", true
	}
	row.Note = fmt.Sprintf("on %s: another clone: %s; %d more fit on disk, %d more can run. irgo-winvm capacity says what holds it",
		utmvm.Home(r.Volume), r.Room.Clone, r.Room.MoreClones, r.Room.MoreRunning)
	return row
}

// snapshotOf is the report as the compact snapshot a ledger event carries
// (wire.LedgerCapacitySnapshot): under the 500 bytes the Worker keeps, and
// nothing in it is a name.
func snapshotOf(r utmvm.CapacityReport) wire.LedgerCapacitySnapshot {
	s := wire.LedgerCapacitySnapshot{DiskFree: r.DiskFree, DiskTotal: r.DiskTotal, Memory: r.Memory, RunningMemory: r.RunningMemory,
		VMs: len(r.VMs), Promised: r.Promised, Clone: r.Room.Clone, MoreClones: r.Room.MoreClones, MoreRunning: r.Room.MoreRunning}
	for _, vm := range r.VMs {
		if !strings.EqualFold(vm.Status, "stopped") {
			s.Running++
		}
		if vm.Stale {
			s.Stale++
		}
	}
	for _, d := range r.Data {
		s.Tool += d.Private
	}
	return s
}

// sendCapacity reports r to the ledger, when it is on.
func sendCapacity(r utmvm.CapacityReport) {
	if !ledger.On() || r.DiskErr != "" {
		return
	}
	b, err := json.Marshal(snapshotOf(r))
	if err != nil {
		return
	}
	ledger.Emit(ledger.Event{Type: ledger.Capacity, Command: "capacity", Detail: string(b)})
}

// reportCapacityChange sends a fresh snapshot after a command that changed
// what the disk or memory holds. It costs an AppleScript call and a stat per
// VM, so only when the ledger is on.
func reportCapacityChange() {
	if ledger.On() {
		sendCapacity(utmvm.Capacity())
	}
}

func pruneFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.Bool("force", false, "actually remove; without this it only lists")
	fs.Bool("json", false, "print the items as JSON")
	return fs
}

const pruneAbout = `  prune           what is past its bound, and what removing it would free
  prune -force    remove it

  Runtime screenshots past 14 days or 200 MiB (the newest of each stage is
  kept), glaze run logs past 30 days or 100 MiB, staged binaries unused for
  7 days, and what interrupted work left for a day: golden-pull/.parts,
  vm/staging, media scratch. vm-create does this on the way in. VMs are
  vm-reap's, the media iso-delete's, the golden image vm-golden-delete's.
`

// runPrune lists, and with -force removes, what the tool wrote that is past
// its bound, and says what df saw freed.
func runPrune(v values, _ []string) error {
	force := v.Bool("force")
	say := utmvm.Printer("prune")
	before, bErr := utmvm.FreeBytes(utmvm.Root())
	items, freed := utmvm.Prune(utmvm.DefaultPrunePolicy, force, time.Now())
	if v.Bool("json") {
		enc := json.NewEncoder(utmvm.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(items); err != nil {
			return err
		}
	} else {
		say("in:     %s", utmvm.Home(utmvm.Root()))
		for _, it := range items {
			verb := "would remove"
			switch {
			case it.Done:
				verb = "removed"
			case it.Err != "":
				verb = "kept"
			}
			say("  %-12s %-9s %-16s %s — %s %s", verb, utmvm.HumanBytes(it.Bytes), it.Kind, utmvm.Home(it.Path), it.Why, it.Err)
		}
	}
	var total int64
	var failed []string
	for _, it := range items {
		total += it.Bytes
		if force && !it.Done {
			failed = append(failed, it.Path+": "+it.Err)
		}
	}
	switch {
	case len(items) == 0:
		say("nothing is past its bound")
		return nil
	case !force:
		return fmt.Errorf("%d item(s), %s. Pass -force to remove them (%w)", len(items), utmvm.HumanBytes(total), errRefused)
	}
	after, aErr := utmvm.FreeBytes(utmvm.Root())
	if bErr == nil && aErr == nil {
		say("freed %s by APFS's count; free space went from %s to %s", utmvm.HumanBytes(freed), gib(before), gib(after))
	} else {
		say("freed %s by APFS's count", utmvm.HumanBytes(freed))
	}
	reportCapacityChange()
	if len(failed) > 0 {
		return fmt.Errorf("%d item(s) not removed:\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	return nil
}

// autoPrune is prune -force on vm-create's way in: everything it removes is
// past its bound already, so it needs no -force, and it never fails the
// create. One line, and only when it removed something.
func autoPrune(say func(string, ...any)) {
	items, freed := utmvm.Prune(utmvm.DefaultPrunePolicy, true, time.Now())
	n := 0
	for _, it := range items {
		if it.Done {
			n++
		}
	}
	if n > 0 {
		say("pruned: %d file(s) past their bounds, %s (irgo-winvm prune lists what it takes)", n, utmvm.HumanBytes(freed))
	}
}

// AutoReapEnv makes vm-create run vm-reap -force with this lease when there
// is no room, then ask again. Unset, it never reaps: deleting somebody's VM
// is the owner's decision to configure, not a default.
const autoReapEnv = "IRGO_WINVM_AUTO_REAP"

// autoReapLease is IRGO_WINVM_AUTO_REAP as a lease, or 0 when unset; an
// unreadable value is an error rather than a quiet "never".
func autoReapLease() (time.Duration, error) {
	s := strings.TrimSpace(os.Getenv(autoReapEnv))
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: %s=%q: want a lease such as 24h", errUsage, autoReapEnv, s)
	}
	return d, nil
}

// beginCreateReaping is utmvm.BeginCreate, and when it finds no room and
// IRGO_WINVM_AUTO_REAP is set, vm-reap -force with that lease (oldest-idle
// clones that nobody is using, never irgo-win11 or the golden image), then
// one more try.
func beginCreateReaping(begin func() (func(), error), say func(string, ...any)) (func(), error) {
	finish, err := begin()
	if err == nil || !errors.Is(err, utmvm.ErrNoRoom) {
		return finish, err
	}
	lease, lErr := autoReapLease()
	if lErr != nil {
		return nil, errors.Join(err, lErr)
	}
	if lease == 0 {
		return nil, fmt.Errorf("%w\n  %s=24h would let vm-create remove clones idle that long first", err, autoReapEnv)
	}
	say("no room; reaping clones idle past %s (%s)", lease, autoReapEnv)
	decisions, _, rErr := utmvm.Reap(lease, true, func(f string, a ...any) { say("  "+f, a...) })
	reaped := 0
	for _, d := range decisions {
		if d.Action == utmvm.ReapDelete && !strings.Contains(d.Why, "FAILED") {
			reaped++
			say("  reaped %s (owner %s): %s", d.Record.Name, d.Record.Owner, d.Why)
		}
	}
	if reaped == 0 {
		return nil, errors.Join(err, rErr)
	}
	return begin()
}
