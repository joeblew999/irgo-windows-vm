package utmvm

// Installing UTM from nothing.
//
// The promise this project makes is that a developer runs one binary on a
// machine with nothing on it. "First install Homebrew, then install UTM" is two
// prerequisites for a tool whose entire job is removing them.
//
// UTM publishes a signed .dmg on its GitHub releases, so there is nothing to
// require — fetch it, mount it, copy the app out, unmount. That is the whole
// procedure, and it is the same one a person performs by hand.
//
// `hdiutil` is used to mount, and it is the one place in this repository where
// that is unavoidable: a .dmg is an APFS or HFS+ filesystem in a wrapper, and
// there is no Go implementation that reads either. The README's "no hdiutil"
// is about generating disk images, which this repo does do in Go; reading
// somebody else's signed installer is a different problem with no Go answer.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// utmReleaseAPI is the GitHub release the .dmg comes from. Latest rather than a
// pin: UTM's schema version is checked separately at DetectUTM, so a mismatch
// is reported rather than silently accepted, and pinning here would install a
// version older than the one a developer would get by hand.
const utmReleaseAPI = "https://api.github.com/repos/utmapp/UTM/releases/latest"

// InstallUTMFromRelease downloads UTM's .dmg and copies the app to
// /Applications.
//
// It refuses rather than overwrites if UTM is already there: replacing an
// application somebody installed deliberately, possibly a different version
// they are testing against, is not a setup command's decision to make.
func InstallUTMFromRelease(progress func(done, total int64)) error {
	if _, err := os.Stat(AppPath); err == nil {
		return fmt.Errorf("utmvm: %s already exists; not replacing it", AppPath)
	}

	url, err := latestUTMDMG()
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "irgo-utm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	dmg := filepath.Join(tmp, "UTM.dmg")
	fmt.Fprintf(os.Stderr, "downloading UTM from %s\n", url)
	if dErr := Download(url, dmg, "", progress); dErr != nil {
		return fmt.Errorf("utmvm: downloading UTM: %w", dErr)
	}

	mnt := filepath.Join(tmp, "mnt")
	if mkErr := os.MkdirAll(mnt, 0o755); mkErr != nil {
		return mkErr
	}
	// -nobrowse keeps it out of Finder, -readonly is all that is needed, and
	// -noverify skips a checksum pass that adds a minute to a 238 MB image the
	// system will verify the signature of anyway when the app is launched.
	attach := exec.Command("hdiutil", "attach", dmg,
		"-nobrowse", "-readonly", "-noverify", "-mountpoint", mnt)
	attach.Stderr = os.Stderr
	if aErr := attach.Run(); aErr != nil {
		return fmt.Errorf("utmvm: mounting the UTM disk image: %w", aErr)
	}
	defer func() {
		detach := exec.Command("hdiutil", "detach", mnt, "-quiet")
		_ = detach.Run()
	}()

	src := filepath.Join(mnt, "UTM.app")
	if _, sErr := os.Stat(src); sErr != nil {
		return fmt.Errorf("utmvm: the disk image has no UTM.app in it: %w", sErr)
	}

	// ditto, not a Go file walk: an application bundle carries extended
	// attributes, symlinks and a code signature, and copying it byte-by-byte
	// without them produces a bundle Gatekeeper refuses to launch with a
	// message about being damaged.
	fmt.Fprintf(os.Stderr, "copying UTM.app to %s\n", AppPath)
	cp := exec.Command("ditto", src, AppPath)
	cp.Stdout, cp.Stderr = os.Stderr, os.Stderr
	if cErr := cp.Run(); cErr != nil {
		return fmt.Errorf("utmvm: copying UTM.app to /Applications: %w\n"+
			"  If this is a permissions error, /Applications may need admin rights;\n"+
			"  download UTM from https://mac.getutm.app and drag it across instead.", cErr)
	}
	return nil
}

// latestUTMDMG finds the macOS build in UTM's newest release.
//
// By name rather than by position: the release also carries .ipa builds for
// iOS and visionOS and a .deb, and any of them would download happily and be
// useless.
func latestUTMDMG() (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, utmReleaseAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("utmvm: asking GitHub for UTM's latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("utmvm: GitHub returned %s for UTM's releases", resp.Status)
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("utmvm: parsing UTM's release: %w", err)
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, "UTM.dmg") {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("utmvm: UTM release %s has no UTM.dmg among its %d assets",
		rel.TagName, len(rel.Assets))
}
