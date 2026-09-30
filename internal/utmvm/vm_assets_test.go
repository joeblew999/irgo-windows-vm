package utmvm

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The answer file is the whole reason the install is unattended, and every
// failure mode below is silent: VMCreate ignores what it cannot use and falls back
// to an interactive install with no error explaining why. Each check here is a
// mistake that was actually made.
func TestAnswerFileIsValidAndARM64(t *testing.T) {
	x := autounattendXML

	if err := xml.Unmarshal(x, new(struct {
		XMLName xml.Name `xml:"unattend"`
	})); err != nil {
		t.Fatalf("answer file is not well-formed XML: %v", err)
	}

	s := string(x)

	// An x64 answer file is not rejected — every component is silently ignored
	// and VMCreate runs interactively, which looks like the file being missing.
	if n := strings.Count(s, `processorArchitecture="arm64"`); n < 8 {
		t.Errorf("only %d arm64 components; every component must be arm64 or VMCreate ignores them all", n)
	}
	// Comments deliberately mention amd64 as the failure to avoid, so they must
	// be stripped before checking — otherwise the warning trips its own test.
	if strings.Contains(stripXMLComments(s), `processorArchitecture="amd64"`) {
		t.Error("an amd64 component is present; on ARM64 VMCreate ignores every component silently")
	}

	for _, want := range []struct{ frag, why string }{
		{"<Value>Windows 11 Pro</Value>",
			"Home cannot host RDP, and a name absent from the image stops VMCreate on the edition picker"},
		{"BypassTPMCheck", "Windows 11 refuses to install without it unless bypassed"},
		{"<WillWipeDisk>true</WillWipeDisk>", "a re-run must not stop on an existing partition layout"},
		{"<SkipMachineOOBE>true</SkipMachineOOBE>", "OOBE would wait for a human"},
		{"<HideOnlineAccountScreens>true</HideOnlineAccountScreens>",
			"the Microsoft-account wall blocks unattended setup"},
		{"fDenyTSConnections", "RDP is how the VM is reached when the guest agent is absent"},
	} {
		if !strings.Contains(s, want.frag) {
			t.Errorf("answer file missing %q\n  why: %s", want.frag, want.why)
		}
	}
}

// `start` does not expand wildcards. Passing utm-guest-tools-*.exe to it fails
// silently, the installer never runs, and the VM comes up with no guest agent —
// undriveable from the host, with nothing to indicate why.
func TestGuestToolsInstallExpandsWildcard(t *testing.T) {
	s := string(autounattendXML)
	body := stripXMLComments(s)
	i := strings.Index(body, "utm-guest-tools")
	if i < 0 {
		t.Fatal("answer file does not install the guest tools; utmctl exec would never work")
	}
	line := body[max(0, i-300):min(len(body), i+200)]
	if !strings.Contains(line, "for %f in") {
		t.Error("the installer path must be expanded by `for` before `start` sees it; " +
			"start does not expand wildcards and fails silently")
	}
}

// The UEFI shell runs startup.nsh, and the order inside it matters: booting the
// installer first would restart VMCreate forever once Windows is installed.
func TestStartupScriptPrefersInstalledWindows(t *testing.T) {
	// Comments explain both loaders, so compare only executable lines.
	s := stripNSHComments(string(startupNSH))
	installed := strings.Index(s, "bootmgfw.efi")
	installer := strings.Index(s, "cdboot_noprompt.efi")
	if installed < 0 || installer < 0 {
		t.Fatal("startup.nsh must handle both an installed Windows and the installer")
	}
	if installed > installer {
		t.Error("installed Windows must be tried before the installer, " +
			"or every reboot restarts VMCreate in a loop")
	}
	if strings.Contains(s, `\efi\boot\bootaa64.efi`) {
		t.Error("bootaa64.efi waits for a keypress that nobody sends; use cdboot_noprompt.efi")
	}
}

// Device Encryption turned itself on in a 24H2 guest and made the whole disk
// ciphertext, which no golden image can compress. The value has to be set in
// specialize, before OOBE ends, and in a command, not a comment.
//
// The comment beside it in autounattend.xml is one line on purpose. With a
// twelve-line one (measured 30 Sep 2026, docs/RESULTS.md) Setup ignored the
// whole answer file and stopped at "Select language settings"; the same
// component under a one-line comment installed. The trigger was not isolated;
// that comment was the only one in the file with "%" in it. This test passed
// against the broken file: only an install shows it.
//
// Negative control, run by hand: move the RunSynchronousCommand into
// oobeSystem, or delete it and leave the comment, and this fails.
func TestAnswerFilePreventsDeviceEncryptionBeforeOOBE(t *testing.T) {
	s := stripXMLComments(string(autounattendXML))
	start := strings.Index(s, `<settings pass="specialize">`)
	if start < 0 {
		t.Fatal("answer file has no specialize pass")
	}
	end := strings.Index(s[start:], "</settings>")
	if end < 0 {
		t.Fatal("specialize pass is not closed")
	}
	specialize := s[start : start+end]
	want := `reg add HKLM\SYSTEM\CurrentControlSet\Control\BitLocker /v PreventDeviceEncryption /t REG_DWORD /d 1 /f`
	if !strings.Contains(specialize, "<Path>"+want+"</Path>") {
		t.Errorf("specialize does not run %q; Windows 11 24H2 encrypts the disk on its own and a golden image of it does not compress", want)
	}
	if !strings.Contains(specialize, `name="Microsoft-Windows-Deployment"`) {
		t.Error("RunSynchronous in specialize belongs to Microsoft-Windows-Deployment; under another component Setup ignores it")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// stripXMLComments removes <!-- ... --> so a comment warning about a mistake
// cannot be mistaken for the mistake.
func stripXMLComments(s string) string {
	for {
		i := strings.Index(s, "<!--")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "-->")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+3:]
	}
}

// stripNSHComments drops UEFI-shell comment lines, which begin with #.
func stripNSHComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
