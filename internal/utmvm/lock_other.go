//go:build !darwin

package utmvm

// Off macOS there is nothing to lock: the VM work is macOS-only, so two
// mutations cannot race here — they all refuse when they reach the stage that
// needs UTM. A no-op rather than a refusal, so a mutating command still reports
// its own error: a usage mistake on Linux must say "usage", not "the lock is
// macOS-only".

func Acquire(...Lock) (func(), error) {
	return func() {}, nil
}
