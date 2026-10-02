package utmvm

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SSH into a guest: the OpenSSH server turned on, one public key allowed in.

// Everything in the guest is one script, which the guest's description names
// (guestOS.sshScript): pushed and run through the agent, like vm-repair's,
// because that is the one way anything here runs in the guest and is checked.

const (
	// sshPort is where sshd listens. The script opens the same number.
	sshPort = "22"

	// sshKeyFileMax bounds what is read as a public key. The largest real
	// one, an RSA key of 16384 bits, is under 3 KiB.
	sshKeyFileMax = 16 << 10

	// sshAnswerWait is how long port 22 is given to answer from the Mac once
	// the guest says sshd is listening and the firewall rule is in.
	sshAnswerWait = 30 * time.Second
)

// ErrSSHKey is a -key that cannot be used: unreadable, not one public key, or
// a private key.
var ErrSSHKey = errors.New("not a usable SSH public key")

// SSHKey is one public key, as a line of an authorized_keys file.
type SSHKey struct {
	Type    string // ssh-ed25519, ssh-rsa, ...
	Blob    string // the key itself, base64
	Comment string
}

// Line is the key as authorized_keys holds it.
func (k SSHKey) Line() string {
	return strings.TrimSpace(k.Type + " " + k.Blob + " " + k.Comment)
}

// ReadSSHPublicKey reads the one public key in the file at path, where a
// leading ~/ is the home directory. A private key is refused by name, before
// anything of it is kept: only what this returns is ever sent to a guest.
func ReadSSHPublicKey(path string) (SSHKey, error) {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return SSHKey{}, fmt.Errorf("%w: %s: %v", ErrSSHKey, path, err)
		}
		path = filepath.Join(home, rest)
	}
	info, err := os.Stat(path)
	if err != nil {
		return SSHKey{}, fmt.Errorf("%w: %v (make one with `ssh-keygen -t ed25519`, or pass -key <file.pub>)", ErrSSHKey, err)
	}
	if info.Size() > sshKeyFileMax {
		return SSHKey{}, fmt.Errorf("%w: %s is %s, and a public key is under %s", ErrSSHKey, Home(path), HumanBytes(info.Size()), HumanBytes(sshKeyFileMax))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SSHKey{}, fmt.Errorf("%w: %v", ErrSSHKey, err)
	}
	k, err := parseSSHPublicKey(data)
	if err != nil {
		return SSHKey{}, fmt.Errorf("%w: %s: %v", ErrSSHKey, Home(path), err)
	}
	return k, nil
}

// parseSSHPublicKey reads one key in the form `type base64 [comment]`, and
// checks that the base64 is a key of the type the line says it is: the blob
// starts with its own type name (RFC 4253, section 6.6).
func parseSSHPublicKey(data []byte) (SSHKey, error) {
	if bytes.Contains(data, []byte("PRIVATE KEY")) {
		return SSHKey{}, errors.New("this is a private key, which never leaves this Mac; pass the .pub file beside it")
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		return SSHKey{}, fmt.Errorf("holds %d keys, want exactly one", len(lines))
	}
	f := strings.Fields(lines[0])
	if len(f) < 2 {
		return SSHKey{}, errors.New("is not `type base64 [comment]`")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return SSHKey{}, fmt.Errorf("the key is not base64: %v", err)
	}
	if len(blob) < 4 {
		return SSHKey{}, errors.New("the key is too short to name its type")
	}
	n := binary.BigEndian.Uint32(blob)
	if uint64(n) > uint64(len(blob)-4) || string(blob[4:4+n]) != f[0] {
		return SSHKey{}, fmt.Errorf("the line says %q and the key inside it does not", f[0])
	}
	return SSHKey{Type: f[0], Blob: f[1], Comment: strings.Join(f[2:], " ")}, nil
}

// VMSSHCreate turns the guest's OpenSSH server on, allows key in for user, and
// returns the address at which port 22 answered from this Mac with an SSH
// banner. Everything in the guest is its system's ssh script (guest.go); it
// says what it changed and what was already so, one line each through say.
//
// The address is the guest's own account of itself (its address command, in
// the same batch), not the cached one pushes use: a stale address here would
// name another VM in the line the caller is told to connect to.
func VMSSHCreate(vmRef, user string, key SSHKey, timeout time.Duration, say func(string, ...any)) (string, error) {
	g, err := guestOf(vmRef)
	if err != nil {
		return "", err
	}
	script := g.publicPath(g.sshFile)
	if err := pushScript(vmRef, script, g.sshScript); err != nil {
		return "", fmt.Errorf("pushing the ssh script: %w", err)
	}
	keyGuest := g.publicPath("irgo-vm-ssh-key.pub")
	if err := pushScript(vmRef, keyGuest, key.Line()+g.eol); err != nil {
		return "", fmt.Errorf("pushing the public key: %w", err)
	}
	say("running it in the guest as %s; %s", g.sshAs, g.sshFirstRun)
	ips, err := runSSHScript(vmRef, g, []string{"-User", user, "-KeyFile", keyGuest}, timeout, say)
	if err != nil {
		return "", err
	}
	addrs := make([]string, len(ips))
	for i, ip := range ips {
		addrs[i] = net.JoinHostPort(ip, sshPort)
	}
	say("waiting for port %s to answer from this Mac at %s", sshPort, strings.Join(ips, ", "))
	addr, banner, err := waitForSSH(addrs, sshAnswerWait)
	if err != nil {
		return "", fmt.Errorf("sshd is listening in the guest, and from this Mac: %w", err)
	}
	host, _, _ := net.SplitHostPort(addr)
	say("%s answers: %s", addr, banner)
	return host, nil
}

// VMSSHDelete turns SSH off again: sshd stopped and disabled, the firewall
// rule and every authorized key removed, and port 22 checked from this Mac to
// have stopped answering. The capability stays installed.
func VMSSHDelete(vmRef string, say func(string, ...any)) error {
	if !Named(vmRef).AgentReady() {
		return fmt.Errorf("%w: %s, so SSH cannot be turned off in it; start it and run this again", ErrNoAgent, vmRef)
	}
	g, err := guestOf(vmRef)
	if err != nil {
		return err
	}
	if err := pushScript(vmRef, g.publicPath(g.sshFile), g.sshScript); err != nil {
		return fmt.Errorf("pushing the ssh script: %w", err)
	}
	ips, err := runSSHScript(vmRef, g, []string{"-Remove"}, 5*time.Minute, say)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		addr := net.JoinHostPort(ip, sshPort)
		if banner, err := sshBanner(addr, shareDialTimeout); err == nil {
			return fmt.Errorf("the guest says SSH is off, and %s still answers: %s", addr, banner)
		}
		say("%s no longer answers", addr)
	}
	return nil
}

// runSSHScript runs the pushed script with args, then the guest's address
// command, in one batch, says the script's lines, and returns the guest's
// IPv4 addresses.
func runSSHScript(vmRef string, g guestOS, args []string, timeout time.Duration, say func(string, ...any)) ([]string, error) {
	res, err := appExecSteps(vmRef, [][]string{g.sshRun(g.publicPath(g.sshFile), args), g.addrCmd}, timeout, say)
	if err != nil {
		return nil, err
	}
	lines, ips := splitSSHOutput(g, res.Stdout)
	for _, l := range lines {
		say("%s", l)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("the ssh script exited %d in the guest", res.ExitCode)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("the ssh script succeeded, and the guest's %s reported no IPv4 address", g.addrCmd[0])
	}
	return ips, nil
}

// splitSSHOutput separates what the script printed from the address command's
// output that follows it, and reads the addresses out of the latter.
func splitSSHOutput(g guestOS, stdout string) (lines, ips []string) {
	script, addrs, _ := strings.Cut(stdout, g.addrHeader)
	for _, l := range strings.Split(script, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, g.addrs(addrs)
}

// waitForSSH tries each address in turn until one answers with an SSH banner
// or the wait runs out, and returns the address and the banner.
func waitForSSH(addrs []string, wait time.Duration) (addr, banner string, err error) {
	deadline := time.Now().Add(wait)
	for {
		var errs []error
		for _, a := range addrs {
			b, bErr := sshBanner(a, shareDialTimeout)
			if bErr == nil {
				return a, b, nil
			}
			errs = append(errs, fmt.Errorf("%s: %w", a, bErr))
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("no SSH server answered within %s: %w", wait, errors.Join(errs...))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// sshBanner connects to addr and returns the identification line an SSH server
// sends first (RFC 4253, section 4.2). An open port that says something else
// is not SSH, and is an error.
func sshBanner(addr string, timeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }() // nothing was written
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	// A server may send other lines before its identification; a few, not
	// a stream.
	r := bufio.NewReaderSize(conn, 512)
	for range 8 {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("connected, and no SSH banner came: %w", err)
		}
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "SSH-") {
			return line, nil
		}
	}
	return "", errors.New("connected, and what answered is not an SSH server")
}
