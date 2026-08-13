package utmvm

// UTM itself: finding it, installing it, and its guest tools.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Install reports how UTM got onto the machine, for messages to the user.
type Install struct {
	Path    string
	Version string

	// Compatible is false when the installed major version differs from the one
	// the schema was verified against. Generation is still attempted — refusing
	// outright would be worse than trying and reporting a clear failure — but
	// the caller should say so up front, because that is the likely cause of
	// any "cannot import" that follows.
	Compatible bool
}

// DetectUTM reports the installed UTM, or ErrUTMNotInstalled.
func DetectUTM() (Install, error) {
	var in Install
	if _, err := os.Stat(AppPath); err != nil {
		return in, ErrUTMNotInstalled
	}
	in.Path = AppPath

	// plutil ships with every macOS install and this file only ever runs on
	// macOS, so reaching for it is not a dependency a developer must satisfy.
	// App Info.plist files are usually binary, so plain XML parsing would fail
	// on most machines — the alternative is a binary-plist decoder for one
	// string.
	out, err := exec.Command("plutil", "-extract", "CFBundleShortVersionString",
		"raw", "-o", "-", AppPath+"/Contents/Info.plist").Output()
	if err != nil {
		// Present but unreadable: still usable, just unknown.
		in.Version = "unknown"
		in.Compatible = true
		return in, nil
	}
	in.Version = strings.TrimSpace(string(out))
	in.Compatible = sameMajor(in.Version, VerifiedVersion)
	return in, nil
}

// EnsureUTM returns the installed UTM, installing it if missing.
//
// A developer should not have to read a prerequisites list before the tool
// works: UTM's signed .dmg is
// downloaded from its GitHub release and the app copied out, so nothing has to
// be installed first.
func EnsureUTM() (Install, error) {
	in, err := DetectUTM()
	if err == nil {
		return in, nil
	}
	if !errors.Is(err, ErrUTMNotInstalled) {
		return in, err
	}

	// Downloaded directly, not via a package manager.
	//
	// "Install a package manager, then install UTM" is two prerequisites for a tool
	// whose whole claim is that a single binary sets the machine up. UTM
	// publishes a signed .dmg on its GitHub releases, so there is nothing to
	// require: fetch it, mount it, copy the app out.
	//
	if dlErr := InstallUTMFromRelease(nil); dlErr != nil {
		return in, dlErr
	}
	return DetectUTM()
}

// EnsureGuestTools returns the guest tools ISO, downloading it if UTM has not
// already cached one.
//
// It writes to UTM's own cache location, so UTM and every VM generated
// afterwards pick it up exactly as if the GUI had fetched it.
func EnsureGuestTools() (string, error) {
	p, err := GuestToolsISO()
	if err == nil {
		return p, nil
	}
	return FetchGuestTools(nil)
}

// FetchGuestTools downloads the guest tools into UTM's cache and returns the
// path. progress, if non-nil, is called as bytes arrive.
func FetchGuestTools(progress func(done, total int64)) (string, error) {
	dest, err := guestToolsPath()
	if err != nil {
		return "", err
	}
	if _, sErr := os.Stat(dest); sErr == nil {
		return dest, nil
	}
	if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
		return "", mkErr
	}

	// No SHA to check against: UTM publishes none, and the file it serves
	// changes with every guest-tools release. The size check below is the only
	// guard available — a truncated download here presents later as a VM with
	// no network, which is a long way from the cause.
	if dErr := isoDownload(GuestToolsURL, dest, "", progress); dErr != nil {
		return "", fmt.Errorf("downloading UTM guest tools: %w\n"+
			"  Alternatively, open UTM once and choose \"Install Windows guest tools\" "+
			"from any VM's menu; it caches the ISO in the same place", dErr)
	}
	fi, sErr := os.Stat(dest)
	if sErr != nil {
		return "", sErr
	}
	if fi.Size() < 10<<20 {
		_ = os.Remove(dest)
		return "", fmt.Errorf("guest tools download was only %s, which is too small to be the ISO",
			HumanBytes(fi.Size()))
	}
	return dest, nil
}

// sameMajor compares leading version components. A patch or minor difference
// from the verified version has never broken the schema in practice; a major
// difference is exactly when UTM has changed ConfigurationVersion before.
func sameMajor(got, want string) bool {
	g, w := majorOf(got), majorOf(want)
	return g != -1 && g == w
}
func majorOf(v string) int {
	part, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	n, err := strconv.Atoi(part)
	if err != nil {
		return -1
	}
	return n
}

// guestToolsPath is where UTM caches the guest tools, and therefore where a
// download of our own has to land for UTM and every generated VM to find it.
func guestToolsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(utmContainerDir(home),
		"Library", "Application Support", utmToolsDir, utmToolsISO), nil
}

// GuestToolsISO returns UTM's downloaded guest tools image, if present.
//
// Installing these inside the guest is what gives the QEMU guest agent, and
// therefore `utmctl exec` and `utmctl ip-address`. Without it a VM boots fine
// but cannot be driven from the host at all — which defeats the point of
// generating one from a script.
func GuestToolsISO() (string, error) {
	p, err := guestToolsPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("UTM guest tools not downloaded yet: %w\n"+
			"Open UTM once and let it fetch them, or the guest agent will be unavailable", err)
	}
	return p, nil
}

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
	defer func() { _ = os.RemoveAll(tmp) }() // best effort; the download already succeeded or failed

	dmg := filepath.Join(tmp, "UTM.dmg")
	fmt.Fprintf(os.Stderr, "downloading UTM from %s\n", url)
	if dErr := isoDownload(url, dmg, "", progress); dErr != nil {
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
		return fmt.Errorf("utmvm: copying UTM.app to /Applications: %w\n"+ //nolint:staticcheck // ST1005: multi-line on purpose — these tell the user the next command to run
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
	defer func() { _ = resp.Body.Close() }()
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

// VerifiedVersion is the UTM release this package's config schema was read
// from — not guessed, but taken from utmapp/UTM at that tag.
//
// This matters more than it looks. UTM's config.plist is decoded by Swift
// Codable with non-optional fields, and a schema mismatch surfaces as a single
// generic "cannot import this VM" with no indication of which field is wrong.
// Reading `main` instead of the matching tag is how an afternoon disappears:
// at the time of writing main was v5.0.4 while the installed app was v4.7.5,
// and they disagreed.
const VerifiedVersion = "4.7.5"

// GuestToolsURL is where UTM itself downloads the guest tools from.
//
// Taken from the string table of UTM.app's own binary rather than guessed, so
// it is the URL the application uses and not one that merely happens to work
// today.
//
// Fetching it ourselves is what makes a one-command setup possible at all. The
// alternative — and what this used to say — was to tell the developer to open
// UTM, create a throwaway VM and pick "Install Windows guest tools" from a
// menu. That is not a step a setup command can take, it is not discoverable,
// and skipping it produces a VM that boots perfectly and is then unreachable:
// no network, no `utmctl exec`, no IP, and nothing saying why.
const GuestToolsURL = "https://getutm.app/downloads/utm-guest-tools-latest.iso"

// utmReleaseAPI is the GitHub release the .dmg comes from. Latest rather than a
// pin: UTM's schema version is checked separately at DetectUTM, so a mismatch
// is reported rather than silently accepted, and pinning here would install a
// version older than the one a developer would get by hand.
const utmReleaseAPI = "https://api.github.com/repos/utmapp/UTM/releases/latest"
