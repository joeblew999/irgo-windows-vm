package utmvm

// UTM's releases on GitHub: which one vm-create installs, and whether the
// installed UTM is behind.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UTMRelease is one published UTM release that has a macOS build.
type UTMRelease struct {
	Version    string    `json:"version"` // the tag without its "v"
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published"`
	Page       string    `json:"page"` // the release's web page
	DMG        string    `json:"dmg"`  // the signed .dmg vm-create downloads
}

// UTMReleases is the newest stable release and the newest pre-release, as
// GitHub listed them at Checked. Either can be nil: a pre-release older than
// the stable one is not reported, and a listing may hold no stable release.
type UTMReleases struct {
	Stable  *UTMRelease `json:"stable"`
	Pre     *UTMRelease `json:"prerelease"`
	Checked time.Time   `json:"checked"`
}

// utmReleasesAPI lists UTM's releases, newest first. A variable so tests can
// point it at a fake server; nothing else changes it.
//
// The list, not /releases/latest: that endpoint also skips pre-releases, but
// the choice would then be GitHub's rule rather than code here that a test can
// hold to it, and doctor needs the newest pre-release as well.
var utmReleasesAPI = "https://api.github.com/repos/utmapp/UTM/releases?per_page=100"

// utmReleaseCacheTTL is how long doctor trusts its last answer. GitHub allows
// 60 unauthenticated requests an hour; UTM releases a few times a year.
const utmReleaseCacheTTL = 12 * time.Hour

// utmReleaseCacheName is the cache file under Root.
const utmReleaseCacheName = "utm-releases.json"

// ghRelease is the part of GitHub's release object that is read.
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	HTMLURL    string    `json:"html_url"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// pickUTMReleases chooses the newest stable release and the newest
// pre-release newer than it, by version number rather than list position.
//
// A release counts only if it is not a draft, its tag parses as a version, and
// it carries UTM.dmg — the release also ships .ipa builds for iOS and visionOS
// and a .deb, which would download happily and be useless. Stable means
// GitHub's prerelease flag is false; UTM marks every beta with it (4.7.0–4.7.3,
// 5.0.0–5.0.6).
func pickUTMReleases(list []ghRelease) UTMReleases {
	var r UTMReleases
	for _, g := range list {
		if g.Draft {
			continue
		}
		v := strings.TrimPrefix(strings.TrimSpace(g.TagName), "v")
		if parseVersion(v) == nil {
			continue
		}
		dmg := ""
		for _, a := range g.Assets {
			if strings.EqualFold(a.Name, "UTM.dmg") {
				dmg = a.URL
			}
		}
		if dmg == "" {
			continue
		}
		rel := &UTMRelease{Version: v, Prerelease: g.Prerelease,
			Published: g.Published, Page: g.HTMLURL, DMG: dmg}
		slot := &r.Stable
		if g.Prerelease {
			slot = &r.Pre
		}
		if *slot == nil || compareVersions(v, (*slot).Version) > 0 {
			*slot = rel
		}
	}
	if r.Pre != nil && r.Stable != nil && compareVersions(r.Pre.Version, r.Stable.Version) <= 0 {
		r.Pre = nil
	}
	return r
}

// fetchUTMReleases asks GitHub for UTM's releases, giving up after timeout.
func fetchUTMReleases(ctx context.Context, timeout time.Duration) (UTMReleases, error) {
	list, err := fetchUTMReleaseList(ctx, timeout)
	if err != nil {
		return UTMReleases{}, err
	}
	r := pickUTMReleases(list)
	r.Checked = time.Now().UTC()
	return r, nil
}

// fetchUTMReleaseList is the raw release list from GitHub.
func fetchUTMReleaseList(ctx context.Context, timeout time.Duration) ([]ghRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, utmReleasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking GitHub for UTM's releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub returned %s for UTM's releases", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("reading UTM's releases: %w", err)
	}
	var list []ghRelease
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parsing UTM's releases: %w", err)
	}
	return list, nil
}

// latestStableUTMDMG is the .dmg of UTM's newest stable release: what
// vm-create installs when UTM is missing. Never a pre-release — betas change
// the config schema this package writes (see VerifiedVersion).
//
// Also never a new major version: only releases with VerifiedVersion's major
// are installed, so the day UTM 5.0 goes stable a fresh machine still gets
// 4.x until 5.x has passed the trial in .plans/2026-09-30_2000_utm-5.md and
// VerifiedVersion is moved.
func latestStableUTMDMG() (UTMRelease, error) {
	list, err := fetchUTMReleaseList(context.Background(), 30*time.Second)
	if err != nil {
		return UTMRelease{}, fmt.Errorf("utmvm: %w", err)
	}
	return installableUTM(list)
}

// installableUTM is the newest stable release sharing VerifiedVersion's major.
func installableUTM(list []ghRelease) (UTMRelease, error) {
	var same []ghRelease
	for _, g := range list {
		if sameMajor(strings.TrimPrefix(strings.TrimSpace(g.TagName), "v"), VerifiedVersion) {
			same = append(same, g)
		}
	}
	r := pickUTMReleases(same)
	if r.Stable == nil {
		return UTMRelease{}, fmt.Errorf("utmvm: GitHub lists no stable UTM %s.x release with a UTM.dmg", strings.SplitN(VerifiedVersion, ".", 2)[0])
	}
	return *r.Stable, nil
}

// UTMReleaseCheck is doctor's answer to "is UTM up to date": the releases
// (fresh, or cached when GitHub cannot be reached), or why there is no answer.
type UTMReleaseCheck struct {
	Releases UTMReleases
	Known    bool   // false: no answer, fresh or cached
	Stale    bool   // Known, but from a cache GitHub could not refresh
	Err      string // why GitHub was not asked or did not answer; "" if it did
}

// CheckUTMReleases reports UTM's latest releases for doctor, from a cache of
// at most utmReleaseCacheTTL, else from GitHub within timeout, else from an
// older cache marked stale. Being offline is an answer ("cannot tell"), not an
// error: doctor must work on a plane.
func CheckUTMReleases(timeout time.Duration) UTMReleaseCheck {
	cachePath := filepath.Join(Root(), utmReleaseCacheName)
	cached, cacheErr := readUTMReleaseCache(cachePath)
	if cacheErr == nil && time.Since(cached.Checked) < utmReleaseCacheTTL {
		return UTMReleaseCheck{Releases: cached, Known: true}
	}
	fresh, err := fetchUTMReleases(context.Background(), timeout)
	if err == nil {
		// Best effort: a cache that cannot be written costs the next doctor
		// one request, not a wrong answer.
		_ = writeUTMReleaseCache(cachePath, fresh)
		return UTMReleaseCheck{Releases: fresh, Known: true}
	}
	if cacheErr == nil {
		return UTMReleaseCheck{Releases: cached, Known: true, Stale: true, Err: err.Error()}
	}
	return UTMReleaseCheck{Err: err.Error()}
}

func readUTMReleaseCache(path string) (UTMReleases, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return UTMReleases{}, err
	}
	var r UTMReleases
	if err := json.Unmarshal(b, &r); err != nil {
		return UTMReleases{}, err
	}
	if r.Checked.IsZero() {
		return UTMReleases{}, errors.New("cache has no check time")
	}
	return r, nil
}

func writeUTMReleaseCache(path string, r UTMReleases) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UTMUpdate is whether a newer stable UTM than the installed one exists.
type UTMUpdate int

// The three answers. CannotTell covers an unreadable installed version, an
// unparseable one, and no release information.
const (
	UTMUpdateCannotTell UTMUpdate = iota
	UTMUpToDate
	UTMUpdateAvailable
)

// UTMUpdateFor compares an installed version with the latest stable release.
// Pre-releases never count as an update: vm-create does not install them and
// this package's schema is not verified against them.
func UTMUpdateFor(installed string, c UTMReleaseCheck) UTMUpdate {
	if !c.Known || c.Releases.Stable == nil || parseVersion(installed) == nil {
		return UTMUpdateCannotTell
	}
	if compareVersions(c.Releases.Stable.Version, installed) > 0 {
		return UTMUpdateAvailable
	}
	return UTMUpToDate
}

// parseVersion reads "4.7.5" as [4 7 5]. It returns nil for anything that is
// not dot-separated numbers, so "unknown" and "5.0.0-beta" do not compare.
func parseVersion(v string) []int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out[i] = n
	}
	return out
}

// compareVersions orders two versions parseVersion accepts; a missing
// component is 0, so "5.0" equals "5.0.0". Unparseable input sorts first.
func compareVersions(a, b string) int {
	x, y := parseVersion(a), parseVersion(b)
	switch {
	case x == nil && y == nil:
		return 0
	case x == nil:
		return -1
	case y == nil:
		return 1
	}
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			if p < q {
				return -1
			}
			return 1
		}
	}
	return 0
}
