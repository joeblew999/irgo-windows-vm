package utmvm

import (
	"errors"
	"strings"
	"testing"
)

// TestVerifyGolden: the VM suite's verdict on the verification clone is
// recorded in the manifest whatever it is; a failing one refuses the image;
// no check is recorded as not run and lets it through.
//
// Negative control, run by hand: returning nil from verifyGolden when verify
// errs lets the failing image through, and the second case fails.
func TestVerifyGolden(t *testing.T) {
	quiet := func(string, ...any) {}
	var m GoldenManifest
	if err := verifyGolden(func(vm string) (string, error) {
		if vm != "irgo-golden-verify" {
			t.Errorf("verified %q", vm)
		}
		return "YES: 30 passed, 0 skipped", nil
	}, "irgo-golden-verify", &m, quiet); err != nil || m.VMCheck != "YES: 30 passed, 0 skipped" {
		t.Errorf("passing: err %v, recorded %q", err, m.VMCheck)
	}

	m = GoldenManifest{}
	err := verifyGolden(func(string) (string, error) {
		return "NO: failed: TestHibernationOff", errors.New("glaze check failed")
	}, "irgo-golden-verify", &m, quiet)
	if !errors.Is(err, ErrGoldenUnverified) || !strings.Contains(err.Error(), "TestHibernationOff") {
		t.Errorf("failing: err %v", err)
	}
	if m.VMCheck != "NO: failed: TestHibernationOff" {
		t.Errorf("failing: recorded %q", m.VMCheck)
	}

	// CANNOT TELL is an error too: an image nobody could check is not passed.
	m = GoldenManifest{}
	if err := verifyGolden(func(string) (string, error) { return "", ErrNoAgent }, "v", &m, quiet); !errors.Is(err, ErrGoldenUnverified) {
		t.Errorf("cannot tell: err %v", err)
	}

	m = GoldenManifest{}
	if err := verifyGolden(nil, "v", &m, quiet); err != nil || !strings.HasPrefix(m.VMCheck, "not run") {
		t.Errorf("no check: err %v, recorded %q", err, m.VMCheck)
	}
}
