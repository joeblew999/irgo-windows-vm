// Package device reads the machine it runs on into the sections of a
// fleet-api device report (github.com/joeblew999/fleet-api, api/device.go):
// host, CPU, memory, disks, power, battery, lid and sleep. The envelope, the
// tool and the keeper sections are the caller's, which knows them.
//
// Every section answers ok with its values, none (this machine has no such
// thing) or unknown with the reason, never a zero that looks like data. Host,
// CPU, memory and disks are read by gopsutil on every OS; power, battery, lid
// and sleep are read on macOS from pmset and ioreg, and are unknown elsewhere
// until they are measured there.
//
// It knows nothing of UTM and builds on every OS.
package device

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Snapshot is one reading of the machine.
type Snapshot struct {
	Host    *fleet.DeviceHost
	CPU     *fleet.DeviceCPU
	Memory  *fleet.DeviceMemory
	Disks   []*fleet.DeviceDisk
	Power   *fleet.DevicePower
	Battery *fleet.DeviceBattery
	Lid     *fleet.DeviceLid
	Sleep   *fleet.DeviceSleep
}

// Read reads this machine. dataDir is where the tool keeps its work, whose
// volume is the report's data disk. It never fails: what cannot be read is
// unknown, with the reason.
func Read(dataDir string) Snapshot {
	s := Snapshot{
		Host:   readHost(),
		CPU:    readCPU(),
		Memory: readMemory(),
		Disks:  readDisks(dataDir),
	}
	s.Power, s.Battery, s.Lid, s.Sleep = readPower()
	return s
}

func readHost() *fleet.DeviceHost {
	name, _ := os.Hostname()
	name, _, _ = strings.Cut(name, ".")
	if name == "" {
		name = "unknown"
	}
	h := &fleet.DeviceHost{Name: strings.ToLower(name), Os: fleet.DeviceHostOs(runtime.GOOS), Arch: runtime.GOARCH}
	if info, err := host.Info(); err == nil {
		if n := osName(runtime.GOOS, info.Platform); n != "" {
			h.OsName = fleet.String(n)
		}
		if info.PlatformVersion != "" {
			h.OsVersion = fleet.String(info.PlatformVersion)
		}
		if info.BootTime > 0 {
			h.Boot = fleet.Int64(int64(info.BootTime) * 1000)
		}
		switch info.VirtualizationRole {
		case "guest":
			h.Guest = fleet.Bool(true)
		case "host":
			h.Guest = fleet.Bool(false)
		}
	}
	if m := model(); m != "" {
		h.Model = fleet.String(m)
	}
	if g, ok := guest(); ok {
		h.Guest = fleet.Bool(g)
	}
	return h
}

// osName is the system's name as a person says it.
func osName(goos, platform string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "windows":
		return platform // "Microsoft Windows 11 Pro"
	}
	if platform == "" {
		return ""
	}
	return strings.ToUpper(platform[:1]) + platform[1:] // ubuntu -> Ubuntu
}

func readCPU() *fleet.DeviceCPU {
	n, err := cpu.Counts(true)
	if err != nil || n < 1 {
		return &fleet.DeviceCPU{Status: fleet.DeviceCPUStatusUnknown, Why: why("counting the processors", err)}
	}
	c := &fleet.DeviceCPU{Status: fleet.DeviceCPUStatusOk, Count: fleet.Int(n)}
	if runtime.GOOS != "windows" { // Windows has no load average
		if l, err := load.Avg(); err == nil && l.Load1 >= 0 {
			c.Load1 = fleet.Float64(round2(l.Load1))
		}
	}
	return c
}

func readMemory() *fleet.DeviceMemory {
	v, err := mem.VirtualMemory()
	if err != nil || v.Total == 0 {
		return &fleet.DeviceMemory{Status: fleet.DeviceMemoryStatusUnknown, Why: why("reading memory", err)}
	}
	return &fleet.DeviceMemory{Status: fleet.DeviceMemoryStatusOk,
		Total: fleet.Int64(int64(v.Total)), Available: fleet.Int64(int64(min(v.Available, v.Total)))}
}

// readDisks is the system volume and dataDir's, one entry with both roles
// when they are the same volume.
func readDisks(dataDir string) []*fleet.DeviceDisk {
	sys := systemRoot()
	sysMount, dataMount := mountOf(sys), mountOf(dataDir)
	if sysMount == dataMount {
		return []*fleet.DeviceDisk{readDisk(sysMount, fleet.DeviceDiskRolesItemSystem, fleet.DeviceDiskRolesItemData)}
	}
	return []*fleet.DeviceDisk{
		readDisk(sysMount, fleet.DeviceDiskRolesItemSystem),
		readDisk(dataMount, fleet.DeviceDiskRolesItemData),
	}
}

func readDisk(path string, roles ...fleet.DeviceDiskRolesItem) *fleet.DeviceDisk {
	d := &fleet.DeviceDisk{Roles: roles, Path: home(path)}
	u, err := disk.Usage(path)
	if err != nil || u.Total == 0 {
		d.Status, d.Why = fleet.DeviceDiskStatusUnknown, why("reading "+home(path), err)
		return d
	}
	d.Status = fleet.DeviceDiskStatusOk
	d.Total, d.Free = fleet.Int64(int64(u.Total)), fleet.Int64(int64(min(u.Free, u.Total)))
	if u.Fstype != "" {
		d.Fs = fleet.String(u.Fstype)
	}
	return d
}

// systemRoot is the system volume's path.
func systemRoot() string {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("SystemDrive"); d != "" {
			return d + `\`
		}
		return `C:\`
	}
	return "/"
}

// home writes the home directory as ~, as a report must.
func home(p string) string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return p
	}
	if p == h || strings.HasPrefix(p, h+string(os.PathSeparator)) {
		return "~" + p[len(h):]
	}
	return p
}

// why is the reason a section is unknown, at most 200 bytes as the schema
// allows.
func why(what string, err error) *string {
	s := what + ": no answer"
	if err != nil {
		s = fmt.Sprintf("%s: %v", what, err)
	}
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return fleet.String(s)
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// commandTimeout bounds each program run to read the machine.
const commandTimeout = 5 * time.Second
