package utmvm

// Is there room for another VM? Several callers on one Mac each making a VM
// of their own can run it out of memory, and a clone's disk grows with every
// write. vm-create asks before it makes or starts a VM, and refuses with the
// numbers when the answer is no or cannot be found out.
//
// Memory is what UTM says each VM is configured with, not what it is using
// now: the guest commits it. Measured 1 Oct 2026 on a 16 GiB Mac with
// irgo-win11 running (8192 MiB configured): QEMULauncher's footprint was
// 8327 MB, 8051 MB of it dirty, and 6.3 GB of the 7 GB swap was in use. So a
// second 8 GiB VM on that Mac is a machine swapping to a halt, not a squeeze.

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// vmMemoryMiB is what every VM is made with (setDefaults), and what a VM that
// does not exist yet is counted as needing. A clone has its golden image's,
// which was made with this.
const vmMemoryMiB = 8192

// hostMemoryReserveBytes is the memory left for macOS and the owner's own
// work after every VM has its configured memory. 4 GiB: the system alone sits
// around 3 GiB, and the measurement above shows a 16 GiB Mac with one VM and
// 8 GiB to spare already 6.3 GB into swap. On 16 GiB this allows one VM (16 -
// 8 = 8 left), and refuses a second (0 left); on 32 GiB, three.
const hostMemoryReserveBytes = 4 << 30

// hostDiskReserveBytes is the free space kept for macOS after a new VM has
// what it needs. macOS keeps its swap on this volume (7 GB of it in the
// measurement above), and warns at about 5 GB free; 10 GiB keeps it out of
// that.
const hostDiskReserveBytes = 10 << 30

// installHeadroomBytes is what a VM installed from the ISO takes: about 30 GiB
// once Windows is on it ("What it costs" in docs/DEVELOPMENT.md). It also
// covers vm-create pulling the golden image when there is none here: 8.4 GB
// of chunks, then the bundle rebuilt (19 GB of data, measured 1 Oct 2026).
const installHeadroomBytes = 30 << 30

// diskNeed is which disk headroom a create needs.
type diskNeed int

const (
	diskNone       diskNeed = iota // the VM exists: booting it writes little
	diskForClone                   // cloneHeadroomBytes
	diskForInstall                 // installHeadroomBytes
)

func (d diskNeed) bytes() int64 {
	switch d {
	case diskForClone:
		return cloneHeadroomBytes
	case diskForInstall:
		return installHeadroomBytes
	}
	return 0
}

func (d diskNeed) String() string {
	switch d {
	case diskForClone:
		return "a clone"
	case diskForInstall:
		return "an install"
	}
	return "nothing new"
}

// ErrNoRoom is vm-create refusing because starting another VM would leave too
// little memory or disk, or because it could not find out.
var ErrNoRoom = errors.New("no room for another VM")

// CapacityPlan is what vm-create is about to do.
type CapacityPlan struct {
	VM     string
	Exists bool     // the VM is there, stopped, and will be booted
	Disk   diskNeed // what a new VM needs on disk

	// Overcommit starts the VM whatever its memory: the person asking accepts
	// that the VMs' configured memory exceeds the Mac's and they will swap.
	// Measured 1 Oct 2026 on 16 GiB: irgo-win11 and two fresh clones (24 GiB
	// configured) booted, ran app-create at once and passed glaze-check -windows
	// in minutes; a guest commits its memory as it runs, so that does not
	// last. Disk is still checked: running out of it corrupts a clone.
	Overcommit bool
}

// vmMemory is one VM as UTM describes it.
type vmMemory struct {
	Name, Status string
	MiB          int // 0 when UTM would not say
}

// capacityFacts is everything the decision reads, gathered by CheckCapacity
// so the decision itself can be tested with any machine described.
type capacityFacts struct {
	plan CapacityPlan

	host    uint64
	hostErr error

	vms    []vmMemory
	vmsErr error

	pending []string // VMs other vm-creates are making, not yet listed as running

	free    int64
	freeErr error
}

// vmUsesMemory reports whether a VM in this state holds its memory. Anything
// but stopped: starting, paused (UTM keeps a paused guest's memory), resuming
// and stopping all do, and an unknown state is counted rather than guessed
// free.
func vmUsesMemory(status string) bool { return !strings.EqualFold(status, "stopped") }

// Answer is a guard's verdict.
type Answer int

const (
	AnswerCannotTell Answer = iota // the zero value: an unanswered guard refuses
	AnswerYes
	AnswerNo
)

func (a Answer) String() string {
	switch a {
	case AnswerYes:
		return "yes"
	case AnswerNo:
		return "no"
	}
	return "cannot tell"
}

// decideCapacity answers "is there room?" yes, no or cannot tell, and says
// why with the numbers it used.
func decideCapacity(f capacityFacts) (Answer, string) {
	gib := func(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
	if f.hostErr != nil {
		return AnswerCannotTell, "cannot read the Mac's memory (hw.memsize): " + f.hostErr.Error()
	}
	if f.vmsErr != nil {
		return AnswerCannotTell, "cannot ask UTM what its VMs are configured with: " + f.vmsErr.Error()
	}
	var used int64
	var running []string
	need := int64(vmMemoryMiB) << 20
	found := !f.plan.Exists
	for _, v := range f.vms {
		if strings.EqualFold(v.Name, f.plan.VM) {
			found = true
			if v.MiB <= 0 {
				return AnswerCannotTell, fmt.Sprintf("UTM would not say how much memory %s is configured with", v.Name)
			}
			need = int64(v.MiB) << 20
			if vmUsesMemory(v.Status) {
				return AnswerYes, v.Name + " is already " + v.Status + "; nothing new is started"
			}
			continue
		}
		if !vmUsesMemory(v.Status) {
			continue
		}
		if v.MiB <= 0 {
			return AnswerCannotTell, fmt.Sprintf("%s is %s and UTM would not say how much memory it has", v.Name, v.Status)
		}
		used += int64(v.MiB) << 20
		running = append(running, fmt.Sprintf("%s (%s, %s)", v.Name, v.Status, gib(int64(v.MiB)<<20)))
	}
	if !found {
		return AnswerCannotTell, fmt.Sprintf("UTM lists no %s to read its memory from", f.plan.VM)
	}
	for _, p := range f.pending {
		used += int64(vmMemoryMiB) << 20
		running = append(running, fmt.Sprintf("%s (being made by another vm-create, %s)", p, gib(int64(vmMemoryMiB)<<20)))
	}
	left := int64(f.host) - used - need
	mem := fmt.Sprintf("memory: %s in this Mac, %s for VMs already running, %s for %s: %s left, want %s for macOS",
		gib(int64(f.host)), gib(used), gib(need), f.plan.VM, gib(left), gib(hostMemoryReserveBytes))
	if left < hostMemoryReserveBytes && f.plan.Overcommit {
		mem += " (short; going ahead because of -overcommit: expect swapping)"
	} else if left < hostMemoryReserveBytes {
		who := "none"
		if len(running) > 0 {
			who = strings.Join(running, ", ")
		}
		return AnswerNo, mem + ". Running: " + who + ". -overcommit starts it anyway, swapping"
	}
	if f.plan.Disk == diskNone {
		return AnswerYes, mem
	}
	if f.freeErr != nil {
		return AnswerCannotTell, mem + "; cannot read the free disk space: " + f.freeErr.Error()
	}
	want := f.plan.Disk.bytes() + hostDiskReserveBytes
	disk := fmt.Sprintf("disk: %s free, want %s for %s and %s for macOS",
		gib(f.free), gib(f.plan.Disk.bytes()), f.plan.Disk, gib(hostDiskReserveBytes))
	if f.free < want {
		return AnswerNo, mem + "; " + disk
	}
	return AnswerYes, mem + "; " + disk
}

//go:embed assets/utm-memory.applescript
var memoryScript string

// vmMemoryTable asks UTM for every VM's status and configured memory.
func vmMemoryTable() ([]vmMemory, error) {
	out, err := utmScript(memoryScript, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return parseMemoryTable(out)
}

// parseMemoryTable reads utm-memory.applescript's lines. A line that is not
// four fields is an answer this code does not understand, which is an error,
// not a VM to skip: skipping it could skip a running VM.
func parseMemoryTable(out string) ([]vmMemory, error) {
	var vms []vmMemory
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 {
			return nil, fmt.Errorf("unexpected line from UTM: %q", line)
		}
		v := vmMemory{Status: f[1], Name: f[3]}
		if n, err := strconv.Atoi(f[2]); err == nil {
			v.MiB = n
		}
		vms = append(vms, v)
	}
	return vms, nil
}

// pendingCreates is the VMs other live vm-creates are making that UTM does not
// yet list as holding memory, so two creates at once count each other.
func pendingCreates(vms []vmMemory) []string {
	records, _, err := VMRecords()
	if err != nil {
		return nil
	}
	holding := map[string]bool{}
	for _, v := range vms {
		if vmUsesMemory(v.Status) {
			holding[strings.ToLower(v.Name)] = true
		}
	}
	var out []string
	for _, r := range records {
		if r.CreatingPID == 0 || r.CreatingPID == os.Getpid() || holding[strings.ToLower(r.Name)] {
			continue
		}
		if processAlive(r.CreatingPID) {
			out = append(out, r.Name)
		}
	}
	return out
}

// CheckCapacity answers whether plan has room, saying so, and returns
// ErrNoRoom on no and on cannot tell. Guards here refuse when they cannot
// answer: a guard that allows on an unreadable answer allows exactly when it
// should not.
func CheckCapacity(plan CapacityPlan, say func(string, ...any)) error {
	f := capacityFacts{plan: plan}
	f.host, f.hostErr = hostMemory()
	f.vms, f.vmsErr = vmMemoryTable()
	if f.vmsErr == nil {
		f.pending = pendingCreates(f.vms)
	}
	if plan.Disk != diskNone {
		if dir, err := DefaultVMDir(); err != nil {
			f.freeErr = err
		} else {
			f.free, f.freeErr = FreeBytes(dir)
		}
	}
	a, why := decideCapacity(f)
	say("room:   %s — %s", a, why)
	if a == AnswerYes {
		return nil
	}
	return fmt.Errorf("%w for %s (%s): %s.\n"+
		"  Stop or vm-delete a VM you own, or wait for another caller's to stop; `irgo-winvm status` lists them with their owners",
		ErrNoRoom, plan.VM, a, why)
}
