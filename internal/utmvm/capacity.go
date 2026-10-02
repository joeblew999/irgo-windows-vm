package utmvm

// What this Mac's disk and memory hold, by whom, and how many more VMs fit:
// `irgo-winvm capacity`, doctor's summary row, and the snapshot sent to the
// ledger. It reads the same model and the same decision as vm-create's guard
// (capacity_model.go, vm_capacity.go), so "room for another clone: yes" here
// is what vm-create would answer now for a caller with no VMs.
//
// Bytes are APFS's own accounting: a VM's or a file's private bytes, what
// deleting it frees, and the blocks a clone family shares counted once. du
// counts a clone and its source in full each (golden-export, a clone of the
// golden image, read 10 GB to du and holds 0 bytes of its own).

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// reportLease is the idle time after which `capacity` calls a clone stale:
// vm-reap's default -stale.
const reportLease = 24 * time.Hour

// CapacityReport is the whole answer. Every field that could not be read has
// its error beside it, as text, so the JSON says "cannot tell" rather than 0.
type CapacityReport struct {
	Taken time.Time `json:"taken"`

	Volume    string `json:"volume"` // where UTM keeps its VMs
	DiskTotal int64  `json:"disk_total_bytes"`
	DiskFree  int64  `json:"disk_free_bytes"`
	DiskErr   string `json:"disk_error,omitempty"`

	Memory    int64  `json:"memory_bytes"`
	MemoryErr string `json:"memory_error,omitempty"`
	VMsErr    string `json:"vms_error,omitempty"`

	Policy CapacityPolicy `json:"policy"`

	VMs    []VMUsage    `json:"vms"`
	Data   []DataUsage  `json:"tool_data"`
	Owners []OwnerUsage `json:"owners"`

	RunningMemory int64 `json:"running_memory_bytes"` // configured, VMs not stopped and being made
	Promised      int64 `json:"promised_bytes"`       // free space the VMs here may still grow into
	Shared        int64 `json:"shared_bytes"`         // blocks clones share, counted once (see Capacity)
	Accounted     int64 `json:"accounted_bytes"`      // everything above, private plus shared once

	Room Room `json:"room"`
}

// CapacityPolicy is the model's numbers, so a reader of the JSON knows what
// the answers were worked out with.
type CapacityPolicy struct {
	VMMemory         int64  `json:"vm_memory_bytes"`    // irgo-win11 and installs
	CloneMemory      int64  `json:"clone_memory_bytes"` // clones of the golden image
	LinuxMemory      int64  `json:"linux_memory_bytes"` // Linux VMs
	MemoryReserve    int64  `json:"memory_reserve_bytes"`
	DiskReserve      int64  `json:"disk_reserve_bytes"`
	CloneReserve     int64  `json:"clone_reserve_bytes"`
	InstallReserve   int64  `json:"install_reserve_bytes"`
	Quota            Quota  `json:"quota"`
	QuotaErr         string `json:"quota_error,omitempty"`
	StaleAfterSecond int64  `json:"stale_after_s"`
}

// VMUsage is one VM UTM lists.
type VMUsage struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Kind      string `json:"kind"`  // owner (irgo-win11), golden, or vm
	OS        string `json:"os"`    // windows or linux, from its record; windows when it has none
	Owner     string `json:"owner"` // from its record; "" when it has none
	MemoryMiB int    `json:"memory_mib"`
	Allocated int64  `json:"allocated_bytes"` // st_blocks, shared blocks included
	Private   int64  `json:"private_bytes"`   // what deleting it frees
	Held      int64  `json:"held_bytes"`      // private, or its reserve if more
	Promised  int64  `json:"promised_bytes"`  // held - private
	IdleS     int64  `json:"idle_s,omitempty"`
	Stale     bool   `json:"stale"` // has a record, and idle past reportLease
	Err       string `json:"error,omitempty"`
}

// DataUsage is one directory or file the tool writes under Root.
type DataUsage struct {
	What      string `json:"what"`
	Path      string `json:"path"`
	Files     int    `json:"files"`
	Allocated int64  `json:"allocated_bytes"`
	Private   int64  `json:"private_bytes"`
	Bound     string `json:"bound"` // what keeps it from growing without limit
}

// OwnerUsage is what one caller holds.
type OwnerUsage struct {
	Owner string `json:"owner"`
	VMs   int    `json:"vms"`
	Held  int64  `json:"held_bytes"`
	Stale int    `json:"stale"`
}

// Room is how many more VMs fit, from the guard's own decision.
type Room struct {
	Clone       string `json:"clone"` // yes, no or cannot tell, for a caller with no VMs
	Why         string `json:"why"`
	MoreClones  int    `json:"more_clones"`  // by disk alone
	MoreRunning int    `json:"more_running"` // clones, by memory alone
}

// dataBounds says what bounds each part of Root: prune's limits, or the
// code that already keeps it small. Something under Root not named here was
// not written by this tool, and is reported as such.
var dataBounds = map[string]string{
	"media":             "kept: iso-delete removes the Windows media; the pinned Linux image stays; prune removes left-over scratch and Linux images of an older pin",
	"bin":               "prune: staged binaries unused for 7 days",
	"logs":              "irgo-winvm.log rotates at 8 MiB; prune: glaze run logs past 30 days or 100 MiB",
	"shots":             "prune: past 14 days or 200 MiB, the newest of each stage kept",
	"jobs":              "the 20 newest finished jobs are kept",
	"vm":                "the guest tools ISO; prune: staging/ left over for a day",
	"golden-pull":       "kept: an APFS clone of the golden image; vm-golden-pull -delete -force removes it; prune: .parts/ left for a day",
	"vms":               "one record per VM; vm-delete and vm-reap remove them",
	"ledger":            "the spool is capped at 4 MiB",
	"net":               "one small file per VM",
	"screens":           "not written by this version",
	"golden.json":       "one file",
	"utm-releases.json": "one file, doctor's cache of UTM's releases",
	"golden-export":     "not written by this tool: a copy made by hand for vm-golden-push -bundle",
}

// Capacity measures everything now.
//
// Shared blocks are counted once, as the most any one file shares: there is
// one golden image, its clones and copies share its blocks, and APFS does not
// say which file a clone shares with (apfsUsage). A VM installed from the ISO
// shares nothing.
func Capacity() CapacityReport {
	r := CapacityReport{Taken: time.Now().UTC(), VMs: []VMUsage{}, Data: []DataUsage{}, Owners: []OwnerUsage{}}
	q, qErr := QuotaFromEnv()
	r.Policy = CapacityPolicy{VMMemory: int64(vmMemoryMiB) << 20, CloneMemory: int64(cloneMemoryMiB) << 20,
		LinuxMemory:   int64(linuxMemoryMiB) << 20,
		MemoryReserve: hostMemoryReserveBytes,
		DiskReserve:   hostDiskReserveBytes, CloneReserve: cloneReserveBytes, InstallReserve: installReserveBytes,
		Quota: q, StaleAfterSecond: int64(reportLease / time.Second)}
	if qErr != nil {
		r.Policy.QuotaErr = qErr.Error()
	}

	f := capacityFacts{plan: CapacityPlan{VM: "(a new clone)", Disk: diskForClone}}
	f.host, f.hostErr = hostMemory()
	r.Memory = int64(f.host) //nolint:gosec // physical memory
	if f.hostErr != nil {
		r.MemoryErr = f.hostErr.Error()
	}
	if dir, err := DefaultVMDir(); err != nil {
		f.freeErr = err
	} else {
		r.Volume = dir
		r.DiskTotal, f.free, f.freeErr = volumeSize(FreeProbe(dir))
		r.DiskFree = f.free
	}
	if f.freeErr != nil {
		r.DiskErr = f.freeErr.Error()
	}
	f.vms, f.vmsErr = vmMemoryTable()
	if f.vmsErr != nil {
		r.VMsErr = f.vmsErr.Error()
	} else {
		f.pending = pendingCreates(f.vms)
	}

	disks := map[string]vmDisk{}
	measure := func(n string) vmDisk {
		d, ok := disks[n]
		if !ok {
			d = readVMDisk(n)
			disks[n] = d
		}
		return d
	}
	gatherDiskFacts(&f, measure)
	r.Promised = f.promised

	records, _, _ := VMRecords()
	byName := map[string]VMRecord{}
	for _, rec := range records {
		byName[strings.ToLower(rec.Name)] = rec
	}
	owners := map[string]*OwnerUsage{}
	now := time.Now()
	for _, v := range f.vms {
		u := VMUsage{Name: v.Name, Status: v.Status, Kind: "vm", OS: GuestWindows, MemoryMiB: v.MiB}
		switch {
		case strings.EqualFold(v.Name, DefaultVMName):
			u.Kind = "owner"
		case strings.EqualFold(v.Name, GoldenVMName):
			u.Kind = "golden"
		}
		if vmUsesMemory(v.Status) {
			r.RunningMemory += int64(v.MiB) << 20
		}
		d := measure(v.Name)
		if d.Err != nil {
			u.Err = d.Err.Error()
		} else {
			u.Allocated, u.Private, u.Held, u.Promised = d.Allocated, d.Private, held(v.Name, d), promised(v.Name, d)
			r.Shared = max(r.Shared, d.Allocated-d.Private)
			r.Accounted += d.Private
		}
		if rec, ok := byName[strings.ToLower(v.Name)]; ok {
			u.Owner = rec.Owner
			if rec.OS != "" {
				u.OS = rec.OS
			}
			idle := rec.Idle(now)
			u.IdleS = int64(idle / time.Second)
			u.Stale = idle > reportLease && !protectedVM(v.Name)
			o := owners[strings.ToLower(rec.Owner)]
			if o == nil {
				o = &OwnerUsage{Owner: rec.Owner}
				owners[strings.ToLower(rec.Owner)] = o
			}
			o.VMs++
			o.Held += u.Held
			if u.Stale {
				o.Stale++
			}
		}
		r.VMs = append(r.VMs, u)
	}
	r.RunningMemory += int64(len(f.pending)) * (int64(vmMemoryMiB) << 20)

	r.Data = toolData(&r.Shared, &r.Accounted)
	r.Accounted += r.Shared
	for _, o := range owners {
		r.Owners = append(r.Owners, *o)
	}
	sort.Slice(r.Owners, func(i, j int) bool { return r.Owners[i].Held > r.Owners[j].Held })

	a, why := decideCapacity(f)
	r.Room = Room{Clone: a.String(), Why: why}
	if f.freeErr == nil && f.promisedErr == nil {
		r.Room.MoreClones = int(max(0, (f.free-f.promised-hostDiskReserveBytes)/cloneReserveBytes))
	}
	if f.hostErr == nil && f.vmsErr == nil {
		r.Room.MoreRunning = int(max(0, (r.Memory-r.RunningMemory-hostMemoryReserveBytes)/(int64(cloneMemoryMiB)<<20)))
	}
	return r
}

// FreeProbe is path, or its nearest parent that exists, for a statfs.
func FreeProbe(path string) string {
	for p := path; ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil || filepath.Dir(p) == p {
			return p
		}
	}
}

// toolData measures each entry directly under Root, adding its private bytes
// to accounted and raising shared to the most any one file shares.
func toolData(shared, accounted *int64) []DataUsage {
	entries, err := os.ReadDir(appRoot())
	if err != nil {
		return []DataUsage{}
	}
	out := []DataUsage{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "mutation") && strings.HasSuffix(name, ".lock") {
			continue // empty, and never deleted (lock.go)
		}
		u := DataUsage{What: name, Path: filepath.Join(appRoot(), name), Bound: dataBounds[name]}
		if u.Bound == "" {
			u.Bound = "not written by this tool"
		}
		_ = filepath.WalkDir(u.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable entry is left out, not fatal
			}
			alloc, priv, aErr := apfsUsage(p)
			if aErr != nil {
				// Off APFS: the allocated size, every byte its own.
				alloc, _ = diskUsage(p)
				priv = alloc
			}
			u.Files++
			u.Allocated += alloc
			u.Private += priv
			*shared = max(*shared, alloc-priv)
			return nil
		})
		*accounted += u.Private
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Allocated > out[j].Allocated })
	return out
}
