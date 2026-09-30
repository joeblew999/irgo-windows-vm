package utmvm

import (
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{
		Name: "Win11ARM", UUID: "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE",
		MemoryMiB: 8192, CPUCount: 4, MACAddress: "52:54:00:11:22:33",
		Drives: []Drive{
			{ID: "D1", ImageName: "disk.img", Type: DriveDisk, Interface: IfaceNVMe},
			{ID: "D2", ImageName: "install.iso", Type: DriveCD, Interface: IfaceUSB, ReadOnly: true},
		},
	}
}

// UTM rejects a config with a single generic "cannot import this VM" message
// that names no field, so a missing key costs an hour of bisecting rather than
// a glance. Each case below is a real failure we hit; the test exists so a
// future tidy-up cannot silently reintroduce one.
func TestPlistContainsFieldsUTMRequires(t *testing.T) {
	got, err := testConfig().Plist()
	if err != nil {
		t.Fatalf("Plist() error: %v", err)
	}

	for _, tc := range []struct{ fragment, why string }{
		{"<key>PS2Controller</key>",
			"decoded with a non-optional decode() and no default; omitting it fails the whole document"},
		{"<key>UsbBusSupport</key><string>3.0</string>",
			`the enum's raw values are "2.0"/"3.0"; "USB3_0" is rejected`},
		{"<key>CPUFlagsAdd</key>", "the key is CPUFlagsAdd/CPUFlagsRemove, not CPUFlags"},
		{"<key>CPUFlagsRemove</key>", "the key is CPUFlagsAdd/CPUFlagsRemove, not CPUFlags"},
		{"<key>RTCLocalTime</key>", "the key is RTCLocalTime, not RTCUseLocalTime"},
		{"<key>ConfigurationVersion</key><integer>4</integer>", "UTM v4 accepts version 4 only"},
		{"<key>Backend</key><string>QEMU</string>", "backend enum is exactly \"QEMU\""},
	} {
		if !strings.Contains(got, tc.fragment) {
			t.Errorf("plist missing %q\n  why it matters: %s", tc.fragment, tc.why)
		}
	}
}

// virtio-gpu-pci leaves the aarch64 guest with no framebuffer and no legacy VGA
// fallback, so Windows boots invisibly and looks hung — including the
// "Press any key to boot from CD" prompt nobody can see.
func TestDisplayIsRamfbForAarch64(t *testing.T) {
	got, err := testConfig().Plist()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "<key>Hardware</key><string>virtio-ramfb-gl</string>") {
		t.Error("display must be virtio-ramfb-gl; virtio-gpu-pci gives no framebuffer on aarch64")
	}
	if strings.Contains(got, "virtio-gpu-pci") {
		t.Error("virtio-gpu-pci must not be used for Windows on aarch64")
	}
}

// Windows ARM64 has no inbox VirtIO storage driver, so a VirtIO system disk is
// invisible to VMCreate and it reports that no drive can be found.
func TestSystemDiskIsNVMe(t *testing.T) {
	got, err := testConfig().Plist()
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(got, "disk.img")
	if i < 0 {
		t.Fatal("system disk missing from plist")
	}
	if !strings.Contains(got[i:i+300], "<string>NVMe</string>") {
		t.Error("system disk must use the NVMe interface for Windows ARM64")
	}
}

func TestPlistRejectsIncompleteConfig(t *testing.T) {
	for name, c := range map[string]Config{
		"no name":   {UUID: "x", Drives: []Drive{{}}},
		"no uuid":   {Name: "x", Drives: []Drive{{}}},
		"no drives": {Name: "x", UUID: "y"},
	} {
		if _, err := c.Plist(); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func TestNameIsXMLEscaped(t *testing.T) {
	c := testConfig()
	c.Name = "dev & test <vm>"
	got, err := c.Plist()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "dev &amp; test &lt;vm&gt;") {
		t.Error("VM name must be XML-escaped or the plist is malformed")
	}
}
