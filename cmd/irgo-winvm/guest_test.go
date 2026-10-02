package main

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// fakeVMs makes findVM and guestOSOf answer from a map of VM name to system,
// "?" being a VM whose system cannot be told, until the test ends.
func fakeVMs(t *testing.T, vms map[string]string) {
	t.Helper()
	oldFind, oldOS := findVM, guestOSOf
	t.Cleanup(func() { findVM, guestOSOf = oldFind, oldOS })
	findVM = func(name string) (utmvm.Entry, error) {
		if _, ok := vms[name]; ok {
			return utmvm.Entry{Name: name, UUID: "U-" + name, Status: "stopped"}, nil
		}
		return utmvm.Entry{}, utmvm.ErrNoVM
	}
	guestOSOf = func(name string) (string, error) {
		switch os, ok := vms[name]; {
		case !ok:
			return utmvm.GuestWindows, nil // no record: Windows
		case os == "?":
			return "", utmvm.ErrGuestOS
		default:
			return os, nil
		}
	}
}

// TestCreateOS: -os decides only for a VM that does not exist; one that does
// keeps the system in its record, and an -os that disagrees is exit 2. A
// Linux VM needs a name of its own, and not one that means Windows.
//
// Negative controls, run by hand 2 Oct 2026: without the osGiven test the
// existing Linux VM with no -os is refused (the flag's default is windows);
// without the loop over the reserved names "-os linux, no -vm" returns linux
// for irgo-win11.
func TestCreateOS(t *testing.T) {
	fakeVMs(t, map[string]string{"l1": utmvm.GuestLinux, "w1": utmvm.GuestWindows, "q1": "?"})
	c, _ := find("vm-create")
	for _, tc := range []struct {
		name  string
		args  []string
		want  string // "" is an error; usage says which
		usage bool
	}{
		{"a new VM, no -os", []string{"-vm", "n1"}, utmvm.GuestWindows, false},
		{"the default VM, no -os", nil, utmvm.GuestWindows, false},
		{"a new Linux VM", []string{"-os", "linux", "-vm", "n1"}, utmvm.GuestLinux, false},
		{"-os linux, no -vm", []string{"-os", "linux"}, "", true},
		{"-os linux under the default VM's name", []string{"-os", "linux", "-vm", "IRGO-WIN11"}, "", true},
		{"-os linux under the golden image's name", []string{"-os", "linux", "-vm", utmvm.GoldenVMName}, "", true},
		{"a system that does not exist", []string{"-os", "plan9", "-vm", "n1"}, "", true},
		{"an existing Linux VM, no -os", []string{"-vm", "l1"}, utmvm.GuestLinux, false},
		{"an existing Linux VM, -os linux", []string{"-vm", "l1", "-os", "linux"}, utmvm.GuestLinux, false},
		{"an existing Linux VM, -os windows", []string{"-vm", "l1", "-os", "windows"}, "", true},
		{"an existing Windows VM, -os linux", []string{"-vm", "w1", "-os", "linux"}, "", true},
		{"an existing VM whose system cannot be told", []string{"-vm", "q1"}, "", false},
	} {
		v, _, err := c.parse(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		got, err := createOS(v, v.String("vm"))
		switch {
		case tc.want != "" && (err != nil || got != tc.want):
			t.Errorf("%s: %q, %v; want %s", tc.name, got, err, tc.want)
		case tc.want == "" && err == nil:
			t.Errorf("%s: %q, want an error", tc.name, got)
		case tc.want == "" && errors.Is(err, errUsage) != tc.usage:
			t.Errorf("%s: %v; usage error: want %v", tc.name, err, tc.usage)
		}
	}
}

// TestWindowsOnlyCommandsRefuseALinuxVM: what pushes a .exe, a batch file or
// PowerShell into a guest is refused on a Linux VM with exit 2 and a sentence
// naming the VM, before it takes a lock; a VM whose system cannot be told is
// refused too. The commands that work on both are not.
//
// Negative control, run by hand 2 Oct 2026: remove the windowsGuest call
// from runToolFor and app-create on l1 reaches its own usage error, which
// does not name the VM.
func TestWindowsOnlyCommandsRefuseALinuxVM(t *testing.T) {
	fakeVMs(t, map[string]string{"l1": utmvm.GuestLinux, "w1": utmvm.GuestWindows, "q1": "?"})
	check := func(name string, args ...string) error {
		t.Helper()
		c, ok := find(name)
		if !ok {
			t.Fatalf("no command %s", name)
		}
		v, _, err := c.parse(args)
		if err != nil {
			t.Fatal(err)
		}
		return windowsGuest(c, v)
	}
	for _, name := range []string{"app-create", "app-delete", "vm-repair", "vm-check", "vm-golden-create"} {
		err := check(name, "-vm", "l1")
		if !errors.Is(err, errUsage) || !strings.Contains(err.Error(), "l1 is a linux VM") {
			t.Errorf("%s on a Linux VM: %v, want a usage error naming it", name, err)
		}
		if exitCode(err) != command.CodeUsage {
			t.Errorf("%s on a Linux VM exits %d, want %d", name, exitCode(err), command.CodeUsage)
		}
		if err := check(name, "-vm", "w1"); err != nil {
			t.Errorf("%s on a Windows VM: %v", name, err)
		}
		if err := check(name, "-vm", "q1"); !errors.Is(err, utmvm.ErrGuestOS) {
			t.Errorf("%s on a VM whose system cannot be told: %v, want ErrGuestOS", name, err)
		}
	}
	if err := check("glaze-check", "-windows", "-vm", "l1"); !errors.Is(err, errUsage) {
		t.Errorf("glaze-check -windows on a Linux VM: %v, want refused", err)
	}
	if err := check("glaze-check", "-vm", "l1"); err != nil {
		t.Errorf("glaze-check on the Mac touches no VM: %v", err)
	}
	for _, name := range []string{"vm-ssh-create", "vm-ssh-delete", "vm-delete", "vm-screen", "vm-create"} {
		if err := check(name, "-vm", "l1"); err != nil {
			t.Errorf("%s works on a Linux VM, and was refused: %v", name, err)
		}
	}
	// Through the one path every command takes, so the guard is wired in.
	// On macOS only: elsewhere app-create is refused before it, as macOS-only.
	if runtime.GOOS != "darwin" {
		return
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(utmvm.OwnerEnv, "")
	if err := runTool("app-create", []string{"-vm", "l1"}); err == nil || !strings.Contains(err.Error(), "l1 is a linux VM") {
		t.Errorf("app-create -vm l1 through runTool: %v, want the refusal", err)
	}
}
