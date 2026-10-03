package utmvm

// The capacity model: what a VM costs this Mac in memory and disk, what is
// kept back for macOS, and how much one caller may hold. One place, read by
// vm-create's guard (vm_capacity.go), `capacity` (capacity.go) and doctor, so
// the answer to "is there room?" and the report of "how much room is there"
// cannot disagree. docs/USING.md, "Is there room?", and docs/ARCHITECTURE.md, "The capacity model", have the reasoning; the
// measurements are in docs/RESULTS.md.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// vmMemoryMiB is what every VM is made with (setDefaults), and what a VM that
// does not exist yet is counted as needing. A clone has its golden image's,
// which was made with this.
const vmMemoryMiB = 8192

// cloneMemoryMiB is what a clone of the golden image is made with: half the
// image's, set in UTM's configuration at clone time and read back
// (cloneVM). irgo-win11 and installs keep vmMemoryMiB. On a 16 GiB Mac that
// is the difference between no clone while irgo-win11 runs (16 - 8 - 8 = 0
// left, want 4) and one (16 - 8 - 4 = 4). A 4 GiB clone passed glaze-check
// -windows and vm-check (docs/RESULTS.md, "VM capacity").
const cloneMemoryMiB = 4096

// linuxMemoryMiB is what a Linux VM is made with. A server image with no
// desktop: 275 MiB in use after its first boot (docs/RESULTS.md, "A Linux
// guest by hand"). On a 16 GiB Mac one fits beside irgo-win11 and a Windows
// clone both stopped or either running, and beside both running it does not
// (16 - 8 - 4 - 2 = 2 left, want 4).
const linuxMemoryMiB = 2048

// hostMemoryReserveBytes is the memory left for macOS and the owner's own
// work after every VM has its configured memory. 4 GiB: the system alone sits
// around 3 GiB, and on 1 Oct 2026 a 16 GiB Mac with one 8 GiB VM running was
// already 6.3 GB into swap. On 16 GiB this allows one VM (16 - 8 = 8 left),
// and refuses a second (0 left); on 32 GiB, three.
const hostMemoryReserveBytes = 4 << 30

// hostDiskReserveBytes is the free space kept for macOS after every VM has
// what it is promised. macOS keeps its swap on this volume (7 to 10 GB of it
// with one and two VMs running, measured 1 Oct 2026) and warns at about 5 GB
// free; 10 GiB keeps it out of that.
const hostDiskReserveBytes = 10 << 30

// cloneReserveBytes is how far each VM is allowed to grow past what the
// golden image already holds for it: a new clone needs this much free, and an
// existing one keeps whatever part of it it has not yet written.
//
// Measured 1 Oct 2026 (one clone, APFS private bytes): +0.03 GiB after its
// boot, 0.10 GiB after glaze-check -windows, 0.26 GiB after 20 app-creates of
// a 19 MB binary, and flat at 0.28 GiB for the next 90 minutes. 4 GiB is
// fourteen times that, for what one session did not show: a cumulative
// Windows update downloads about 1 GB and stages two to three times that
// (an estimate, not measured on a clone). A clone past it is still allowed to
// grow; it simply stops counting as promised space.
const cloneReserveBytes = 4 << 30

// installReserveBytes is what a VM installed from the ISO takes: about 30 GiB
// once Windows is on it ("What it costs" in docs/USING.md). It also
// covers vm-create pulling the golden image when there is none here: 8.4 GB
// of chunks, then the bundle rebuilt (19 GB of data, measured 1 Oct 2026).
const installReserveBytes = 30 << 30

// linuxReserveBytes is what a Linux VM made from the cloud image needs free:
// the image as a raw disk holds 2.5 GiB before its first boot and 4.3 GiB
// after one and three more boots (measured 2 Oct 2026), and it is then
// allowed to grow like any VM. Twice cloneReserveBytes covers both. The
// download itself, 0.6 GB once, is inside hostDiskReserveBytes's margin.
const linuxReserveBytes = 2 * cloneReserveBytes

// Quota is how much one caller (an owner in vms/) may hold. Zero means no
// limit.
type Quota struct {
	VMs   int   `json:"vms"`
	Bytes int64 `json:"bytes"` // held: each VM's private bytes or its reserve, whichever is more
}

// The quota every caller gets unless IRGO_WINVM_QUOTA_VMS or
// IRGO_WINVM_QUOTA_GIB says otherwise. Two VMs: one to work in and one to try
// something in, which is what the agents on this Mac have asked for; a third
// is usually a forgotten one. 16 GiB: two fresh clones hold 8 GiB, so each can
// grow by another 4 before the owner has to delete one.
const (
	defaultQuotaVMs   = 2
	defaultQuotaBytes = 16 << 30
)

// Quota environment variables. 0 turns a limit off.
const (
	QuotaVMsEnv = "IRGO_WINVM_QUOTA_VMS"
	QuotaGiBEnv = "IRGO_WINVM_QUOTA_GIB"
)

// QuotaFromEnv is the quota in force, and an error naming the variable when
// one does not parse: a typo is not a quiet "no limit".
func QuotaFromEnv() (Quota, error) {
	return parseQuota(os.Getenv(QuotaVMsEnv), os.Getenv(QuotaGiBEnv))
}

func parseQuota(vms, gib string) (Quota, error) {
	q := Quota{VMs: defaultQuotaVMs, Bytes: defaultQuotaBytes}
	if s := strings.TrimSpace(vms); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return Quota{}, fmt.Errorf("%s=%q: want a whole number of VMs, 0 for no limit", QuotaVMsEnv, vms)
		}
		q.VMs = n
	}
	if s := strings.TrimSpace(gib); s != "" {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || n < 0 {
			return Quota{}, fmt.Errorf("%s=%q: want GiB, 0 for no limit", QuotaGiBEnv, gib)
		}
		q.Bytes = int64(n * (1 << 30))
	}
	return q, nil
}

// vmDisk is what one VM's system disk costs, by APFS's own accounting.
type vmDisk struct {
	Allocated int64 // every block, shared ones included
	Private   int64 // the blocks no other file shares: what deleting it frees
	Err       error
}

// readVMDisk measures name's system disk where UTM keeps it.
func readVMDisk(name string) vmDisk {
	b, err := BundlePath(name)
	if err != nil {
		return vmDisk{Err: err}
	}
	var d vmDisk
	d.Allocated, d.Private, d.Err = apfsUsage(DiskPath(b))
	return d
}

// held is what a VM holds of the disk: what it has written of its own, or
// its reserve when it has written less. A golden image holds only what it
// has written: it stays stopped and never grows.
func held(name string, d vmDisk) int64 {
	if IsGoldenImage(name) {
		return d.Private
	}
	return max(d.Private, cloneReserveBytes)
}

// promised is the part of a VM's reserve it has not used yet: space that is
// free now and must stay free for it.
func promised(name string, d vmDisk) int64 { return held(name, d) - d.Private }

// errVMDisk is a VM whose disk could not be measured; the guard cannot tell
// how much is promised, and refuses.
var errVMDisk = errors.New("cannot measure a VM's disk")
