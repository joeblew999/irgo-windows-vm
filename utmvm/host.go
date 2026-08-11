package utmvm

import "runtime"

// Target is a desktop build a developer might need to run.
type Target string

const (
	TargetMacOS   Target = "macos"
	TargetWindows Target = "windows"
	TargetLinux   Target = "linux"
)

// Coverage describes how — or whether — the current host can run a target.
type Coverage struct {
	Target Target
	How    string // "native", "vm", or "" when unavailable
	Note   string
}

// Runnable reports whether the target can actually be exercised here.
func (c Coverage) Runnable() bool { return c.How != "" }

// HostCoverage reports what desktop builds this machine can genuinely run.
//
// The asymmetry is the point, and it drives which machine a team should give
// to whoever owns the desktop app:
//
//   - macOS on Apple Silicon is a superset. It runs the macOS build natively
//     and the Windows build in a VM, because UTM virtualises Windows ARM64
//     with hardware acceleration.
//   - Windows can only run the Windows build. There is no UTM for Windows, and
//     nesting a macOS guest is not permitted by Apple's licence regardless.
//   - Linux can run the Linux build, and Windows via KVM — but not from this
//     tool, which generates UTM bundles.
//
// A developer on Windows is therefore not doing anything wrong when the VM
// commands refuse to run; they are simply on the narrower machine. Saying so
// plainly beats an error about a missing directory.
func HostCoverage() []Coverage {
	switch runtime.GOOS {
	case "darwin":
		cov := []Coverage{
			{TargetMacOS, "native", "runs directly on this machine"},
		}
		if runtime.GOARCH == "arm64" {
			cov = append(cov, Coverage{TargetWindows, "vm",
				"Windows 11 ARM64 under UTM, hardware-accelerated via HVF"})
		} else {
			cov = append(cov, Coverage{TargetWindows, "",
				"Intel Macs would emulate ARM64 or run x64 Windows unaccelerated; not worth it"})
		}
		cov = append(cov, Coverage{TargetLinux, "",
			"buildable here, but this tool only generates Windows VMs"})
		return cov

	case "windows":
		return []Coverage{
			{TargetWindows, "native", "runs directly on this machine"},
			{TargetMacOS, "", "macOS cannot be virtualised on non-Apple hardware"},
			{TargetLinux, "", "possible via WSL2 or Hyper-V; out of scope for this tool"},
		}

	case "linux":
		return []Coverage{
			{TargetLinux, "native", "runs directly on this machine"},
			{TargetWindows, "", "possible via KVM (see dockur/windows); this tool generates UTM bundles"},
			{TargetMacOS, "", "macOS cannot be virtualised on non-Apple hardware"},
		}
	}
	return nil
}

// CanCreateVMs reports whether VM subcommands can do anything here. Callers
// should prefer this over comparing runtime.GOOS so the reason stays in one
// place.
func CanCreateVMs() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}
