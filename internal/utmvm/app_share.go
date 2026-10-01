package utmvm

// Pushing over SMB: the fast path for Push.
//
// utmctl file push moves about 0.4 MB/s. The guest's own network is far faster,
// but the Mac cannot be the server — its firewall runs in stealth mode and the
// guest's connections to it are dropped — and the owner is not to be asked to
// change that. So the connection goes the other way: Windows serves a share,
// the Mac connects out to it, and the only settings that change are in the
// guest (assets/file-share.ps1, run by vm-repair and at first logon).
//
// The client is pure Go (cloudsoda/go-smb2, the maintained fork of
// hirochachacha/go-smb2), so nothing is mounted on the Mac and nothing appears
// in Finder.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
)

// The share, as assets/file-share.ps1 creates it. TestShareScriptAgrees checks
// the script spells them the same way.
//
// The account is dev with the password dev, from autounattend.xml: a throwaway
// VM's obvious credentials, reachable only from this Mac (see docs/USING.md).
const (
	shareName = "irgo-drop"
	shareDir  = `C:\irgo-drop`
	shareUser = "dev"
	sharePass = "dev"
)

// shareDialTimeout bounds the TCP connect. The guest is on a virtual bridge a
// fraction of a millisecond away, and a guest without the firewall rule drops
// the SYN rather than refusing it, so a long timeout is only time wasted before
// the fallback.
const shareDialTimeout = time.Second

// guestIPDirName holds the guest addresses the fast path last reached, under
// the runtime root.
const guestIPDirName = "net"

// pushShared copies localPath to guestPath over the guest's SMB share, and
// returns the address it used.
//
// The file is written into the share under a staging name, then moved into
// place and hashed in the guest, in one batch run as SYSTEM: the share covers
// one directory, guestPath may be in another (C:\Windows\Temp), and a move
// within C: is a rename. Success means the guest's SHA-256 of guestPath equals
// the bytes read here.
func pushShared(vmRef, localPath, guestPath string) (string, error) {
	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }() // read-only

	ip, sess, err := dialShare(vmRef)
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Logoff() }()
	share, err := sess.Mount(shareName)
	if err != nil {
		return ip, fmt.Errorf("opening \\\\%s\\%s: %w", ip, shareName, err)
	}
	defer func() { _ = share.Umount() }()

	// Named after the file, not the time: an interrupted push leaves one stale
	// file per name, which the next push of that name overwrites.
	stage := scratchPrefix + path.Base(strings.ReplaceAll(guestPath, `\`, "/")) + ".part"
	sum, err := copyToShare(share, stage, src)
	if err != nil {
		_ = share.Remove(stage)
		return ip, fmt.Errorf("writing \\\\%s\\%s\\%s: %w", ip, shareName, stage, err)
	}

	res, err := appExecSteps(vmRef, [][]string{
		{"move", "/y", shareDir + `\` + stage, guestPath},
		{"certutil", "-hashfile", guestPath, "SHA256"},
	}, time.Minute, func(string, ...any) {})
	if err != nil {
		_ = share.Remove(stage)
		return ip, fmt.Errorf("moving it into place in the guest: %w", err)
	}
	if res.ExitCode != 0 {
		_ = share.Remove(stage)
		return ip, fmt.Errorf("moving it into place in the guest: exit %d: %s", res.ExitCode, res.Stdout)
	}
	got, ok := certutilSHA256(res.Stdout)
	if !ok {
		return ip, fmt.Errorf("no SHA-256 in certutil's output: %q", res.Stdout)
	}
	if got != sum {
		return ip, fmt.Errorf("%s in the guest has SHA-256 %s, the local file %s", guestPath, got, sum)
	}
	return ip, nil
}

// copyToShare writes r to name on the share and returns the SHA-256 of what it
// wrote. Close is checked: it is where the server reports a write it could not
// complete.
func copyToShare(share *smb2.Share, name string, r io.Reader) (string, error) {
	f, err := share.Create(name)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(f, io.TeeReader(r, h)); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dialShare opens an authenticated, signed SMB session to the guest.
//
// The address comes from a cache first, because asking costs a guest round trip
// and `utmctl ip-address` sometimes hangs; then from utmctl with a short
// deadline; then from ipconfig, run through the agent. A cached address that no
// longer answers is dropped and the others are tried.
func dialShare(vmRef string) (string, *smb2.Session, error) {
	var errs []error
	tried := map[string]bool{}
	try := func(ips []string) (string, *smb2.Session) {
		for _, ip := range ips {
			if tried[ip] {
				continue
			}
			tried[ip] = true
			s, err := smbSession(ip)
			if err == nil {
				saveGuestIP(vmRef, ip)
				return ip, s
			}
			errs = append(errs, fmt.Errorf("%s: %w", ip, err))
		}
		return "", nil
	}
	if ip := cachedGuestIP(vmRef); ip != "" {
		if got, s := try([]string{ip}); s != nil {
			return got, s, nil
		}
		forgetGuestIP(vmRef)
	}
	// ipconfig only when utmctl gave no answer: when it did, the address is
	// known and the share is what is missing, and a guest round trip would only
	// delay the fallback.
	if ips, err := Named(vmRef).ipAddressWithin(3 * time.Second); err == nil {
		if got, s := try(ips); s != nil {
			return got, s, nil
		}
	} else if res, xerr := appExec(vmRef, []string{"ipconfig"}, time.Minute, func(string, ...any) {}); xerr == nil {
		errs = append(errs, err)
		if got, s := try(ipconfigIPv4(res.Stdout)); s != nil {
			return got, s, nil
		}
	} else {
		errs = append(errs, err, fmt.Errorf("ipconfig in the guest: %w", xerr))
	}
	if len(errs) == 0 {
		return "", nil, errors.New("the guest reported no IPv4 address")
	}
	return "", nil, errors.Join(errs...)
}

// smbSession connects to ip:445 and logs in as the share's account. Signing is
// required, so a server that would fall back to guest access is refused rather
// than trusted.
func smbSession(ip string) (*smb2.Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), shareDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "445"))
	if err != nil {
		return nil, err
	}
	// The login gets longer than the connect: NTLM is three messages, and the
	// first can wait on the server's service starting.
	lctx, lcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer lcancel()
	d := &smb2.Dialer{
		Negotiator: smb2.Negotiator{RequireMessageSigning: true},
		Initiator:  &smb2.NTLMInitiator{User: shareUser, Password: sharePass},
	}
	s, err := d.DialConn(lctx, conn, ip)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

// ipconfigIPv4 returns the usable IPv4 addresses in Windows ipconfig output:
// not loopback, and not the 169.254 address an adapter gives itself when DHCP
// never answered.
func ipconfigIPv4(out string) []string {
	var ips []string
	for _, line := range strings.Split(out, "\n") {
		label, val, ok := strings.Cut(line, ":")
		if !ok || !strings.Contains(label, "IPv4 Address") {
			continue
		}
		val = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(val), "(Preferred)"))
		ip := net.ParseIP(val)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		ips = append(ips, val)
	}
	return ips
}

// certutilSHA256 finds the hash in `certutil -hashfile <f> SHA256` output. The
// hash is the line of 64 hex digits; older builds print it in spaced pairs.
func certutilSHA256(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(line), " ", ""))
		if len(h) != 64 {
			continue
		}
		if _, err := hex.DecodeString(h); err == nil {
			return h, true
		}
	}
	return "", false
}

// guestIPCache is where the guest's address is remembered between runs, one
// file per VM reference.
func guestIPCache(vmRef string) string {
	return filepath.Join(appRoot(), guestIPDirName, strings.NewReplacer("/", "_", `\`, "_").Replace(vmRef)+".ip")
}

// cachedGuestIP returns the remembered address, or "" when there is none. A
// wrong address costs one failed connect, so nothing here is fatal.
func cachedGuestIP(vmRef string) string {
	b, err := os.ReadFile(guestIPCache(vmRef))
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(b))
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

// saveGuestIP remembers an address that just worked. Best effort: failing to
// write it only means asking again next time.
func saveGuestIP(vmRef, ip string) {
	p := guestIPCache(vmRef)
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		_ = os.WriteFile(p, []byte(ip+"\n"), 0o644)
	}
}

func forgetGuestIP(vmRef string) { _ = os.Remove(guestIPCache(vmRef)) }
