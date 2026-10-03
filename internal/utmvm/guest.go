package utmvm

// The operating system inside a VM, described once.
//
// The host side of this package (the bundle, UTM, utmctl, the locks, the
// records, the capacity guard) does not care what a guest runs. The guest side
// does: where scratch files go, what a script is and what runs it, how an
// argument is quoted, how the guest says its own address, which script turns
// SSH on, and what machine it needs: which disk, display, clock and memory.
// Those answers were constants spread over app.go, vm_ssh.go, vm.go and the
// plist template. They are gathered here so each operation keeps one body and
// a second system supplies different data to it, not a second implementation.

import (
	_ "embed"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// guestOS answers what the shared code asks about a guest's system.
type guestOS struct {
	// name is the value of a VM record's os field.
	name string
	// label is the system's name in a message.
	label string

	// tempDir holds what a command needs only while it runs: the script that
	// is executing, its output and its exit code. publicDir holds what must
	// outlast it or be read by another account. sep joins a directory and a
	// name.
	tempDir, publicDir, sep string

	// scriptExt names a script file, and eol ends a line in a text file the
	// guest reads.
	scriptExt, eol string

	// script is the text of a script that runs cmds in order, stopping at the
	// first that fails, with their output in outFile and the exit code in
	// rcFile. utmctl exec returns neither, so this is how both get back.
	script func(cmds [][]string, outFile, rcFile string) string
	// runScript is the argv that runs a pushed script by its path.
	runScript func(path string) []string
	// remove is the argv that deletes files, missing ones included.
	remove func(paths ...string) []string

	// addrCmd makes the guest print its own addresses, after a line starting
	// addrHeader; addrs reads the IPv4 addresses out of what follows it.
	addrCmd    []string
	addrHeader string
	addrs      func(out string) []string

	// sshScript turns SSH on for one key, and with -Remove off again. It is
	// pushed as sshFile and run by sshRun, as sshAs. sshFirstRun is what to
	// expect of the first run.
	sshScript   string
	sshFile     string
	sshRun      func(path string, args []string) []string
	sshAs       string
	sshFirstRun string

	// quiet is the likely reason a VM that started has not answered.
	quiet string

	// The machine UTM is told to make for it (config.plist.tmpl): its icon
	// and note, whether it has a TPM, whether its clock is local time, its
	// display (empty: Config.DisplayHardware decides), its system disk's
	// interface, and the memory a new VM gets.
	icon, notes string
	tpm         bool
	localClock  bool
	display     string
	diskIface   DriveInterface
	memoryMiB   int

	// The golden image of this system (vm_golden.go): its name in UTM and its
	// manifest's file under the application root; the seal script, pushed as
	// sealFile and run one step at a time by sealRun, in sealSteps' order;
	// the command that shuts the guest down from inside after a pause, so the
	// batch running it can still record its exit code; and the memory a
	// clone of the image is made with.
	goldenName, goldenManifest string
	sealScript, sealFile       string
	sealRun                    func(path, step string) []string
	sealSteps                  []sealStep
	shutdown                   []string
	cloneMemoryMiB             int
}

// sealStep is one step of a seal script, what it is for, and how long it may
// take.
type sealStep struct {
	step, what string
	limit      time.Duration
}

// goldenVerify is the throwaway clone vm-golden-create boots to prove the
// image it just made boots.
func (g guestOS) goldenVerify() string { return g.goldenName + "-verify" }

// tempPath and publicPath are name inside the guest's two directories.
func (g guestOS) tempPath(name string) string   { return g.tempDir + g.sep + name }
func (g guestOS) publicPath(name string) string { return g.publicDir + g.sep + name }

// vmSSHScript does all of vm-ssh-create in a Windows guest, as SYSTEM, and
// with -Remove undoes it.
//
//go:embed assets/vm-ssh.ps1
var vmSSHScript string

// windowsGuest is Windows 11, as the answer file installs it.
var windowsGuest = guestOS{
	name:  GuestWindows,
	label: "Windows",

	// C:\Windows\Temp rather than the user profile: it exists on every
	// install and does not depend on which account the agent runs as.
	tempDir: `C:\Windows\Temp`,
	// C:\Users\Public for anything the INTERACTIVE session runs or reads. A
	// scheduled task running as the logged-in user cannot execute from
	// Windows\Temp: it completes with "Last AppResult: 1" and nothing else.
	// Verified by running the same batch from both: Public produced output,
	// Temp did not.
	publicDir: `C:\Users\Public`,
	sep:       `\`,

	scriptExt: ".bat",
	eol:       "\r\n",

	script:    batchSteps,
	runScript: func(path string) []string { return []string{"cmd.exe", "/c", path} },
	remove: func(paths ...string) []string {
		return []string{"cmd.exe", "/c", "del /q " + strings.Join(paths, " ")}
	},

	addrCmd:    []string{"ipconfig"},
	addrHeader: "Windows IP Configuration",
	addrs:      ipconfigIPv4,

	sshScript: vmSSHScript,
	sshFile:   "irgo-vm-ssh.ps1",
	sshRun: func(path string, args []string) []string {
		return append([]string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path}, args...)
	},
	sshAs:       "SYSTEM",
	sshFirstRun: "the first time it installs OpenSSH Server from Windows Update, which takes minutes",

	quiet: "It is probably Windows Update",

	icon:       "windows",
	notes:      "Generated by irgo-windows-vm. Unattended Windows 11 ARM64; login dev/dev, RDP enabled.",
	tpm:        true,
	localClock: true,
	// NVMe: Windows ARM64 has no inbox VirtIO driver, so Setup finds no drive
	// on a VirtIO disk.
	diskIface: IfaceNVMe,
	memoryMiB: vmMemoryMiB,

	goldenName:     GoldenVMName,
	goldenManifest: "golden.json",
	sealScript:     sealScript,
	sealFile:       "irgo-vm-golden-seal.ps1",
	sealRun: func(path, step string) []string {
		return []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path, "-Step", step}
	},
	// The limits are generous on purpose: decryption and DISM are minutes to
	// tens of minutes on a 30 GB install, and a limit that fires on a slow run
	// leaves a half-sealed VM for no gain.
	sealSteps: []sealStep{
		{"facts", "what is there before", 2 * time.Minute},
		{"decrypt", "turning BitLocker off and waiting for the decryption", 130 * time.Minute},
		{"hibernate", "turning hibernation off", 2 * time.Minute},
		{"cleanup", "cleaning up the component store (DISM /ResetBase), which takes minutes", 90 * time.Minute},
		{"trim", "TRIM, so freed blocks can become holes on the host", 30 * time.Minute},
		{"facts", "what is there after", 2 * time.Minute},
	},
	shutdown:       []string{"shutdown", "/s", "/t", "5"},
	cloneMemoryMiB: cloneMemoryMiB,
}

// vmSSHScriptLinux is vm-ssh-create and, with -Remove, vm-ssh-delete in a
// Linux guest, as root.
//
//go:embed assets/vm-ssh.sh
var vmSSHScriptLinux string

// linuxAddrHeader is printed by linuxGuest's address command before the
// addresses, so they can be told from the lines of the script before it.
const linuxAddrHeader = "irgo-winvm: addresses"

// linuxGuest is Ubuntu Server from its cloud image, as vm-create -os linux
// makes it (linux_vm.go). Measured by hand before any of it was written:
// docs/findings.md, "A Linux guest by hand".
var linuxGuest = guestOS{
	name:  GuestLinux,
	label: "Linux",

	// /tmp is emptied at boot, which suits files that live for one command.
	// /var/tmp is not, and both are there before any account is.
	tempDir:   "/tmp",
	publicDir: "/var/tmp",
	sep:       "/",

	scriptExt: ".sh",
	eol:       "\n",

	script:    shSteps,
	runScript: func(path string) []string { return []string{"/bin/sh", path} },
	remove:    func(paths ...string) []string { return append([]string{"/bin/rm", "-f"}, paths...) },

	// scope global leaves out loopback and link-local addresses.
	addrCmd:    []string{"/bin/sh", "-c", "echo '" + linuxAddrHeader + "'; ip -4 -o addr show scope global"},
	addrHeader: linuxAddrHeader,
	addrs:      ipAddrIPv4,

	sshScript: vmSSHScriptLinux,
	sshFile:   "irgo-vm-ssh.sh",
	sshRun: func(path string, args []string) []string {
		return append([]string{"/bin/sh", path}, args...)
	},
	sshAs:       "root",
	sshFirstRun: "the server is in the image, so this is seconds",

	quiet: "On a first boot cloud-init installs the guest agent from the network, and a VM " +
		"whose seed CD cloud-init did not read looks the same and never answers",

	icon:  "linux",
	notes: "Generated by irgo-windows-vm. Ubuntu Server 24.04 ARM64 from the cloud image; account dev, sudo without a password, no password set.",
	// The clock is UTC, as Linux expects, and there is no TPM: UTM's own
	// wizard adds one for Windows only.
	tpm:        false,
	localClock: false,
	// virtio-ramfb, not virtio-gpu-pci (what UTM's wizard picks): that one
	// shows nothing until a guest driver loads, so the firmware would be
	// invisible. No -gl: nothing here needs 3D.
	display: displayRAMFB,
	// VirtIO: the driver is in the kernel, and it is what UTM's wizard picks.
	diskIface: IfaceVirtIO,
	memoryMiB: linuxMemoryMiB,

	goldenName:     GoldenLinuxVMName,
	goldenManifest: "golden-linux.json",
	sealScript:     sealScriptLinux,
	sealFile:       "irgo-vm-golden-seal.sh",
	sealRun:        func(path, step string) []string { return []string{"/bin/sh", path, step} },
	sealSteps: []sealStep{
		{"facts", "what is there before", 2 * time.Minute},
		{"clean", "removing what makes the machine itself: machine-id, SSH host keys and keys, cloud-init's state; the network matched by name", 10 * time.Minute},
		{"trim", "fstrim, so freed blocks can become holes on the host", 10 * time.Minute},
		{"facts", "what is there after", 2 * time.Minute},
	},
	// Through systemd, after 5 s: the batch that asks still has to write its
	// exit code, and the host still has to pull it.
	shutdown:       []string{"systemd-run", "--on-active=5", "/bin/systemctl", "poweroff"},
	cloneMemoryMiB: linuxMemoryMiB,
}

// sealScriptLinux makes a Linux VM fit to be copied, as root.
//
//go:embed assets/vm-golden-seal.sh
var sealScriptLinux string

// guests is every system a VM record can name.
var guests = []guestOS{windowsGuest, linuxGuest}

// GuestWindows and GuestLinux are the names -os takes and a VM's record holds.
const (
	GuestWindows = "windows"
	GuestLinux   = "linux"
)

// GuestOSOf is the name of the system in the VM vmRef names: GuestWindows or
// GuestLinux. See guestOf for what an error means.
func GuestOSOf(vmRef string) (string, error) {
	g, err := guestOf(vmRef)
	return g.name, err
}

// GuestOSNamed checks a name given to -os, and returns it.
func GuestOSNamed(os string) (string, error) {
	for _, g := range guests {
		if g.name == os {
			return g.name, nil
		}
	}
	return "", fmt.Errorf("%q is not a system this tool makes VMs of (%s or %s)", os, GuestWindows, GuestLinux)
}

// shSteps is batchSteps for a POSIX shell: cmds run in order, the first that
// exits non-zero stops the rest and its code is the one recorded.
func shSteps(cmds [][]string, outFile, rcFile string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nrc=0\n")
	for i, argv := range cmds {
		redir := " > "
		if i > 0 {
			redir = " >> "
		}
		line := shQuote(argv) + redir + shQuote([]string{outFile}) + " 2>&1 || rc=$?"
		if i > 0 {
			line = "[ \"$rc\" -ne 0 ] || { " + line + "; }"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("echo \"$rc\" > " + shQuote([]string{rcFile}) + "\n")
	return b.String()
}

// shQuote renders argv for a POSIX shell, quoting only what needs it. Inside
// single quotes nothing is special, and a single quote is written by closing
// them, escaping it and opening them again.
func shQuote(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		if a != "" && strings.Trim(a, shPlain) == "" {
			parts = append(parts, a)
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}

// shPlain is every character a shell word may hold unquoted.
const shPlain = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:=@%+,"

// ipAddrIPv4 returns the usable IPv4 addresses in `ip -4 -o addr` output, one
// address a line as `2: enp0s1    inet 192.168.64.58/24 ...`: not loopback,
// and not the 169.254 address an interface gives itself.
func ipAddrIPv4(out string) []string {
	var ips []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] != "inet" {
				continue
			}
			addr, _, _ := strings.Cut(f[i+1], "/")
			ip := net.ParseIP(addr)
			if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				break
			}
			ips = append(ips, addr)
			break
		}
	}
	return ips
}

// ErrGuestOS is a VM whose system cannot be told, or is not one this tool
// knows.
var ErrGuestOS = errors.New("cannot tell which system is in the VM")

// guestNamed is the description of the system a record's os field names. An
// empty field is Windows: every VM made before the field existed is one.
func guestNamed(os string) (guestOS, error) {
	if os == "" {
		return windowsGuest, nil
	}
	for _, g := range guests {
		if g.name == os {
			return g, nil
		}
	}
	return guestOS{}, fmt.Errorf("%w: its record says %q, which this version does not know", ErrGuestOS, os)
}

// guestCache remembers the answer for a UUID for the life of the process: one
// command asks many times, and a UUID costs a `utmctl list` to resolve. A
// UUID only, never a name: a name can be deleted and made again as another
// system while an MCP server is still running, and a UUID cannot.
var guestCache sync.Map

// guestOf is the system inside the VM vmRef names, from its record
// (vm_lease.go). A VM with no record, or a record without the field, is
// Windows: irgo-win11, the golden image and every clone made before records
// said. A record that cannot be read is cannot tell, and an error: running a
// batch file in a guest that is not Windows fails in ways that name nothing.
func guestOf(vmRef string) (guestOS, error) {
	name, byUUID := vmRef, uuidRef.MatchString(vmRef)
	if byUUID {
		if g, ok := guestCache.Load(strings.ToUpper(vmRef)); ok {
			return g.(guestOS), nil
		}
		e, err := Find(vmRef)
		if err != nil {
			return guestOS{}, fmt.Errorf("%w %s: %w", ErrGuestOS, vmRef, err)
		}
		name = e.Name
	}
	// A golden image and its verification clone have no record; their names
	// say which system they hold.
	if g, ok := goldenGuest(name); ok {
		return g, nil
	}
	r, _, err := readRecord(name)
	if err != nil {
		return guestOS{}, fmt.Errorf("%w %s: %w", ErrGuestOS, name, err)
	}
	g, err := guestNamed(r.OS)
	if err != nil {
		return guestOS{}, fmt.Errorf("%s: %w", name, err)
	}
	if byUUID {
		guestCache.Store(strings.ToUpper(vmRef), g)
	}
	return g, nil
}

// goldenGuest is the system whose golden image, or its verification clone,
// is called name.
func goldenGuest(name string) (guestOS, bool) {
	for _, g := range guests {
		if strings.EqualFold(name, g.goldenName) || strings.EqualFold(name, g.goldenVerify()) {
			return g, true
		}
	}
	return guestOS{}, false
}
