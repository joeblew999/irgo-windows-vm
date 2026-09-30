package utmvm

import (
	"encoding/xml"
	"strings"
	"testing"
)

// FuzzPlistWellFormed asserts the generated plist always parses as XML.
//
// Names reach this from a -name flag, a config file or CI, and a single
// unescaped & or < produces a document UTM rejects with its usual unhelpful
// "cannot import this VM". A malformed plist is a silent failure much later,
// so it is worth proving the output is well-formed for any input rather than
// for the handful of names anyone thought to try.
func FuzzPlistWellFormed(f *testing.F) {
	for _, seed := range []string{
		"Win11ARM", "dev & test", "<script>", "quote\"name", "'apostrophe",
		"emoji 🚀", "tab\there", "newline\nhere", "", strings.Repeat("x", 500),
		"]]>", "&amp;", "\x00null",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		cfg := Config{
			Name: name, UUID: "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE",
			MemoryMiB: 8192, CPUCount: 4, MACAddress: "52:54:00:11:22:33",
			Drives: []Drive{{ID: "D1", ImageName: "disk.img", Type: DriveDisk, Interface: IfaceNVMe}},
		}
		out, err := cfg.Plist()
		if err != nil {
			return // rejecting input is fine; emitting broken XML is not
		}

		// Control characters are not representable in XML 1.0 at all, so a name
		// containing one cannot produce a valid document. Escaping cannot fix
		// that; the input has to be refused.
		if hasXMLIllegalRune(name) {
			t.Fatalf("accepted a name with a control character (%q); it must be rejected, "+
				"since XML 1.0 cannot represent it and UTM will refuse the file", name)
		}

		if err := xml.Unmarshal([]byte(out), new(struct {
			XMLName xml.Name `xml:"plist"`
		})); err != nil {
			t.Fatalf("generated plist is not well-formed XML for name %q: %v", name, err)
		}
	})
}

func hasXMLIllegalRune(s string) bool {
	for _, r := range s {
		if r == 0x09 || r == 0x0A || r == 0x0D {
			continue
		}
		if r < 0x20 || (r >= 0xD800 && r <= 0xDFFF) || r == 0xFFFE || r == 0xFFFF {
			return true
		}
	}
	return false
}
