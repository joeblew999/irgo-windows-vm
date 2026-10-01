//go:build !windows

// Not on Windows: these are the parsers alone, on captured output, and they
// would otherwise be rows in every VM's record. go:check runs them on the Mac.

package vmconformance

import (
	"encoding/hex"
	"testing"
)

// The parsers, on output in the form build 26100 (en-US) prints.

func TestParsePowerIndex(t *testing.T) {
	out := `Power Scheme GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Balanced)
  Subgroup GUID: 238c9fa8-0aad-41ed-83f4-97be242c8f20  (Sleep)
    Power Setting GUID: 29f6c1db-86da-48c5-9fdb-f2b67b1f44da  (Sleep after)
      Minimum Possible Setting: 0x00000000
      Maximum Possible Setting: 0xffffffff
      Possible Settings increment: 0x00000001
      Possible Settings units: Seconds
    Current AC Power Setting Index: 0x00000000
    Current DC Power Setting Index: 0x00000708
`
	ac, dc, err := powerIndex(out)
	if err != nil || ac != 0 || dc != 1800 {
		t.Errorf("powerIndex = %d, %d, %v; want 0, 1800", ac, dc, err)
	}
	if _, _, err := powerIndex("The power scheme, subgroup or setting specified does not exist."); err == nil {
		t.Error("an error message parsed as an index")
	}
}

func TestParseMaxPasswordAge(t *testing.T) {
	out := "Force user logoff how long after time expires?:       Never\r\n" +
		"Minimum password age (days):                          0\r\n" +
		"Maximum password age (days):                          Unlimited\r\n" +
		"The command completed successfully.\r\n"
	if got, err := maxPasswordAge(out); got != "Unlimited" || err != nil {
		t.Errorf("maxPasswordAge = %q, %v", got, err)
	}
	if _, err := maxPasswordAge("System error 5 has occurred."); err == nil {
		t.Error("no line parsed as an age")
	}
}

// TestParseWindowsKeysOff: the map the answer file and vm-repair write turns both
// keys off; one that maps only the left key, or maps the right key to
// something, does not.
//
// Negative control, run by hand: dropping the `to == 0` condition passes the
// map that sends the right key to Escape.
func TestParseWindowsKeysOff(t *testing.T) {
	for _, c := range []struct {
		name, hex string
		want      bool
	}{
		{"the answer file's", "00000000000000000300000000005BE000005CE000000000", true},
		{"left only", "00000000000000000200000000005BE000000000", false},
		{"right key to Escape", "00000000000000000300000000005BE001005CE000000000", false},
		{"empty", "00000000000000000100000000000000", false},
	} {
		b, err := hex.DecodeString(c.hex)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := windowsKeysOff(b)
		if got != c.want {
			t.Errorf("%s: %t, want %t", c.name, got, c.want)
		}
	}
	if _, err := windowsKeysOff([]byte{1, 2}); err == nil {
		t.Error("two bytes read as a map")
	}
}

// TestParseShareFacts: PowerShell's JSON, where a one-element list is a bare
// string and an empty one null.
func TestParseShareFacts(t *testing.T) {
	f, err := parseShareFacts(`{"Share":true,"Path":"C:\\irgo-drop","Access":"WIN11ARM\\dev:Full:Allow","Rules":1,` +
		`"RuleEnabled":"True","Direction":"Inbound","Action":"Allow","Remote":"LocalSubnet","Protocol":"TCP","Port":"445",` +
		`"Restrictive":null,"Server":"Running"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Share || f.Path != `C:\irgo-drop` || len(f.Access) != 1 || f.Remote[0] != "LocalSubnet" || len(f.Restrictive) != 0 {
		t.Errorf("%+v", f)
	}
	f, err = parseShareFacts(`{"Restrictive":["File and Printer Sharing (Restrictive) (SMB-In)","x"]}`)
	if err != nil || len(f.Restrictive) != 2 {
		t.Errorf("%+v, %v", f, err)
	}
	if conversionStatus(0) != "fully decrypted" || conversionStatus(9) != "status 9" {
		t.Error("conversionStatus")
	}
}
