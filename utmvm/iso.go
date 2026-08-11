package utmvm

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

// ISOInfo is what we can learn about Windows install media without mounting it.
type ISOInfo struct {
	// IsARM64 reports whether the ARM64 UEFI bootloader is present. An x86-64
	// ISO on Apple Silicon boots to a black screen with no diagnostic at all,
	// so this is the single most valuable check here.
	IsARM64 bool

	// HasNoPromptLoader reports whether cdboot_noprompt.efi is present. The
	// default loader (efi/boot/bootaa64.efi) prints "Press any key to boot from
	// CD or DVD" and gives up after about five seconds, which stops an
	// "unattended" install before it starts. The _noprompt variant is what
	// makes hands-off installation possible.
	HasNoPromptLoader bool

	// SizeBytes is the image size, useful for a sanity check: a Windows 11
	// image is several GB, and a few hundred MB means a truncated download.
	SizeBytes int64
}

// InspectISO reports what matters about Windows install media.
//
// Implementation note, because the obvious approach does not work: Windows 11
// ISOs are UDF, not ISO9660 — they have to be, since install.wim exceeds
// ISO9660's 4 GB file limit. Filesystem libraries that read the ISO9660
// descriptor find only a stub and fail to traverse it.
//
// Rather than embed a UDF reader for two yes/no questions, this streams the
// image looking for the filenames in the directory structures. UDF stores them
// as either 8-bit or UTF-16BE strings, so both encodings are searched. That is
// a heuristic — it proves a name is present, not that it is at a particular
// path — but for "is there an ARM64 bootloader in here" it is decisive, and it
// is honest about being a scan rather than pretending to be a parse.
func InspectISO(path string) (ISOInfo, error) {
	var info ISOInfo

	f, err := os.Open(path)
	if err != nil {
		return info, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return info, err
	}
	info.SizeBytes = st.Size()

	needles := [][]byte{
		encode8("BOOTAA64.EFI"), encode16be("bootaa64.efi"),
		encode8("CDBOOT_NOPROMPT.EFI"), encode16be("cdboot_noprompt.efi"),
	}
	found := make([]bool, len(needles))

	// A modest buffer with an overlap, so a name spanning a chunk boundary is
	// still matched. The overlap must exceed the longest needle.
	const chunk = 1 << 20
	const overlap = 128
	buf := make([]byte, chunk+overlap)
	var carry int

	for {
		n, err := io.ReadFull(f, buf[carry:chunk+overlap])
		if n > 0 {
			window := buf[:carry+n]
			up := bytes.ToUpper(window)
			for i, needle := range needles {
				if found[i] {
					continue
				}
				if bytes.Contains(up, bytes.ToUpper(needle)) {
					found[i] = true
				}
			}
			if allTrue(found) {
				break
			}
			// Keep the tail so the next pass can match across the seam.
			if len(window) > overlap {
				copy(buf, window[len(window)-overlap:])
				carry = overlap
			} else {
				carry = len(window)
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return info, fmt.Errorf("reading %s: %w", path, err)
		}
	}

	info.IsARM64 = found[0] || found[1]
	info.HasNoPromptLoader = found[2] || found[3]
	return info, nil
}

func allTrue(b []bool) bool {
	for _, v := range b {
		if !v {
			return false
		}
	}
	return true
}

// encode8 is the plain 8-bit form UDF uses for names that fit in Latin-1.
func encode8(s string) []byte { return []byte(strings.ToUpper(s)) }

// encode16be is UDF's UTF-16BE form. Note big-endian: UDF differs from the
// little-endian UTF-16 used elsewhere in Windows, and searching for the wrong
// byte order silently finds nothing.
func encode16be(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range utf16.Encode([]rune(s)) {
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}
