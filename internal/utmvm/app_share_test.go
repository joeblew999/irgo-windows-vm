package utmvm

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ipconfigOut is irgo-win11's ipconfig, 30 Sep 2026, with an APIPA adapter
// added: the address a NIC gives itself when DHCP never answered, which the
// Mac cannot reach.
const ipconfigOut = "\r\nWindows IP Configuration\r\n\r\n\r\n" +
	"Ethernet adapter Ethernet:\r\n\r\n" +
	"   Connection-specific DNS Suffix  . : \r\n" +
	"   IPv6 Address. . . . . . . . . . . : fdec:f55c:82d0:c2fc:41e:ed21:f085:1b8d\r\n" +
	"   Link-local IPv6 Address . . . . . : fe80::33c5:8a48:77c2:4d4a%6\r\n" +
	"   IPv4 Address. . . . . . . . . . . : 192.168.64.40\r\n" +
	"   Subnet Mask . . . . . . . . . . . : 255.255.255.0\r\n" +
	"   Default Gateway . . . . . . . . . : fe80::5ce9:1eff:fe7a:a366%6\r\n" +
	"                                       192.168.64.1\r\n\r\n" +
	"Ethernet adapter Ethernet 2:\r\n\r\n" +
	"   Autoconfiguration IPv4 Address. . : 169.254.12.7(Preferred)\r\n"

// Negative control, run by hand: dropping the IsLinkLocalUnicast test returns
// 169.254.12.7 too, and the test fails.
func TestIpconfigIPv4(t *testing.T) {
	got := ipconfigIPv4(ipconfigOut)
	if want := []string{"192.168.64.40"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ipconfigIPv4 = %v, want %v", got, want)
	}
	if got := ipconfigIPv4("Media disconnected\r\n"); len(got) != 0 {
		t.Fatalf("ipconfigIPv4 with no address = %v, want none", got)
	}
}

// The first form is what irgo-win11's certutil printed, 30 Sep 2026. The hash
// is what Push trusts, so a line that merely looks hex-ish must not match.
//
// Negative control, run by hand: removing the length check makes "deadbeef"
// match in the last case, and the test fails.
func TestCertutilSHA256(t *testing.T) {
	const h = "aa1fff87dcf72e58a38d0581613032286aacbe0d2e3efa4d6fc1e5e09fa11584"
	for _, c := range []struct {
		name, out, want string
		ok              bool
	}{
		{"windows 11", "SHA256 hash of C:\\Windows\\System32\\notepad.exe:\r\n" + h + "\r\nCertUtil: -hashfile command completed successfully.\r\n", h, true},
		{"spaced pairs", "SHA256 hash of file x:\r\naa 1f ff 87 dc f7 2e 58 a3 8d 05 81 61 30 32 28 6a ac be 0d 2e 3e fa 4d 6f c1 e5 e0 9f a1 15 84\r\n", h, true},
		{"upper case", strings.ToUpper(h) + "\r\n", h, true},
		{"no hash", "CertUtil: -hashfile command FAILED: 0x80070002 (WIN32: 2 ERROR_FILE_NOT_FOUND)\r\ndeadbeef\r\n", "", false},
	} {
		got, ok := certutilSHA256(c.out)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: certutilSHA256 = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// A later command must append to the output, not truncate it, and must not
// run when an earlier one failed — that is what lets pushShared trust the hash
// line: certutil only runs on a file that was moved into place.
//
// Negative control, run by hand: writing " > " for every command fails the
// append check; dropping the goto line fails the stop check.
func TestBatchSteps(t *testing.T) {
	got := batchSteps([][]string{{"move", "/y", `C:\a b`, `C:\c`}, {"certutil", "-hashfile", `C:\c`, "SHA256"}}, `C:\o.txt`, `C:\r.txt`)
	want := "@echo off\r\n" +
		`move /y "C:\a b" C:\c > "C:\o.txt" 2>&1` + "\r\n" +
		"if %ERRORLEVEL% neq 0 goto done\r\n" +
		`certutil -hashfile C:\c SHA256 >> "C:\o.txt" 2>&1` + "\r\n" +
		":done\r\n" +
		`echo %ERRORLEVEL% > "C:\r.txt"` + "\r\n"
	if got != want {
		t.Fatalf("batchSteps =\n%s\nwant\n%s", got, want)
	}
	// One command is the batch appExec always wrote: no label, no goto.
	one := batchFile([]string{"prog.exe"}, `C:\o.txt`, `C:\r.txt`)
	if one != "@echo off\r\nprog.exe > \"C:\\o.txt\" 2>&1\r\necho %ERRORLEVEL% > \"C:\\r.txt\"\r\n" {
		t.Fatalf("batchFile changed for a single command:\n%s", one)
	}
}

// The share is defined twice, in Go (what Push connects to) and in the script
// (what the guest opens), and the account a third time, in the answer file.
// If they drift, every push falls back to utmctl and says only that the share
// did not work.
//
// Negative control, run by hand: renaming $name in file-share.ps1 fails it.
func TestShareScriptAgrees(t *testing.T) {
	s := string(fileShareScript)
	for _, want := range []string{
		"$name = '" + shareName + "'",
		"$path = '" + shareDir + "'",
		"[string]$User = '" + shareUser + "'",
		"-LocalPort 445 -RemoteAddress LocalSubnet",
		"[switch]$Remove",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("file-share.ps1 does not contain %q", want)
		}
	}
	a := string(autounattendXML)
	for _, want := range []string{
		"<Name>" + shareUser + "</Name>",
		"<Password><Value>" + sharePass + "</Value>",
		`%i:\file-share.ps1`,
	} {
		if !strings.Contains(a, want) {
			t.Errorf("autounattend.xml does not contain %q", want)
		}
	}
}

// autounattend.xml runs file-share.ps1 from the unattend CD, so it must be on
// it, at the root, under its long name.
//
// Negative control, run by hand: deleting the WriteFile in BuildPayload fails it.
func TestPayloadCarriesShareScript(t *testing.T) {
	img := filepath.Join(t.TempDir(), "unattend.iso")
	if err := BuildPayload(img, PayloadOptions{SizeMiB: 16}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, isoEncode16be("file-share.ps1")) {
		t.Error("file-share.ps1 is not on the payload under its Joliet name")
	}
	if !bytes.Contains(data, fileShareScript) {
		t.Error("the payload does not hold file-share.ps1's bytes")
	}
}
