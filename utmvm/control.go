package utmvm

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// utmctl is UTM's CLI, shipped inside the app bundle and not on PATH.
func utmctlPath() string { return AppPath + "/Contents/MacOS/utmctl" }

// VM identifies a virtual machine by name or UUID. utmctl accepts either, so
// callers can use whichever they have.
type VM struct{ Ref string }

// Named returns a handle to a VM. Nothing is validated until a call is made.
func Named(ref string) VM { return VM{Ref: ref} }

func (v VM) run(args ...string) (string, error) {
	cmd := exec.Command(utmctlPath(), append(args, v.Ref)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	combined := strings.TrimSpace(out.String() + errb.String())
	if err != nil {
		return combined, fmt.Errorf("utmctl %s: %w: %s", strings.Join(args, " "), err, combined)
	}
	return combined, nil
}

// Status returns "started", "stopped", "paused" or similar.
func (v VM) Status() (string, error) { return v.run("status") }

// Start powers on the VM. Starting an already-running VM is not an error.
func (v VM) Start() error { _, err := v.run("start"); return err }

// Stop requests a shutdown.
func (v VM) Stop() error { _, err := v.run("stop"); return err }

// IPAddress returns the guest's non-loopback addresses.
//
// This only works once the QEMU guest agent is installed, which on Windows
// means the UTM guest tools. A VM generated without them will always report
// the agent as missing — that is the tools being absent, not the guest being
// broken.
func (v VM) IPAddress() ([]string, error) {
	out, err := v.run("ip-address")
	if err != nil {
		return nil, err
	}
	// utmctl exits 0 and prints its complaint as ordinary output when the agent
	// is missing, so the exit status cannot be trusted here. Every line must be
	// validated as an address or a human-readable error is mistaken for one —
	// which made `status` cheerfully report a working agent on a VM that had
	// none.
	var ips []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		ip := net.ParseIP(line)
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		ips = append(ips, line)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no guest IP: %s", firstLine(out))
	}
	return ips, nil
}

// Exec runs a command in the guest and returns its output. Requires the guest
// agent.
func (v VM) Exec(cmdline ...string) (string, error) {
	args := append([]string{"exec", v.Ref, "--cmd"}, cmdline...)
	cmd := exec.Command(utmctlPath(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	combined := strings.TrimSpace(out.String() + errb.String())
	if err != nil {
		return combined, fmt.Errorf("exec in guest: %w: %s", err, combined)
	}
	return combined, nil
}

// AgentReady reports whether the guest agent is answering.
func (v VM) AgentReady() bool {
	_, err := v.IPAddress()
	return err == nil
}

// WaitForAgent blocks until the guest agent answers or the timeout elapses.
//
// This is the honest "is the VM actually usable" check. Status reports
// "started" the instant QEMU launches, long before Windows has booted — so
// polling status tells you nothing about whether you can do anything with it.
func (v VM) WaitForAgent(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v.AgentReady() {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("guest agent did not respond within %s — "+
		"if the VM was created without guest tools it never will", timeout)
}

// Entry is one row of utmctl list.
type Entry struct {
	UUID, Status, Name string
}

// List enumerates VMs UTM knows about.
//
// UTM only rescans its bundle directory at launch, so a VM generated while UTM
// is running will not appear here until UTM is restarted.
func List() ([]Entry, error) {
	out, err := exec.Command(utmctlPath(), "list").Output()
	if err != nil {
		return nil, fmt.Errorf("utmctl list: %w", err)
	}
	var entries []Entry
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // header
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		entries = append(entries, Entry{UUID: f[0], Status: f[1], Name: strings.Join(f[2:], " ")})
	}
	return entries, nil
}

// Find resolves a name or UUID to a known VM.
func Find(ref string) (Entry, error) {
	list, err := List()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range list {
		if strings.EqualFold(e.Name, ref) || strings.EqualFold(e.UUID, ref) {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no VM named %q (restart UTM if it was just generated)", ref)
}

// firstLine keeps error messages to one line; utmctl can be verbose.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
