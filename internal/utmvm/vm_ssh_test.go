package utmvm

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// sshBlob is a key blob that names typ as its type, as every real one does,
// followed by 32 bytes standing in for the key.
func sshBlob(typ string) string {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(typ)))
	b = append(b, typ...)
	b = binary.BigEndian.AppendUint32(b, 32)
	b = append(b, make([]byte, 32)...)
	return base64.StdEncoding.EncodeToString(b)
}

// The second case is a real private key's first and last lines: passing
// id_ed25519 for id_ed25519.pub is the mistake this exists for, and the
// message has to say so, because the same file fails the base64 check anyway
// with words that do not.
//
// Negative controls, run by hand 2 Oct 2026: without the PRIVATE KEY check
// the private-key case fails on its message; without the comparison of the
// blob's own type with the line's, "the type is not the key's" is accepted;
// with `len(lines) < 1` for `!= 1`, "two keys" is accepted.
func TestParseSSHPublicKey(t *testing.T) {
	ed := sshBlob("ssh-ed25519")
	for _, c := range []struct {
		name, in string
		want     SSHKey
		wantErr  string
	}{
		{"a key and a comment", "ssh-ed25519 " + ed + " me@mac\n", SSHKey{"ssh-ed25519", ed, "me@mac"}, ""},
		{"no comment, no newline", "ssh-ed25519 " + ed, SSHKey{"ssh-ed25519", ed, ""}, ""},
		{"a comment with spaces, CRLF", "ssh-ed25519  " + ed + "  my  key\r\n", SSHKey{"ssh-ed25519", ed, "my key"}, ""},
		{"a # line before it", "# mine\nssh-ed25519 " + ed + " x\n", SSHKey{"ssh-ed25519", ed, "x"}, ""},
		{"a private key", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEA\n-----END OPENSSH PRIVATE KEY-----\n", SSHKey{}, "private key"},
		{"empty", "\n", SSHKey{}, "0 keys"},
		{"two keys", "ssh-ed25519 " + ed + " a\nssh-ed25519 " + ed + " b\n", SSHKey{}, "2 keys"},
		{"one field", "ssh-ed25519\n", SSHKey{}, "type base64"},
		{"not base64", "ssh-ed25519 !!!! x\n", SSHKey{}, "not base64"},
		{"the type is not the key's", "ssh-rsa " + ed + " x\n", SSHKey{}, "does not"},
		{"options before the key", `command="x" ssh-ed25519 ` + ed + "\n", SSHKey{}, ""},
		{"a blob too short", "ssh-ed25519 AAAA x\n", SSHKey{}, "too short"},
	} {
		got, err := parseSSHPublicKey([]byte(c.in))
		if c.want.Type == "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: got %+v, %v; want an error containing %q", c.name, got, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
	if got := (SSHKey{"ssh-ed25519", ed, ""}).Line(); got != "ssh-ed25519 "+ed {
		t.Errorf("Line with no comment = %q, want no trailing space", got)
	}
}

// ~/ is the home directory, a missing file and a private key are both
// ErrSSHKey (exit 2), and the error names the file.
//
// Negative control, run by hand 2 Oct 2026: without the CutPrefix the first
// case fails with "no such file".
func TestReadSSHPublicKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	pub := "ssh-ed25519 " + sshBlob("ssh-ed25519") + " t@t\n"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte(pub), 0o644); err != nil {
		t.Fatal(err)
	}
	priv := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.WriteFile(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := ReadSSHPublicKey("~/.ssh/id_ed25519.pub")
	if err != nil || k.Comment != "t@t" {
		t.Fatalf("~/.ssh/id_ed25519.pub: %+v, %v", k, err)
	}
	for _, bad := range []string{priv, filepath.Join(home, "none.pub")} {
		if _, err := ReadSSHPublicKey(bad); !errors.Is(err, ErrSSHKey) {
			t.Errorf("%s: %v, want ErrSSHKey", bad, err)
		}
	}
	if _, err := ReadSSHPublicKey(priv); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Errorf("a private key was refused without saying what it is: %v", err)
	}
}

// answering is a listener on loopback that says hello to every connection and
// closes it.
func answering(t *testing.T, hello string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte(hello))
			_ = c.Close()
		}
	}()
	return l.Addr().String()
}

// An open port is not an SSH server: only the identification line is. The
// closed address is a listener that was closed, so nothing else has the port.
//
// Negative control, run by hand 2 Oct 2026: with the HasPrefix test replaced
// by `line != ""`, the HTTP server is reported as SSH and both checks on it
// fail.
func TestSSHBannerAndWait(t *testing.T) {
	ssh := answering(t, "SSH-2.0-OpenSSH_for_Windows_9.5\r\n")
	late := answering(t, "a notice first\r\nSSH-2.0-Late\r\n")
	web := answering(t, "HTTP/1.1 400 Bad Request\r\n\r\n")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()

	if b, err := sshBanner(ssh, time.Second); err != nil || b != "SSH-2.0-OpenSSH_for_Windows_9.5" {
		t.Errorf("an SSH server: %q, %v", b, err)
	}
	if b, err := sshBanner(late, time.Second); err != nil || b != "SSH-2.0-Late" {
		t.Errorf("an SSH server that says something first: %q, %v", b, err)
	}
	if b, err := sshBanner(web, time.Second); err == nil {
		t.Errorf("a web server was taken for SSH: %q", b)
	}
	if b, err := sshBanner(closed, time.Second); err == nil {
		t.Errorf("a closed port answered: %q", b)
	}

	addr, _, err := waitForSSH([]string{closed, web, ssh}, time.Second)
	if err != nil || addr != ssh {
		t.Errorf("waitForSSH = %q, %v; want the SSH server past the two that are not", addr, err)
	}
	if addr, _, err := waitForSSH([]string{closed, web}, 200*time.Millisecond); err == nil {
		t.Errorf("waitForSSH with no SSH server returned %q", addr)
	} else if !strings.Contains(err.Error(), closed) || !strings.Contains(err.Error(), web) {
		t.Errorf("the failure does not name what it tried: %v", err)
	}
}

// The script's lines are said and ipconfig's are not, and the addresses come
// from ipconfig only: an address in a script line is not the guest's.
//
// Negative control, run by hand 2 Oct 2026: reading addresses from the whole
// output returns 10.9.9.9 too.
func TestSplitSSHOutput(t *testing.T) {
	out := "account: ok (dev, an administrator)\r\n" +
		"   IPv4 Address. . . . . . . . . . . : 10.9.9.9\r\n" +
		"ssh: already on, nothing changed\r\n" + ipconfigOut
	lines, ips := splitSSHOutput(windowsGuest, out)
	if len(lines) != 3 || lines[2] != "ssh: already on, nothing changed" {
		t.Errorf("lines = %q", lines)
	}
	if want := []string{"192.168.64.40"}; !reflect.DeepEqual(ips, want) {
		t.Errorf("ips = %v, want %v", ips, want)
	}
	if lines, ips := splitSSHOutput(windowsGuest, "account: there is no local account x\r\n"); len(lines) != 1 || len(ips) != 0 {
		t.Errorf("a script that failed before ipconfig: %q, %v", lines, ips)
	}
}

// What the documentation promises about the script: where the port is open
// from, where the key goes and who may write it, that keys are compared with
// case, and that sshd's own configuration, password settings included, is not
// touched. Comments are dropped first, since they say what it does not do.
//
// Negative control, run by hand 2 Oct 2026: a line
// `Add-Content $env:ProgramData\ssh\sshd_config 'PasswordAuthentication no'`
// fails it twice, and -eq for -ceq once.
func TestSSHScriptAgrees(t *testing.T) {
	var code []string
	for _, l := range strings.Split(vmSSHScript, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			code = append(code, l)
		}
	}
	s := strings.Join(code, "\n")
	for _, want := range []string{
		"-LocalPort " + sshPort + " -RemoteAddress LocalSubnet -Profile Any",
		`$env:ProgramData\ssh\administrators_authorized_keys`,
		"/inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F'",
		"-ceq",
		"[string]$User = 'dev'",
		"[string]$KeyFile",
		"[switch]$Remove",
		"OpenSSH.Server~~~~0.0.1.0",
		"Remove-Item -LiteralPath $KeyFile",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("vm-ssh.ps1 does not contain %q", want)
		}
	}
	for _, never := range []string{"sshd_config", "PasswordAuthentication", "PubkeyAuthentication", "PRIVATE"} {
		if strings.Contains(s, never) {
			t.Errorf("vm-ssh.ps1 mentions %q outside a comment; it must leave sshd's configuration alone", never)
		}
	}
}
