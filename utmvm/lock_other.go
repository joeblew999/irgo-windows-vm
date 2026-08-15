//go:build !darwin

package utmvm

import (
	"errors"
	"runtime"
)

// Off macOS the mutation lock is refused, not approximated.
//
// The VM machinery cannot run here anyway — UTM is macOS-only — but the package
// must still compile, for the same reason sysfile_other.go exists: `doctor`
// tells a Windows or Linux developer what their machine cannot do. A lock that
// silently reported success would claim a serialisation this host does not
// enforce, and a guard that allows when it cannot tell is the failure this
// whole file is about.

func AcquireMutation() (func(), error) {
	return nil, errors.New("utmvm: the mutation lock is macOS-only (host is " + runtime.GOOS + ")")
}

func MutationHeld() (bool, error) {
	return false, errors.New("utmvm: the mutation lock is macOS-only (host is " + runtime.GOOS + ")")
}
