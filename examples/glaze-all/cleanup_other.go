//go:build !darwin && !windows

package main

import (
	"errors"
	"fmt"

	"github.com/crgimenes/glaze"
)

// Only the Mac and Windows are run by glaze-check. Elsewhere nothing is opened
// that could not be closed: openAndClose refuses to make the call when it
// cannot list windows first, and says so as unsupported.
var errNoWindowList = fmt.Errorf("closing file-manager windows is implemented for macOS and Windows: %w", errors.ErrUnsupported)

func listFileManagerWindows() ([]fmWindow, error) { return nil, errNoWindowList }
func closeFileManagerWindow(uint64) error         { return errNoWindowList }
func placeMatches(string, string) bool            { return false }
func shownInExisting([]fmWindow, string) (bool, error) {
	return false, errNoWindowList
}
func dismissFileDialog(glaze.WebView, string) error {
	return errNoWindowList
}
