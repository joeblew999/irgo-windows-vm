package utmvm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ---- from ensure.go ----
// AppPath is where Homebrew's cask and the direct download both install UTM.
const AppPath = "/Applications/UTM.app"

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

// SchemaConfigurationVersion is the value UTM v4 accepts. UTM rejects anything
// higher outright, so a major-version jump is a real compatibility signal
// rather than cosmetic drift.
const SchemaConfigurationVersion = 4

// ErrUTMNotInstalled is returned when UTM is absent and could not be installed.
var ErrUTMNotInstalled = errors.New("UTM is not installed")

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
// works. Homebrew is used when present; otherwise UTM's signed .dmg is
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

	// Download it ourselves rather than requiring Homebrew.
	//
	// "Install Homebrew, then install UTM" is two prerequisites for a tool
	// whose whole claim is that a single binary sets the machine up. UTM
	// publishes a signed .dmg on its GitHub releases, so there is nothing to
	// require: fetch it, mount it, copy the app out.
	//
	// Homebrew is still used when it is present, because a developer who
	// manages their applications with it will want UTM in that inventory rather
	// than dropped into /Applications behind its back.
	if BrewPath() != "" {
		if runErr := BrewInstall("utm", true); runErr == nil {
			return DetectUTM()
		}
		fmt.Fprintln(os.Stderr, "Homebrew could not install it; downloading the .dmg instead...")
	}

	if dlErr := InstallUTMFromRelease(nil); dlErr != nil {
		return in, dlErr
	}
	return DetectUTM()
}

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
	if dErr := Download(GuestToolsURL, dest, "", progress); dErr != nil {
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
	return filepath.Join(home, "Library", "Containers", "com.utmapp.UTM", "Data",
		"Library", "Application Support", "GuestSupportTools", "utm-guest-tools-latest.iso"), nil
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

// ---- from installutm.go ----
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

// ---- from brew.go ----
// Installing the things this project needs, in one place.
//
// There are three of them — UTM, wimlib, xorriso — and before this they were
// three different stories: UTM shelled out to brew inline, wimlib and xorriso
// printed a `brew install` line for the developer to copy, and each had its own
// idea of what to say when brew was missing. Same job, three implementations,
// three behaviours.
//
// A developer running one binary should not be handed a shopping list. If brew
// is there, use it; if it is not, say so once, in one voice.

// BrewPath returns the Homebrew binary, or "" when it is not installed.
func BrewPath() string {
	p, err := exec.LookPath("brew")
	if err != nil {
		return ""
	}
	return p
}

// BrewInstall installs a formula or cask.
//
// Output goes to stderr as it happens rather than being captured: these take
// tens of seconds to minutes, and a silent command that long reads as a hang.
func BrewInstall(name string, cask bool) error {
	brew := BrewPath()
	if brew == "" {
		return fmt.Errorf("Homebrew is not installed")
	}
	args := []string{"install"}
	if cask {
		args = append(args, "--cask")
	}
	args = append(args, name)

	fmt.Fprintf(os.Stderr, "installing %s with Homebrew...\n", name)
	cmd := exec.Command(brew, args...) //nolint:gosec // name comes from this package's own tables
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// Ensure makes the tool available, installing it with Homebrew if it is not.
//
// The error when that is impossible names the tool, what it is for, and the one
// command that fixes it — because "executable file not found in $PATH" tells a
// developer nothing about which of three tools is missing or why this project
// wants it.
func (t *Tool) Ensure() error {
	if t.resolve() {
		return nil
	}
	if BrewPath() == "" {
		return fmt.Errorf("%s is needed to %s, and Homebrew is not installed to fetch it.\n"+
			"  Install Homebrew from https://brew.sh, then: %s",
			t.Name, t.Why, t.Install())
	}
	if err := BrewInstall(t.Formula, false); err != nil {
		return fmt.Errorf("installing %s: %w\n  Try it by hand: %s", t.Name, err, t.Install())
	}
	if !t.resolve() {
		return fmt.Errorf("%s installed but %s is still not on PATH", t.Formula, t.Name)
	}
	return nil
}

// ---- from host.go ----
// Target is a desktop build a developer might need to run.
type Target string

const (
	TargetMacOS   Target = "macos"
	TargetWindows Target = "windows"
	TargetLinux   Target = "linux"
)

// Coverage describes how — or whether — the current host can run a target.
type Coverage struct {
	Target Target
	How    string // "native", "vm", or "" when unavailable
	Note   string
}

// Runnable reports whether the target can actually be exercised here.
func (c Coverage) Runnable() bool { return c.How != "" }

// HostCoverage reports what desktop builds this machine can genuinely run.
//
// The asymmetry is the point, and it drives which machine a team should give
// to whoever owns the desktop app:
//
//   - macOS on Apple Silicon is a superset. It runs the macOS build natively
//     and the Windows build in a VM, because UTM virtualises Windows ARM64
//     with hardware acceleration.
//   - Windows can only run the Windows build. There is no UTM for Windows, and
//     nesting a macOS guest is not permitted by Apple's licence regardless.
//   - Linux can run the Linux build, and Windows via KVM — but not from this
//     tool, which generates UTM bundles.
//
// A developer on Windows is therefore not doing anything wrong when the VM
// commands refuse to run; they are simply on the narrower machine. Saying so
// plainly beats an error about a missing directory.
func HostCoverage() []Coverage {
	switch runtime.GOOS {
	case "darwin":
		cov := []Coverage{
			{TargetMacOS, "native", "runs directly on this machine"},
		}
		if runtime.GOARCH == "arm64" {
			cov = append(cov, Coverage{TargetWindows, "vm",
				"Windows 11 ARM64 under UTM, hardware-accelerated via HVF"})
		} else {
			cov = append(cov, Coverage{TargetWindows, "",
				"Intel Macs would emulate ARM64 or run x64 Windows unaccelerated; not worth it"})
		}
		cov = append(cov, Coverage{TargetLinux, "",
			"buildable here, but this tool only generates Windows VMs"})
		return cov

	case "windows":
		return []Coverage{
			{TargetWindows, "native", "runs directly on this machine"},
			{TargetMacOS, "", "macOS cannot be virtualised on non-Apple hardware"},
			{TargetLinux, "", "possible via WSL2 or Hyper-V; out of scope for this tool"},
		}

	case "linux":
		return []Coverage{
			{TargetLinux, "native", "runs directly on this machine"},
			{TargetWindows, "", "possible via KVM (see dockur/windows); this tool generates UTM bundles"},
			{TargetMacOS, "", "macOS cannot be virtualised on non-Apple hardware"},
		}
	}
	return nil
}

// CanCreateVMs reports whether VM subcommands can do anything here. Callers
// should prefer this over comparing runtime.GOOS so the reason stays in one
// place.
func CanCreateVMs() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

// ---- from external.go ----
// Everything this project depends on that does not live in the repository.
//
// There is a lot of it, it is large, and none of it is in git — so a clone is
// nowhere near enough to run any of this, and the gap is invisible until
// something fails a long way from the cause. A missing guest-tools ISO does not
// say "missing guest-tools ISO"; it says the VM has no network and `utmctl
// exec` does nothing.
//
// Each entry names what it is, where it is, why it is outside, and how to get
// it back. `irgo-winvm doctor` prints them with their real sizes and whether
// they are actually there, so the answer is measured rather than remembered.
//
// The sizes are the reason for most of it: ~33 GB of ISO and disk image, which
// belongs in git under no circumstances, plus a VM bundle that is machine state
// rather than source.

// Externals returns every file and directory outside the repository that the
// project relies on, in the order a new machine acquires them.
//
// repoRoot may be empty; the entries that live inside the working tree but
// outside git are then skipped rather than guessed at.
func Externals(repoRoot string) []External {
	home, _ := os.UserHomeDir()
	utmData := filepath.Join(home, "Library", "Containers", "com.utmapp.UTM", "Data")

	// Honour the same overrides everything else does, so an inventory taken
	// with IRGO_CACHE_DIR set reports where the files actually are rather than
	// where they would be by default — which is the whole point of taking one.
	paths := DefaultPaths()
	if repoRoot != "" {
		paths.Root = repoRoot
		if os.Getenv("IRGO_CACHE_DIR") == "" {
			paths.Cache = filepath.Join(repoRoot, ".cache")
		}
		if os.Getenv("IRGO_BIN_DIR") == "" {
			paths.Bin = filepath.Join(repoRoot, ".bin")
		}
		if os.Getenv("IRGO_WORK_DIR") == "" {
			paths.Work = filepath.Join(repoRoot, ".work")
		}
	}

	list := []External{
		{
			Name: "Go toolchain",
			Path: lookPath("go"),
			Why: "deliberately not pinned by mise: each go.mod's `go` directive is a floor and " +
				"the toolchain mechanism fetches what a module needs. Two managers on that job disagree.",
			Fix:  "install Go however you like; go.mod will say if it is too old",
			Kind: KindTool,
		},
		{
			Name: "UTM.app",
			Path: AppPath,
			Why:  "the hypervisor. Everything here drives it through utmctl, which lives inside the bundle.",
			Fix:  "brew install --cask utm   (or `irgo-winvm doctor`, which offers to)",
			Kind: KindTool,
		},
		{
			Name: "UTM guest tools ISO",
			Path: filepath.Join(utmData, "Library", "Application Support",
				"GuestSupportTools", "utm-guest-tools-latest.iso"),
			Why: "the QEMU guest agent and the virtio-net driver. Without it a VM boots and " +
				"is then unreachable: no network, no `utmctl exec`, no IP.",
			Fix:  "open UTM once and let it download them; there is no supported way to fetch them ourselves",
			Kind: KindTool,
		},
		{
			Name: "Windows 11 ARM64 ISO",
			Path: paths.ISO(),
			Why: "the installation media. Microsoft's, not redistributable, and 5 GB. " +
				"Hardlinked into .cache so nothing depends on ~/Downloads and the second copy costs no space.",
			Fix:  "fetch with CrystalFetch, then: mkdir -p .cache && ln <the.iso> .cache/win11-arm64.iso",
			Kind: KindMedia,
			Skip: repoRoot == "",
		},
		{
			Name: "the VM itself",
			Path: paths.VMs,
			Why: "machine state, not source: a 64 GB sparse disk with Windows installed on it. " +
				"Rebuildable from the ISO in about an hour, unattended.",
			Fix:  "irgo-winvm up -iso .cache/win11-arm64.iso -name irgo-win11",
			Kind: KindState,
			Dir:  true,
		},
		{
			Name: "probe binaries",
			Path: paths.Bin,
			Why:  "cross-compiled Windows binaries. Gitignored, rebuilt in seconds, but a restart used to lose them.",
			Fix:  "mise run probes",
			Kind: KindBuilt,
			Dir:  true,
			Skip: repoRoot == "",
		},
		{
			Name: "glaze clone",
			Path: filepath.Join(paths.Upstream, "glaze"),
			Why: "only needed to FIX glaze rather than work around it, which is this repo's rule. " +
				"Uncommitted work in here is invisible from this repository and easy to lose.",
			Fix:  "mise run upstream:clone   (then upstream:diff to see what is in it)",
			Kind: KindUpstream,
			Dir:  true,
		},
		{
			Name: "native clone",
			Path: filepath.Join(paths.Upstream, "native"),
			Why:  "as above, for crgimenes/native.",
			Fix:  "mise run upstream:clone",
			Kind: KindUpstream,
			Dir:  true,
		},
	}

	// One inode set across the whole inventory, not one per entry.
	//
	// The Windows ISO is hardlinked into at least three of these — ~/Downloads,
	// .cache, and the VM bundle's Data/install.iso — because that is how it
	// gets used twice without costing 10 GB. Summing the entries naively
	// reports 15 GB of ISO that does not exist, and the total is the number
	// somebody compares against their free space.
	//
	// First entry to claim an inode owns it; the rest report it as shared. The
	// order above therefore matters, and the ISO is listed before the VM bundle
	// deliberately, so the story reads "the bundle re-uses the cached ISO"
	// rather than the reverse.
	seen := map[uint64]bool{}
	out := make([]External, 0, len(list))
	for _, e := range list {
		if e.Skip {
			continue
		}
		e.stat(seen)
		out = append(out, e)
	}
	return out
}

// Kind groups externals by what it means when one is missing.
type Kind string

const (
	// KindTool is installed software. Missing means nothing works at all.
	KindTool Kind = "tool"
	// KindMedia is downloaded, non-redistributable media.
	KindMedia Kind = "media"
	// KindState is a machine's state — expensive to rebuild, not source.
	KindState Kind = "state"
	// KindBuilt is generated from this repository in seconds.
	KindBuilt Kind = "built"
	// KindUpstream is a clone of a dependency, needed only to fix it.
	KindUpstream Kind = "upstream"
)

// External is one thing outside the repository that the project relies on.
type External struct {
	Name string
	Path string
	Why  string
	Fix  string
	Kind Kind
	Dir  bool
	Skip bool

	// Filled in by stat.
	Present bool
	Bytes   int64 // blocks this entry is the first to account for
	Shared  int64 // blocks already accounted for by an earlier entry
}

// stat measures the entry, counting each inode's blocks once across the whole
// inventory. seen carries that state between entries.
func (e *External) stat(seen map[uint64]bool) {
	fi, err := os.Stat(e.Path)
	if err != nil {
		return
	}
	e.Present = true
	if !fi.IsDir() {
		e.add(e.Path, seen)
		return
	}
	_ = filepath.WalkDir(e.Path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			// An unreadable subtree is not worth failing an inventory over —
			// UTM's container has directories this process cannot enter.
			return nil //nolint:nilerr
		}
		e.add(p, seen)
		return nil
	})
}

// add accounts for one file, using ALLOCATED blocks rather than apparent size.
// The VM's disk image is a 64 GB sparse file that has only ever touched ~28 GB;
// reporting the 64 would make the total a fiction.
func (e *External) add(p string, seen map[uint64]bool) {
	used, ok := diskUsage(p)
	if !ok {
		return
	}
	ino, nlink, haveInode := inodeInfo(p)
	if haveInode && nlink > 1 {
		if seen[ino] {
			e.Shared += used
			return
		}
		seen[ino] = true
	}
	e.Bytes += used
}

// Missing reports the externals that are not present, most important first.
func Missing(list []External) []External {
	var out []External
	for _, e := range list {
		if !e.Present {
			out = append(out, e)
		}
	}
	return out
}

// TotalBytes is what all the present externals occupy on disk.
func TotalBytes(list []External) int64 {
	var n int64
	for _, e := range list {
		n += e.Bytes
	}
	return n
}

// HumanBytes formats a byte count the way the rest of the CLI does.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	case n == 0:
		return "—"
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// lookPath resolves a tool on PATH, or returns a name that will read as
// missing. An empty Path would stat the working directory and report present,
// which is the wrong answer stated confidently.
func lookPath(name string) string {
	t := Tool{Name: name}
	if !t.resolve() {
		return filepath.Join("(not on PATH)", name)
	}
	p := t.Path
	// Resolve symlinks: Homebrew's bin is a link farm, and the interesting
	// answer is which install it actually points at.
	if real, rErr := filepath.EvalSymlinks(p); rErr == nil {
		return real
	}
	return p
}

// Home shortens a path for display, so the inventory is readable rather than
// three lines of container path per row.
func Home(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || runtime.GOOS == "windows" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// ---- from diskspace.go ----
// A Windows 11 install consumes roughly this much once it settles. The sparse
// disk starts near zero, so the free-space figure at creation time is
// misleading — the cost arrives later, during install, when failing is most
// expensive.
const WindowsInstallBytes = 30 << 30

// SpaceCheck reports whether a target directory can host an install.
type SpaceCheck struct {
	FreeBytes     int64
	RequiredBytes int64
	ISOBytes      int64 // 0 when the ISO is hardlinked and therefore free
	OK            bool
}

func (s SpaceCheck) String() string {
	return fmt.Sprintf("%s free, ~%s needed", HumanBytes(s.FreeBytes), HumanBytes(s.RequiredBytes))
}

// CheckSpace verifies there is room before a VM is created.
//
// Worth doing up front because the failure mode is so bad: the sparse disk and
// hardlinked ISO make a new bundle look almost free, then Windows Setup runs
// out of space mid-install and leaves a corrupt VM that has to be rebuilt from
// scratch — after a 20-minute wait.
//
// The ISO costs nothing when it can be hardlinked into the same volume, so it
// is only counted when a copy would be needed.
func CheckSpace(targetDir, isoPath string) (SpaceCheck, error) {
	var s SpaceCheck

	free, err := statfsAvailable(targetDir)
	if err != nil {
		return s, fmt.Errorf("checking free space on %s: %w", targetDir, err)
	}
	s.FreeBytes = free
	s.RequiredBytes = WindowsInstallBytes

	if isoPath != "" && !sameVolume(targetDir, isoPath) {
		if n, err := fileSize(isoPath); err == nil {
			s.ISOBytes = n
			s.RequiredBytes += n
		}
	}

	s.OK = s.FreeBytes >= s.RequiredBytes
	return s, nil
}

// sameVolume reports whether two paths live on one filesystem, in which case a
// hardlink works and the ISO is free.
//
// A false answer only costs an over-estimate of the space needed, which is the
// safe direction — so a platform that cannot tell says no.
func sameVolume(a, b string) bool { return sameDevice(a, b) }

func fileSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
