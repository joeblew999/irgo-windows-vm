package vmconformance

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// What the suite reads out of Windows' own tools, parsed here, where it can
// be tested on any machine. The guest is en-US (the answer file sets every
// locale), so the English labels are what the tools print.

// powerIndex reads the AC and DC values out of `powercfg /query` for one
// setting: "Current AC Power Setting Index: 0x00000000".
func powerIndex(out string) (ac, dc uint64, err error) {
	found := 0
	for _, line := range strings.Split(out, "\n") {
		label, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		var into *uint64
		switch strings.TrimSpace(label) {
		case "Current AC Power Setting Index":
			into = &ac
		case "Current DC Power Setting Index":
			into = &dc
		default:
			continue
		}
		n, pErr := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(value), "0x"), 16, 64)
		if pErr != nil {
			return 0, 0, fmt.Errorf("powercfg printed %q: %w", strings.TrimSpace(line), pErr)
		}
		*into = n
		found++
	}
	if found != 2 {
		return 0, 0, fmt.Errorf("powercfg printed no AC and DC index: %q", strings.TrimSpace(out))
	}
	return ac, dc, nil
}

// maxPasswordAge is the "Maximum password age (days)" line of `net accounts`:
// "Unlimited", or a number of days.
func maxPasswordAge(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if label, value, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(strings.TrimSpace(label), "Maximum password age") {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("net accounts printed no maximum password age: %q", strings.TrimSpace(out))
}

// windowsKeysOff reports whether a Scancode Map maps both Windows keys —
// left E0 5B and right E0 5C — to nothing, whatever else it maps. The value
// is a header of 8 zero bytes, a count of entries including the terminator,
// the entries (new scancode, old scancode, each two bytes little-endian), and
// a zero terminator.
func windowsKeysOff(m []byte) (bool, error) {
	if len(m) < 16 {
		return false, fmt.Errorf("%d bytes is too short for a Scancode Map", len(m))
	}
	n := int(binary.LittleEndian.Uint32(m[8:12]))
	if n < 1 || len(m) < 12+4*n {
		return false, fmt.Errorf("the map says %d entries and holds %d bytes", n, len(m))
	}
	off := map[uint16]bool{}
	for i := 0; i < n-1; i++ {
		e := m[12+4*i:]
		to, from := binary.LittleEndian.Uint16(e[0:2]), binary.LittleEndian.Uint16(e[2:4])
		if to == 0 {
			off[from] = true
		}
	}
	return off[0xE05B] && off[0xE05C], nil
}

// shareFacts is what one PowerShell call reports about the irgo-drop share
// and its firewall (shareScript in the Windows tests).
type shareFacts struct {
	Share       bool
	Path        string
	Access      strs
	Rules       int
	RuleEnabled string
	Direction   string
	Action      string
	Remote      strs
	Protocol    string
	Port        strs
	Restrictive strs
	Server      string
}

// strs is a list in PowerShell's JSON, which writes one element as a bare
// value and none as null.
type strs []string

func (s *strs) UnmarshalJSON(b []byte) error {
	var one string
	if string(b) == "null" {
		*s = nil
		return nil
	}
	if err := json.Unmarshal(b, &one); err == nil {
		*s = strs{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

func parseShareFacts(out string) (shareFacts, error) {
	var f shareFacts
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &f); err != nil {
		return f, fmt.Errorf("reading what PowerShell said about the share: %w: %q", err, out)
	}
	return f, nil
}

// conversionStatus words BitLocker's GetConversionStatus.
func conversionStatus(n int) string {
	switch n {
	case 0:
		return "fully decrypted"
	case 1:
		return "fully encrypted"
	case 2:
		return "encrypting"
	case 3:
		return "decrypting"
	case 4:
		return "encryption paused"
	case 5:
		return "decryption paused"
	}
	return fmt.Sprintf("status %d", n)
}
