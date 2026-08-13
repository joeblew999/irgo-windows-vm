package utmvm

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func buildTestISO(t *testing.T, files map[string]string) string {
	t.Helper()
	src := t.TempDir()
	for name, body := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "test.iso")
	// 64 MiB requested for a few bytes of content, so the trimming below is
	// actually exercised.
	if err := ISOBuildImage(out, src, 64); err != nil {
		t.Fatalf("ISOBuildImage: %v", err)
	}
	return out
}

// The bug this guards: go-diskfs needs a size up front and leaves the remainder
// as a tail of zeros past the end of the volume. macOS mounted such an image
// happily, so it looked correct — but Windows Setup did not read
// autounattend.xml from one and silently fell back to an interactive install.
func TestISOIsTrimmedToVolumeSize(t *testing.T) {
	iso := buildTestISO(t, map[string]string{"autounattend.xml": "<unattend/>"})

	f, err := os.Open(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var b [4]byte
	if _, err := f.ReadAt(b[:], 0x8050); err != nil {
		t.Fatalf("reading volume space size: %v", err)
	}
	want := int64(binary.LittleEndian.Uint32(b[:])) * 2048

	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != want {
		t.Errorf("image is %d bytes but its volume descriptor declares %d.\n"+
			"A tail past the end of the volume is what stopped Windows Setup reading the answer file.",
			st.Size(), want)
	}
	if st.Size() >= 64<<20 {
		t.Errorf("image was not trimmed at all (%d bytes for a few bytes of content)", st.Size())
	}
}

// Without Joliet the image is 8.3 only and autounattend.xml becomes
// AUTOUNAT.XML — a name Windows Setup never looks for, so the install silently
// runs interactive.
func TestISOKeepsLongFilenames(t *testing.T) {
	iso := buildTestISO(t, map[string]string{
		"autounattend.xml":      "<unattend/>",
		"nativeprobe-arm64.exe": "MZ",
	})
	data, err := os.ReadFile(iso)
	if err != nil {
		t.Fatal(err)
	}
	// Joliet stores names as UTF-16BE in the supplementary descriptor.
	for _, name := range []string{"autounattend.xml", "nativeprobe-arm64.exe"} {
		if !bytes.Contains(data, isoEncode16be(name)) {
			t.Errorf("%q not present as a Joliet (UTF-16BE) name; "+
				"without Joliet Setup sees only the 8.3 form and ignores the answer file", name)
		}
	}
}

// ISOInspect must agree with what was written, since it is the gate that stops
// an x86-64 image being used on Apple Silicon — where it boots to a black
// screen with no diagnostic.
func TestInspectISORoundTrip(t *testing.T) {
	iso := buildTestISO(t, map[string]string{
		"efi/boot/bootaa64.efi":                  "stub",
		"efi/microsoft/boot/cdboot_noprompt.efi": "stub",
	})
	info, err := ISOInspect(iso)
	if err != nil {
		t.Fatalf("ISOInspect: %v", err)
	}
	if !info.IsARM64 {
		t.Error("bootaa64.efi was written but ISOInspect reports the image is not ARM64")
	}
	if !info.HasNoPromptLoader {
		t.Error("cdboot_noprompt.efi was written but ISOInspect did not find it")
	}
	if info.SizeBytes == 0 {
		t.Error("SizeBytes not reported")
	}
}

func TestInspectISORejectsNonARM(t *testing.T) {
	iso := buildTestISO(t, map[string]string{"efi/boot/bootx64.efi": "stub"})
	info, err := ISOInspect(iso)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsARM64 {
		t.Error("an x86-64 image was reported as ARM64; it would boot to a black screen on Apple Silicon")
	}
}
