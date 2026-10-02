package utmvm

// The operating system inside a VM, described once.
//
// The host side of this package (the bundle, UTM, utmctl, the locks, the
// records, the capacity guard) does not care what a guest runs. The guest side
// does: where scratch files go, what a script is and what runs it, how an
// argument is quoted, how the guest says its own address, which script turns
// SSH on. Those answers were constants spread over app.go, vm_ssh.go and
// vm.go. They are gathered here so each operation keeps one body and a second
// system supplies different data to it, not a second implementation.

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"
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
}

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
	name:  "windows",
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
}

// guests is every system a VM record can name.
var guests = []guestOS{windowsGuest}

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

// guestCache remembers each reference's answer for the life of the process:
// one command asks many times, and a UUID costs a `utmctl list` to resolve.
var guestCache sync.Map

// guestOf is the system inside the VM vmRef names, from its record
// (vm_lease.go). A VM with no record, or a record without the field, is
// Windows: irgo-win11, the golden image and every clone made before records
// said. A record that cannot be read is cannot tell, and an error: running a
// batch file in a guest that is not Windows fails in ways that name nothing.
func guestOf(vmRef string) (guestOS, error) {
	if g, ok := guestCache.Load(vmRef); ok {
		return g.(guestOS), nil
	}
	name := vmRef
	if uuidRef.MatchString(vmRef) {
		e, err := Find(vmRef)
		if err != nil {
			return guestOS{}, fmt.Errorf("%w %s: %w", ErrGuestOS, vmRef, err)
		}
		name = e.Name
	}
	r, _, err := readRecord(name)
	if err != nil {
		return guestOS{}, fmt.Errorf("%w %s: %w", ErrGuestOS, name, err)
	}
	g, err := guestNamed(r.OS)
	if err != nil {
		return guestOS{}, fmt.Errorf("%s: %w", name, err)
	}
	guestCache.Store(vmRef, g)
	return g, nil
}
