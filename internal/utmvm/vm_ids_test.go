package utmvm

import (
	"regexp"
	"testing"
)

// TestIdentifiersAreRandomAndWellFormed covers newUUID and randomMAC, which
// lost their error returns when crypto/rand.Read stopped being able to fail.
//
// What matters is what UTM gets: a version-4 UUID in upper case (an empty or
// malformed Identifier is rejected with the generic "cannot import this VM"),
// and a MAC in QEMU's 52:54:00 range that differs between VMs (the all-zero
// address every VM once got is an L2 collision).
//
// Negative control, run by hand: dropping the version-bit line in newUUID fails
// the pattern; returning a constant from randomMAC fails the "differ" check.
func TestIdentifiersAreRandomAndWellFormed(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`)
	mac := regexp.MustCompile(`^52:54:00:[0-9A-F]{2}:[0-9A-F]{2}:[0-9A-F]{2}$`)

	seenU, seenM := map[string]bool{}, map[string]bool{}
	for range 64 {
		u, m := newUUID(), randomMAC()
		if !uuid.MatchString(u) {
			t.Fatalf("newUUID() = %q, not an upper-case version-4 UUID", u)
		}
		if !mac.MatchString(m) {
			t.Fatalf("randomMAC() = %q, not in 52:54:00:XX:XX:XX", m)
		}
		seenU[u], seenM[m] = true, true
	}
	// 64 draws from 2^24 MACs collide with probability ~1e-4; a MAC that never
	// varies gives exactly one.
	if len(seenU) != 64 || len(seenM) < 60 {
		t.Errorf("identifiers repeat: %d distinct UUIDs and %d distinct MACs in 64 draws", len(seenU), len(seenM))
	}
}
