//go:build !darwin

package utmvm

import "errors"

// utmWindow is one of UTM's on-screen windows.
type utmWindow struct {
	id    int
	title string
}

// utmWindows is macOS-only: UTM exists only there.
func utmWindows() ([]utmWindow, error) {
	return nil, errors.New("listing UTM windows needs macOS")
}
