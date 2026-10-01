package vmconformance

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The checks made as SYSTEM, through the guest agent. Each reads; none
// writes. What sets each property is in its failure message, and in
// internal/utmvm/assets: autounattend.xml for a new VM, vm-repair.ps1 for an
// existing one, vm-golden-seal.ps1 for the golden image, file-share.ps1 for
// the share.

const (
	hklm = syscall.HKEY_LOCAL_MACHINE

	keyCurrentVersion = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	keyWinlogon       = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`
	keyWU             = `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`
	keyAU             = keyWU + `\AU`
	keyKeyboard       = `SYSTEM\CurrentControlSet\Control\Keyboard Layout`
	keyMarker         = `SOFTWARE\irgo-winvm`
	keyOneDrive       = `SOFTWARE\Policies\Microsoft\Windows\OneDrive`
	keyBitLocker      = `SYSTEM\CurrentControlSet\Control\BitLocker`
	keyPower          = `SYSTEM\CurrentControlSet\Control\Power`
	keyWebView2State  = `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\ClientState\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	keyWebView2Client = `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

	unattendMarker = `C:\unattend-complete.txt`

	repair = "irgo-winvm vm-repair fixes it"
)

// TestWindowsBuild records the Windows the VM runs, and checks it is Windows
// 11 on ARM64, which is the only thing this project builds for.
func TestWindowsBuild(t *testing.T) {
	asSystem(t)
	build, _ := regString(t, hklm, keyCurrentVersion, "CurrentBuild")
	ubr, _ := regDWORD(t, hklm, keyCurrentVersion, "UBR")
	display, _ := regString(t, hklm, keyCurrentVersion, "DisplayVersion")
	edition, _ := regString(t, hklm, keyCurrentVersion, "EditionID")
	arch := os.Getenv("PROCESSOR_ARCHITECTURE")
	v := fmt.Sprintf("%s.%d (%s, %s, %s)", build, ubr, display, edition, arch)
	fact(t, "windows", v)
	evidence(t, "%s", v)
	n, err := strconv.Atoi(build)
	if err != nil || n < 22000 {
		t.Fatalf("CurrentBuild is %q: not Windows 11 (22000 or later)", build)
	}
	if arch != "ARM64" || runtime.GOARCH != "arm64" {
		t.Fatalf("the processor is %s and this binary %s: the VM is meant to be ARM64", arch, runtime.GOARCH)
	}
}

// TestDevAccount: the account the VM logs itself in as exists, is enabled,
// is an administrator, and its password never expires — local passwords
// expire after 42 days, and AutoLogon then stops at "Your password has
// expired" with no desktop, so every -gui run has nowhere to go.
func TestDevAccount(t *testing.T) {
	asSystem(t)
	t.Run("exists_enabled_admin", func(t *testing.T) {
		out := powershell(t, fmt.Sprintf(`$u = Get-LocalUser -Name '%s'; `+
			`$admin = [bool](Get-LocalGroupMember -SID S-1-5-32-544 | Where-Object { $_.Name -like '*\%s' }); `+
			`"$($u.Enabled)|$admin|$($u.PasswordExpires)"`, *devUser, *devUser))
		f := strings.SplitN(out, "|", 3)
		if len(f) != 3 {
			t.Fatalf("Get-LocalUser said %q", out)
		}
		evidence(t, "enabled=%s administrator=%s", f[0], f[1])
		if f[0] != "True" {
			t.Fatalf("%s is disabled", *devUser)
		}
		if f[1] != "True" {
			t.Fatalf("%s is not in Administrators; the answer file makes it one", *devUser)
		}
	})
	t.Run("password_never_expires", func(t *testing.T) {
		out := powershell(t, fmt.Sprintf(`"$((Get-LocalUser -Name '%s').PasswordExpires)"`, *devUser))
		evidence(t, "PasswordExpires=%q", out)
		if out != "" {
			t.Fatalf("%s's password expires %s; AutoLogon stops when it does (%s)", *devUser, out, repair)
		}
	})
	t.Run("max_password_age_unlimited", func(t *testing.T) {
		age, err := maxPasswordAge(command(t, "net.exe", "accounts"))
		if err != nil {
			t.Fatal(err)
		}
		evidence(t, "maximum password age=%s", age)
		if age != "Unlimited" {
			t.Fatalf("local passwords expire after %s days (net accounts); want Unlimited (%s)", age, repair)
		}
	})
}

// TestAutoLogon: Windows logs dev in by itself, with logons to spare, and dev
// is logged in now — without a desktop session no -gui run can start.
func TestAutoLogon(t *testing.T) {
	asSystem(t)
	t.Run("configured", func(t *testing.T) {
		auto, _ := regString(t, hklm, keyWinlogon, "AutoAdminLogon")
		name, _ := regString(t, hklm, keyWinlogon, "DefaultUserName")
		count, hasCount := regDWORD(t, hklm, keyWinlogon, "AutoLogonCount")
		left := "no limit"
		if hasCount {
			left = strconv.Itoa(int(count)) + " logons left"
		}
		evidence(t, "AutoAdminLogon=%q DefaultUserName=%q, %s", auto, name, left)
		if auto != "1" || !strings.EqualFold(name, *devUser) {
			t.Fatalf("AutoLogon is not set for %s (AutoAdminLogon %q, DefaultUserName %q)", *devUser, auto, name)
		}
		// The answer file allows 999; each logon spends one, and at 0
		// Windows turns AutoLogon off.
		if hasCount && count < 10 {
			t.Fatalf("AutoLogon has %d logons left; at 0 Windows turns it off and the desktop session stops coming back", count)
		}
	})
	t.Run("desktop_session", func(t *testing.T) {
		out := powershell(t, `Get-CimInstance Win32_Process -Filter "Name='explorer.exe'" | ForEach-Object { `+
			`$o = Invoke-CimMethod -InputObject $_ -MethodName GetOwner; "$($o.User)|$($_.SessionId)" }`)
		evidence(t, "explorer.exe (owner|session): %s", strings.Join(strings.Fields(out), ", "))
		for _, l := range strings.Fields(out) {
			if u, s, _ := strings.Cut(l, "|"); strings.EqualFold(u, *devUser) && s != "0" {
				return
			}
		}
		t.Fatalf("no explorer.exe runs as %s in a desktop session: nobody is logged in, and -gui has nowhere to run "+
			"(irgo-winvm vm-screen shows why; irgo-winvm vm-repair -reboot brings AutoLogon back)", *devUser)
	})
}

// TestWindowsUpdatePolicy: Windows Update never restarts the VM on its own,
// downloads nothing without being asked, and puts no prompt on the desktop —
// one restarted it nine minutes into a test run, and its prompt sat on every
// screenshot.
func TestWindowsUpdatePolicy(t *testing.T) {
	asSystem(t)
	set := "the answer file sets it; " + repair
	t.Run("no_auto_restart", func(t *testing.T) { wantDWORD(t, hklm, keyAU, "NoAutoRebootWithLoggedOnUsers", 1, set) })
	t.Run("notify_only", func(t *testing.T) { wantDWORD(t, hklm, keyAU, "AUOptions", 2, set) })
	t.Run("notifications_off", func(t *testing.T) {
		wantDWORD(t, hklm, keyWU, "SetUpdateNotificationLevel", 1, set)
		wantDWORD(t, hklm, keyWU, "UpdateNotificationLevel", 2, set)
	})
	t.Run("restart_notifications_off", func(t *testing.T) {
		wantDWORD(t, hklm, keyWU, "SetAutoRestartNotificationDisable", 1, set)
	})
}

// TestWindowsKeysDisabled: both Windows keys are remapped to nothing — UTM
// forwards the Mac's Command key as the Windows key, so every Cmd-Tab opened
// Start over the screenshots — and this boot has the remap, which Windows
// reads only at boot.
func TestWindowsKeysDisabled(t *testing.T) {
	asSystem(t)
	typ, m, err := regRead(hklm, keyKeyboard, "Scancode Map")
	if errors.Is(err, errNoValue) {
		evidence(t, "Scancode Map not set")
		t.Fatalf(`HKLM\%s\Scancode Map is not set: the Windows keys work (%s, then a reboot)`, keyKeyboard, repair)
	}
	if err != nil {
		t.Fatal(err)
	}
	evidence(t, "Scancode Map=%X", m)
	off, err := windowsKeysOff(m)
	if typ != syscall.REG_BINARY || err != nil || !off {
		t.Fatalf("the Scancode Map (type %d) does not map both Windows keys to nothing: %v (%s)", typ, err, repair)
	}

	t.Run("in_effect_since_boot", func(t *testing.T) {
		boot := bootTime()
		// When it was written: vm-repair records that; otherwise the answer
		// file wrote it moments before C:\unattend-complete.txt.
		var setAt time.Time
		how := ""
		if _, b, err := regRead(hklm, keyMarker, "ScancodeMapSetAt"); err == nil && len(b) == 8 {
			setAt = time.Unix(0, (int64(binary.LittleEndian.Uint64(b))-116444736000000000)*100)
			how = "vm-repair wrote it"
		} else if fi, err := os.Stat(unattendMarker); err == nil {
			setAt = fi.ModTime()
			how = "the answer file wrote it, just before " + unattendMarker
		} else {
			evidence(t, "booted %s; no record of when the map was written", boot.UTC().Format(time.RFC3339))
			t.Skip("cannot tell: nothing records when the map was written (irgo-winvm vm-repair -reboot makes sure)")
		}
		evidence(t, "%s at %s; booted %s", how, setAt.UTC().Format(time.RFC3339), boot.UTC().Format(time.RFC3339))
		if !setAt.Before(boot) {
			t.Fatalf("the map was written at %s and Windows last booted at %s: it takes effect at the next boot "+
				"(irgo-winvm vm-repair -reboot)", setAt.UTC().Format(time.RFC3339), boot.UTC().Format(time.RFC3339))
		}
	})
}

// TestOneDriveOff: OneDrive is off by policy — it put "Turn On Windows
// Backup" on the desktop a minute after an update reboot.
func TestOneDriveOff(t *testing.T) {
	asSystem(t)
	wantDWORD(t, hklm, keyOneDrive, "DisableFileSyncNGSC", 1, "the answer file sets it; "+repair)
}

// TestDeviceEncryptionOff: Windows 11 24H2 encrypts the disk on its own,
// ciphertext does not compress, and a TPM protector could lock a clone out.
// So the answer file prevents it and sealing decrypts.
func TestDeviceEncryptionOff(t *testing.T) {
	asSystem(t)
	t.Run("prevented", func(t *testing.T) {
		wantDWORD(t, hklm, keyBitLocker, "PreventDeviceEncryption", 1,
			"the answer file sets it in specialize; irgo-winvm vm-golden-create sets it before sealing")
	})
	t.Run("volume_decrypted", func(t *testing.T) {
		out := powershell(t, `$v = Get-CimInstance -Namespace 'root/CIMV2/Security/MicrosoftVolumeEncryption' `+
			`-ClassName Win32_EncryptableVolume -Filter "DriveLetter='C:'" -ErrorAction SilentlyContinue; `+
			`if (-not $v) { 'none' } else { $s = Invoke-CimMethod -InputObject $v -MethodName GetConversionStatus; `+
			`"$($s.ConversionStatus)|$($s.EncryptionPercentage)|$($v.ProtectionStatus)" }`)
		if out == "none" {
			evidence(t, "C: is not an encryptable volume (no BitLocker)")
			return
		}
		f := strings.Split(out, "|")
		status, err := strconv.Atoi(f[0])
		if len(f) != 3 || err != nil {
			t.Fatalf("BitLocker said %q", out)
		}
		evidence(t, "C: %s, %s%% encrypted, protection status %s", conversionStatus(status), f[1], f[2])
		if status != 0 {
			t.Fatalf("C: is %s (%s%% encrypted): a copy of it does not compress (irgo-winvm vm-golden-create decrypts its source)",
				conversionStatus(status), f[1])
		}
	})
}

// TestHibernationOff: hiberfil.sys is gigabytes of nothing a VM needs and a
// golden image would carry; sealing turns hibernation off.
func TestHibernationOff(t *testing.T) {
	asSystem(t)
	on, hasOn := regDWORD(t, hklm, keyPower, "HibernateEnabled")
	fi, err := os.Stat(`C:\hiberfil.sys`)
	file := "absent"
	if err == nil {
		file = fmt.Sprintf("%d bytes", fi.Size())
	}
	enabled := "not set"
	if hasOn {
		enabled = strconv.Itoa(int(on))
	}
	evidence(t, `HibernateEnabled=%s, C:\hiberfil.sys %s`, enabled, file)
	if err == nil || (hasOn && on != 0) {
		t.Fatalf(`hibernation is on (HibernateEnabled %s, C:\hiberfil.sys %s): powercfg /h off turns it off, `+
			`as irgo-winvm vm-golden-create does before sealing`, enabled, file)
	}
}

// shareScript reports the irgo-drop share and its firewall in one call, as
// JSON (shareFacts).
const shareScript = `$s = Get-SmbShare -Name 'irgo-drop' -ErrorAction SilentlyContinue
$a = @(); if ($s) { $a = @(Get-SmbShareAccess -Name 'irgo-drop' | ForEach-Object { "$($_.AccountName):$($_.AccessRight):$($_.AccessControlType)" }) }
$r = @(Get-NetFirewallRule -DisplayName 'irgo-winvm: SMB from the host' -ErrorAction SilentlyContinue)
$f = $null; $p = $null; if ($r.Count -gt 0) { $f = $r[0] | Get-NetFirewallAddressFilter; $p = $r[0] | Get-NetFirewallPortFilter }
$x = @(Get-NetFirewallRule -DisplayGroup 'File and Printer Sharing (Restrictive)' -ErrorAction SilentlyContinue | Where-Object { $_.Enabled -eq 'True' } | ForEach-Object { $_.DisplayName })
[pscustomobject]@{
  Share = [bool]$s; Path = "$($s.Path)"; Access = $a; Rules = $r.Count
  RuleEnabled = "$($r[0].Enabled)"; Direction = "$($r[0].Direction)"; Action = "$($r[0].Action)"
  Remote = @($f.RemoteAddress | ForEach-Object { "$_" }); Protocol = "$($p.Protocol)"; Port = @($p.LocalPort | ForEach-Object { "$_" })
  Restrictive = $x; Server = "$((Get-Service LanmanServer).Status)"
} | ConvertTo-Json -Compress`

// TestFileShare: the SMB share pushes go through at network speed is there,
// for dev only, reachable on 445 from the local subnet only, and Windows' own
// sharing rules, which 24H2 opens to any address when a share is created,
// are off.
func TestFileShare(t *testing.T) {
	asSystem(t)
	f, err := parseShareFacts(powershell(t, shareScript))
	if err != nil {
		t.Fatal(err)
	}
	fix := "file-share.ps1 opens it; irgo-winvm vm-repair runs that"
	t.Run("share", func(t *testing.T) {
		evidence(t, "irgo-drop=%t path=%q access=%s server=%s", f.Share, f.Path, strings.Join(f.Access, ","), f.Server)
		if !f.Share {
			t.Fatalf(`there is no share irgo-drop: every push falls back to utmctl at 0.4 MB/s (%s)`, fix)
		}
		if !strings.EqualFold(f.Path, `C:\irgo-drop`) {
			t.Fatalf(`irgo-drop shares %q, not C:\irgo-drop (%s)`, f.Path, fix)
		}
		var devFull bool
		for _, a := range f.Access {
			if strings.HasSuffix(strings.ToLower(a), `\`+strings.ToLower(*devUser)+":full:allow") {
				devFull = true
			} else if strings.HasSuffix(a, ":Allow") {
				t.Errorf("irgo-drop also allows %s; it is meant for %s only", a, *devUser)
			}
		}
		if !devFull {
			t.Errorf("irgo-drop does not give %s Full access (%s)", *devUser, fix)
		}
		if f.Server != "Running" {
			t.Errorf("the Server service (LanmanServer) is %s: nothing answers on 445", f.Server)
		}
	})
	t.Run("firewall_local_subnet_only", func(t *testing.T) {
		evidence(t, "rules=%d enabled=%s %s %s %s port=%s remote=%s", f.Rules, f.RuleEnabled, f.Direction, f.Action,
			f.Protocol, strings.Join(f.Port, ","), strings.Join(f.Remote, ","))
		switch {
		case f.Rules == 0:
			t.Fatalf("there is no firewall rule 'irgo-winvm: SMB from the host' (%s)", fix)
		case f.Rules > 1:
			t.Errorf("there are %d rules named 'irgo-winvm: SMB from the host'; one is expected", f.Rules)
		}
		if f.RuleEnabled != "True" || f.Direction != "Inbound" || f.Action != "Allow" || f.Protocol != "TCP" ||
			strings.Join(f.Port, ",") != "445" {
			t.Errorf("the rule is not 'allow inbound TCP 445': enabled %s, %s %s %s %s", f.RuleEnabled, f.Direction, f.Action, f.Protocol, f.Port)
		}
		if strings.Join(f.Remote, ",") != "LocalSubnet" {
			t.Errorf("the rule admits %s; it must admit the local subnet only, where the Mac is", strings.Join(f.Remote, ","))
		}
	})
	t.Run("restrictive_rules_off", func(t *testing.T) {
		evidence(t, "enabled File and Printer Sharing (Restrictive) rules: %d", len(f.Restrictive))
		if len(f.Restrictive) > 0 {
			t.Fatalf("Windows' own sharing rules are on, open to any address: %s (%s)", strings.Join(f.Restrictive, "; "), fix)
		}
	})
}

// TestWebView2: the WebView2 runtime is registered and its folder is there —
// an interrupted update once left the registration naming a deleted folder,
// and every glaze window then reported the runtime missing (glaze#34).
func TestWebView2(t *testing.T) {
	asSystem(t)
	pv, _ := regString(t, hklm, keyWebView2Client, "pv")
	reg, ok := regString(t, hklm, keyWebView2State, "EBWebView")
	fact(t, "webview2", map[bool]string{true: pv, false: "none"}[pv != ""])
	evidence(t, "pv=%q EBWebView=%q", pv, reg)
	if !ok || reg == "" {
		t.Fatalf("the WebView2 runtime is not registered (no EBWebView in HKLM\\%s): glaze cannot open a window", keyWebView2State)
	}
	if _, err := os.Stat(filepath.Join(reg, "EBWebView")); err != nil {
		t.Fatalf("the registration names %s, and it is not there (%v): glaze reports WebView2 missing (%s)", reg, err, repair)
	}
}

// TestNeverSleeps: a VM that sleeps drops its connections and looks hung, and
// a display that times out photographs as black.
func TestNeverSleeps(t *testing.T) {
	asSystem(t)
	for _, s := range []struct{ name, sub, setting, what string }{
		{"sleep", "SUB_SLEEP", "STANDBYIDLE", "sleep after"},
		{"display", "SUB_VIDEO", "VIDEOIDLE", "turn the display off after"},
	} {
		t.Run(s.name, func(t *testing.T) {
			ac, dc, err := powerIndex(command(t, "powercfg.exe", "/query", "SCHEME_CURRENT", s.sub, s.setting))
			if err != nil {
				t.Fatal(err)
			}
			evidence(t, "%s: AC %ds, DC %ds", s.what, ac, dc)
			if ac != 0 {
				t.Fatalf("%s is %d s on AC power; want never (0) — the answer file sets it with powercfg /change", s.what, ac)
			}
		})
	}
}

// TestUnattendComplete: the answer file's last step wrote its marker, so
// every step before it ran.
func TestUnattendComplete(t *testing.T) {
	asSystem(t)
	fi, err := os.Stat(unattendMarker)
	if err != nil {
		t.Fatalf("%s is missing: the answer file's first-logon commands did not all run (%v)", unattendMarker, err)
	}
	evidence(t, "%s written %s", unattendMarker, fi.ModTime().UTC().Format(time.RFC3339))
}

// minFreeBytes is the least free space on C: the suite accepts. A choice,
// not a measurement: room for a Windows update and the binaries pushed in.
const minFreeBytes = 10 << 30

// TestFreeDiskSpace: C: has room for an update and what is pushed in.
func TestFreeDiskSpace(t *testing.T) {
	asSystem(t)
	free, err := freeBytes(`C:\`)
	if err != nil {
		t.Fatal(err)
	}
	gib := fmt.Sprintf("%.1f GiB", float64(free)/(1<<30))
	fact(t, "c-free", gib)
	evidence(t, "C: %s free", gib)
	if free < minFreeBytes {
		t.Fatalf("C: has %s free; want at least %d GiB", gib, minFreeBytes>>30)
	}
}
