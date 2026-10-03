package utmvm

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A script for a Linux guest, run here by the same /bin/sh: each command's
// output lands in the output file in order, a later command does not run once
// one has failed, and the code recorded is the failing one's. That is what
// lets a caller trust the last line: it was only reached if everything before
// it worked.
//
// Negative controls, run by hand 2 Oct 2026: without the `[ "$rc" -ne 0 ] ||`
// guard the third command runs and "after" is in the output; with " > " for
// every command the first command's output is gone; with `|| rc=1` the code
// recorded is 1, not 7.
func TestShSteps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs the script with /bin/sh")
	}
	dir := t.TempDir()
	out, rc, script := filepath.Join(dir, "o u t.txt"), filepath.Join(dir, "rc.txt"), filepath.Join(dir, "s.sh")
	run := func(cmds [][]string) (string, string) {
		t.Helper()
		if err := os.WriteFile(script, []byte(linuxGuest.script(cmds, out, rc)), 0o644); err != nil {
			t.Fatal(err)
		}
		argv := linuxGuest.runScript(script)
		if b, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil || len(b) != 0 {
			t.Fatalf("the script itself failed or printed: %v: %s", err, b)
		}
		o, _ := os.ReadFile(out)
		r, _ := os.ReadFile(rc)
		return string(o), strings.TrimSpace(string(r))
	}

	o, r := run([][]string{{"echo", "one"}, {"/bin/sh", "-c", "echo two >&2"}, {"echo", "it's $HOME; `x` \"y\""}})
	if want := "one\ntwo\nit's $HOME; `x` \"y\"\n"; o != want || r != "0" {
		t.Errorf("three commands: output %q, code %q; want %q and 0", o, r, want)
	}
	o, r = run([][]string{{"echo", "before"}, {"/bin/sh", "-c", "exit 7"}, {"echo", "after"}})
	if o != "before\n" || r != "7" {
		t.Errorf("a failure in the middle: output %q, code %q; want only \"before\" and 7", o, r)
	}
	if o, r = run([][]string{{"/bin/sh", "-c", "exit 3"}}); o != "" || r != "3" {
		t.Errorf("one failing command: output %q, code %q", o, r)
	}
}

// The addresses come from the lines after the header only, loopback and
// link-local left out, in the form `ip -4 -o addr` printed in the guest on
// 2 Oct 2026.
//
// Negative control, run by hand 2 Oct 2026: reading addresses from the whole
// output returns 10.9.9.9 too.
func TestLinuxAddresses(t *testing.T) {
	out := "account: ok (dev, home /home/dev)\n" +
		"3: fake    inet 10.9.9.9/8 scope global fake\n" +
		"ssh: already on, nothing changed\n" +
		linuxAddrHeader + "\n" +
		"1: lo    inet 127.0.0.1/8 scope host lo\\       valid_lft forever preferred_lft forever\n" +
		"2: enp0s1    inet 192.168.64.58/24 metric 100 brd 192.168.64.255 scope global dynamic enp0s1\\       valid_lft 3555sec preferred_lft 3555sec\n" +
		"4: x    inet 169.254.3.4/16 scope link x\n" +
		"2: enp0s1    inet6 fe80::1/64 scope link\n"
	lines, ips := splitSSHOutput(linuxGuest, out)
	if len(lines) != 3 || lines[2] != "ssh: already on, nothing changed" {
		t.Errorf("lines = %q", lines)
	}
	if want := []string{"192.168.64.58"}; !reflect.DeepEqual(ips, want) {
		t.Errorf("ips = %v, want %v", ips, want)
	}
	// The command that prints them says the header first.
	if c := linuxGuest.addrCmd; !strings.Contains(c[len(c)-1], "echo '"+linuxGuest.addrHeader+"'") {
		t.Errorf("the address command %q does not print the header %q", c, linuxGuest.addrHeader)
	}
}

// What the documentation promises about the Linux scripts and the seed, and
// that each is a script /bin/sh accepts: the account, that no password gets
// in, where the key goes and who may read it, that the undo removes keys and
// closes the port, and that a new VM has SSH off.
//
// Negative controls, run by hand 2 Oct 2026: `chmod 644 "$keys"` fails it; an
// unclosed `if` in vm-ssh.sh fails the syntax check; lock_passwd: false in
// the seed fails it.
func TestLinuxAssetsAgree(t *testing.T) {
	code := func(s string) string {
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "#") {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	for _, c := range []struct {
		name, text string
		want       []string
	}{
		{"vm-ssh.sh", code(vmSSHScriptLinux), []string{
			"user=" + linuxUser,
			"-User) user=$2", "-KeyFile) keyfile=$2", "-Remove) remove=1",
			"PasswordAuthentication no",
			"keys=$dir/authorized_keys", `chmod 600 "$keys"`, `chmod 700 "$dir"`,
			`rm -f "$keyfile"`,
			"systemctl disable --now ssh.socket ssh.service",
			"'sport = :" + sshPort + "'",
		}},
		{"vm-linux-check.sh", code(linuxCheckScript), []string{
			"user=" + linuxUser, "-New) new=1", "cloud-init status --wait", "sudo -n true", "'sport = :" + sshPort + "'",
		}},
		{"linux-user-data.yaml", code(string(linuxUserData)), []string{
			"- name: " + linuxUser, "lock_passwd: true", `sudo: "ALL=(ALL) NOPASSWD:ALL"`,
			"- qemu-guest-agent", "[systemctl, disable, --now, ssh.socket, ssh.service]",
		}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.text, w) {
				t.Errorf("%s does not contain %q", c.name, w)
			}
		}
	}
	// cloud-init reads user-data as a cloud-config only if this is its first line.
	if !bytes.HasPrefix(linuxUserData, []byte("#cloud-config\n")) {
		t.Error("linux-user-data.yaml does not start with #cloud-config")
	}
	if runtime.GOOS == "windows" {
		return
	}
	for name, s := range map[string]string{"vm-ssh.sh": vmSSHScriptLinux, "vm-linux-check.sh": linuxCheckScript} {
		cmd := exec.Command("/bin/sh", "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s is not a script /bin/sh accepts: %v: %s", name, err, out)
		}
	}
}

// The seed CD is an ISO9660 volume named cidata, which is how cloud-init
// finds it, trimmed to its declared size, holding user-data as embedded and a
// meta-data naming this VM.
//
// Negative control, run by hand 2 Oct 2026: passing unattendLabel to
// isoBuildImage in buildSeed fails the label check.
func TestBuildSeed(t *testing.T) {
	img := filepath.Join(t.TempDir(), seedISO)
	if err := buildSeed(img, "ABC-123", "my-vm"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	// The primary volume descriptor is sector 16: "CD001" at byte 1, the
	// volume identifier at byte 40, 32 bytes that go-diskfs pads with NULs
	// where the standard says spaces, and the volume's size in blocks at
	// byte 80.
	pvd := data[16*2048:]
	if string(pvd[1:6]) != "CD001" {
		t.Fatalf("no ISO9660 volume descriptor: %q", pvd[1:6])
	}
	if label := strings.TrimRight(string(pvd[40:72]), "\x00 "); label != seedLabel {
		t.Errorf("the volume is named %q, and cloud-init looks for %q", label, seedLabel)
	}
	if want := int(binary.LittleEndian.Uint32(pvd[80:84])) * 2048; len(data) != want {
		t.Errorf("the image is %d bytes and declares %d", len(data), want)
	}
	for _, want := range [][]byte{linuxUserData, []byte("instance-id: ABC-123\nlocal-hostname: my-vm\n"),
		isoEncode16be("user-data"), isoEncode16be("meta-data")} {
		if !bytes.Contains(data, want) {
			t.Errorf("the seed does not hold %q", want[:min(len(want), 40)])
		}
	}
}

// Negative control, run by hand 2 Oct 2026: without the Trim, "-a-" stays as
// it is, which is not a hostname.
func TestSeedHostname(t *testing.T) {
	for in, want := range map[string]string{
		"rig-linux": "rig-linux", "Linux Dev_1": "linux-dev-1", "-a-": "a", "": "linux", "___": "linux",
		strings.Repeat("a", 62) + "-bcd": strings.Repeat("a", 62),
	} {
		if got := seedHostname(in); got != want {
			t.Errorf("seedHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

// convertImage's own promises, on a raw image, which the reader also takes:
// the output is the size asked for with the source's bytes at the start, and
// a source that is not a disk with a partition table, or is larger than the
// disk to be made, is an error and not a VM that will not boot. That it reads
// Ubuntu's compressed qcow2 is measured, not tested here (docs/RESULTS.md).
//
// Negative controls, run by hand 2 Oct 2026: without the Truncate the size
// check fails; without the read-back, "no partition table" returns nil.
func TestConvertImage(t *testing.T) {
	dir := t.TempDir()
	src := make([]byte, 1<<20)
	copy(src[512:], gptSignature)
	copy(src[4096:], "the data")
	good := filepath.Join(dir, "good.img")
	if err := os.WriteFile(good, src, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "disk.img")
	if err := convertImage(good, dst, 4<<20); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4<<20 || !bytes.Equal(got[:len(src)], src) || bytes.Count(got[len(src):], []byte{0}) != len(got)-len(src) {
		t.Errorf("the disk is %d bytes; want 4 MiB, the source's bytes first and zeros after", len(got))
	}

	blank := filepath.Join(dir, "blank.img")
	if err := os.WriteFile(blank, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := convertImage(blank, filepath.Join(dir, "d2.img"), 4<<20); err == nil || !strings.Contains(err.Error(), "partition table") {
		t.Errorf("no partition table: %v, want an error naming it", err)
	}
	if err := convertImage(good, filepath.Join(dir, "d3.img"), 1<<19); err == nil || !strings.Contains(err.Error(), "does not fit") {
		t.Errorf("a source larger than the disk: %v, want an error", err)
	}
}

// One template, two machines. Linux gets a VirtIO disk, virtio-ramfb, no
// TPM and a UTC clock; a config that names no system is Windows, with the
// values the template used to hold as text.
//
// Negative control, run by hand 2 Oct 2026: tpm: true in linuxGuest fails it.
func TestPlistPerGuest(t *testing.T) {
	drive := func(i DriveInterface) []Drive {
		return []Drive{{ID: "D1", ImageName: diskImage, Type: DriveDisk, Interface: i}}
	}
	lin, err := Config{Name: "l", UUID: "U", MemoryMiB: linuxGuest.memoryMiB, CPUCount: 4, MACAddress: "52:54:00:00:00:01",
		guest: linuxGuest, Drives: drive(linuxGuest.diskIface)}.Plist()
	if err != nil {
		t.Fatal(err)
	}
	win, err := Config{Name: "w", UUID: "U", MemoryMiB: 8192, CPUCount: 4, MACAddress: "52:54:00:00:00:02",
		Drives: drive(windowsGuest.diskIface)}.Plist()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, plist string
		want        []string
	}{
		{"linux", lin, []string{
			"<key>Icon</key><string>linux</string>", "<key>TPMDevice</key><false/>", "<key>RTCLocalTime</key><false/>",
			"<key>Hardware</key><string>virtio-ramfb</string>", "<key>Interface</key><string>VirtIO</string>",
			"<key>MemorySize</key><integer>2048</integer>", "<key>PS2Controller</key><false/>",
		}},
		{"windows", win, []string{
			"<key>Icon</key><string>windows</string>", "<key>TPMDevice</key><true/>", "<key>RTCLocalTime</key><true/>",
			"<key>Hardware</key><string>virtio-ramfb-gl</string>", "<key>Interface</key><string>NVMe</string>",
			"<string>Generated by irgo-windows-vm. Unattended Windows 11 ARM64; login dev/dev, RDP enabled.</string>",
		}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.plist, w) {
				t.Errorf("the %s plist does not contain %s", c.name, w)
			}
		}
		if strings.Contains(c.plist, "%!") {
			t.Errorf("the %s plist has an unfilled value", c.name)
		}
	}
}

// A Linux VM is counted at its own memory, 2 GiB, not a clone's 4 or an
// install's 8: it fits beside irgo-win11, or beside irgo-win11 stopped and a
// clone, and not beside both running.
//
// Negative control, run by hand 2 Oct 2026: with diskForLinux counted at
// vmMemoryMiB, "beside irgo-win11" says no.
func TestDecideCapacityForLinux(t *testing.T) {
	const gib = 1 << 30
	win := vmMemory{Name: "irgo-win11", Status: "started", MiB: 8192}
	clone := vmMemory{Name: "a2", Status: "started", MiB: 4096}
	plan := CapacityPlan{VM: "l1", Disk: diskForLinux}
	for _, c := range []struct {
		name string
		f    capacityFacts
		want Answer
	}{
		{"beside irgo-win11", capacityFacts{plan: plan, host: 16 * gib, vms: []vmMemory{win}, free: 100 * gib}, AnswerYes},
		{"beside a clone", capacityFacts{plan: plan, host: 16 * gib, vms: []vmMemory{clone}, free: 100 * gib}, AnswerYes},
		{"beside irgo-win11 and a clone", capacityFacts{plan: plan, host: 16 * gib, vms: []vmMemory{win, clone}, free: 100 * gib}, AnswerNo},
		{"disk short of the image and its reserve", capacityFacts{plan: plan, host: 16 * gib, free: 17 * gib}, AnswerNo},
		{"disk enough", capacityFacts{plan: plan, host: 16 * gib, free: 18 * gib}, AnswerYes},
		// A clone of the Linux golden image needs a clone's reserve, at a
		// Linux VM's memory. Negative control, run by hand 3 Oct 2026: with
		// diskForLinuxClone's bytes at linuxReserveBytes the first case says
		// no; with its memory at a Windows clone's, the second does.
		{"a Linux clone, disk for a clone", capacityFacts{plan: CapacityPlan{VM: "l2", Disk: diskForLinuxClone}, host: 16 * gib, free: 14 * gib}, AnswerYes},
		{"a Linux clone beside irgo-win11 and a 2 GiB VM", capacityFacts{plan: CapacityPlan{VM: "l2", Disk: diskForLinuxClone}, host: 16 * gib,
			vms: []vmMemory{win, {Name: "l1", Status: "started", MiB: 2048}}, free: 100 * gib}, AnswerYes},
	} {
		if got, why := decideCapacity(c.f); got != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
}

// A cloud image of another pin is prune's to remove; the pinned one is not.
//
// Negative control, run by hand 2 Oct 2026: without the `n == linuxImageName`
// test the pinned image is listed too.
func TestPruneTakesOnlySupersededLinuxImages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(ISODir(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(ISODir(), linuxImagePrefix+"20250101.img")
	for _, p := range []string{LinuxImagePath(), old, filepath.Join(ISODir(), "win11-arm64.esd")} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, it := range PrunePlan(DefaultPrunePolicy, time.Now()) {
		got = append(got, it.Path)
	}
	if !reflect.DeepEqual(got, []string{old}) {
		t.Errorf("prune would remove %v, want only %s", got, old)
	}
}
