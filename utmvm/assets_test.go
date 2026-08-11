package utmvm

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The answer file is the whole reason the install is unattended, and every
// failure mode below is silent: Setup ignores what it cannot use and falls back
// to an interactive install with no error explaining why. Each check here is a
// mistake that was actually made.
func TestAnswerFileIsValidAndARM64(t *testing.T) {
	x := AnswerFile()

	if err := xml.Unmarshal(x, new(struct {
		XMLName xml.Name `xml:"unattend"`
	})); err != nil {
		t.Fatalf("answer file is not well-formed XML: %v", err)
	}

	s := string(x)

	// An x64 answer file is not rejected — every component is silently ignored
	// and Setup runs interactively, which looks like the file being missing.
	if n := strings.Count(s, `processorArchitecture="arm64"`); n < 8 {
		t.Errorf("only %d arm64 components; every component must be arm64 or Setup ignores them all", n)
	}
	// Comments deliberately mention amd64 as the failure to avoid, so they must
	// be stripped before checking — otherwise the warning trips its own test.
	if strings.Contains(stripXMLComments(s), `processorArchitecture="amd64"`) {
		t.Error("an amd64 component is present; on ARM64 Setup ignores every component silently")
	}

	for _, want := range []struct{ frag, why string }{
		{"<Value>Windows 11 Pro</Value>",
			"Home cannot host RDP, and a name absent from the image stops Setup on the edition picker"},
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
	s := string(AnswerFile())
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
// installer first would restart Setup forever once Windows is installed.
func TestStartupScriptPrefersInstalledWindows(t *testing.T) {
	// Comments explain both loaders, so compare only executable lines.
	s := stripNSHComments(string(StartupScript()))
	installed := strings.Index(s, "bootmgfw.efi")
	installer := strings.Index(s, "cdboot_noprompt.efi")
	if installed < 0 || installer < 0 {
		t.Fatal("startup.nsh must handle both an installed Windows and the installer")
	}
	if installed > installer {
		t.Error("installed Windows must be tried before the installer, " +
			"or every reboot restarts Setup in a loop")
	}
	if strings.Contains(s, `\efi\boot\bootaa64.efi`) {
		t.Error("bootaa64.efi waits for a keypress that nobody sends; use cdboot_noprompt.efi")
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
