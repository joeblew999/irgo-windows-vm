package utmvm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// releasesJSON is GitHub's list as it stood on 30 Sep 2026, trimmed: newest
// first, betas flagged, and a draft newer than all of them.
const releasesJSON = `[
 {"tag_name":"v5.1.0","draft":true,"prerelease":false,"assets":[{"name":"UTM.dmg","browser_download_url":"https://x/v5.1.0/UTM.dmg"}]},
 {"tag_name":"v5.0.6","prerelease":true,"published_at":"2026-09-24T05:35:31Z","html_url":"https://x/v5.0.6",
  "assets":[{"name":"UTM.ipa","browser_download_url":"https://x/v5.0.6/UTM.ipa"},{"name":"UTM.dmg","browser_download_url":"https://x/v5.0.6/UTM.dmg"}]},
 {"tag_name":"v5.0.5","prerelease":true,"assets":[{"name":"UTM.dmg","browser_download_url":"https://x/v5.0.5/UTM.dmg"}]},
 {"tag_name":"v4.7.10","prerelease":false,"published_at":"2026-01-03T17:51:54Z","html_url":"https://x/v4.7.10",
  "assets":[{"name":"UTM.ipa","browser_download_url":"https://x/v4.7.10/UTM.ipa"},{"name":"UTM.dmg","browser_download_url":"https://x/v4.7.10/UTM.dmg"},{"name":"UTM.deb","browser_download_url":"https://x/v4.7.10/UTM.deb"}]},
 {"tag_name":"v4.7.9","prerelease":false,"assets":[{"name":"UTM.dmg","browser_download_url":"https://x/v4.7.9/UTM.dmg"}]},
 {"tag_name":"v4.7.3","prerelease":true,"assets":[{"name":"UTM.dmg","browser_download_url":"https://x/v4.7.3/UTM.dmg"}]}
]`

// fakeGitHub serves body at utmReleasesAPI for the test and counts requests.
// HOME is a temp dir, so the cache starts empty and is the test's own.
func fakeGitHub(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := utmReleasesAPI
	utmReleasesAPI = srv.URL
	t.Cleanup(func() { utmReleasesAPI = old })
	return &hits
}

// TestLatestStableUTMDMGSkipsBetas pins what vm-create installs: the newest
// stable release's UTM.dmg — not the newer betas above it, not the draft, not
// an .ipa or .deb, and 4.7.10 over 4.7.9 by number, not string order.
//
// Negative controls, each run by hand and seen to fail: dropping the
// g.Prerelease slot choice (stable becomes 5.0.6); deleting the Draft skip
// (5.1.0); comparing tags as strings (4.7.9 beats 4.7.10); taking any asset
// instead of UTM.dmg (the .deb after it). UTM.dmg sits between other assets in
// the fixture: while it was last, "any asset" still ended on it and passed.
func TestLatestStableUTMDMGSkipsBetas(t *testing.T) {
	fakeGitHub(t, http.StatusOK, releasesJSON)
	rel, err := latestStableUTMDMG()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "4.7.10" || rel.DMG != "https://x/v4.7.10/UTM.dmg" || rel.Prerelease {
		t.Fatalf("installs %+v, want stable 4.7.10's UTM.dmg", rel)
	}
}

// TestLatestStableUTMDMGNoStable: a listing of betas only is an error, never
// a beta installed in its place.
//
// Negative control: falling back to r.Pre when r.Stable is nil installs 5.0.6.
func TestLatestStableUTMDMGNoStable(t *testing.T) {
	fakeGitHub(t, http.StatusOK, `[{"tag_name":"v5.0.6","prerelease":true,
	  "assets":[{"name":"UTM.dmg","browser_download_url":"https://x/UTM.dmg"}]}]`)
	if rel, err := latestStableUTMDMG(); err == nil {
		t.Fatalf("installs %+v from a listing with no stable release", rel)
	}
}

// TestPickUTMReleasesPrerelease: the newest pre-release is reported only when
// it is newer than the newest stable.
//
// Negative control: deleting the "<= stable" clearing reports 4.7.3 as the
// pre-release in the second case.
func TestPickUTMReleasesPrerelease(t *testing.T) {
	dmg := []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{{"UTM.dmg", "u"}}
	r := pickUTMReleases([]ghRelease{
		{TagName: "v5.0.5", Prerelease: true, Assets: dmg},
		{TagName: "v5.0.6", Prerelease: true, Assets: dmg},
		{TagName: "v4.7.5", Assets: dmg},
	})
	if r.Pre == nil || r.Pre.Version != "5.0.6" || r.Stable == nil || r.Stable.Version != "4.7.5" {
		t.Fatalf("got stable %+v pre %+v", r.Stable, r.Pre)
	}
	r = pickUTMReleases([]ghRelease{
		{TagName: "v4.7.5", Assets: dmg},
		{TagName: "v4.7.3", Prerelease: true, Assets: dmg},
	})
	if r.Pre != nil {
		t.Fatalf("reported pre-release %s older than stable 4.7.5", r.Pre.Version)
	}
}

// TestCheckUTMReleasesOffline: no network and no cache is "cannot tell" with
// the reason, not an error and not "up to date"; and it gives up within the
// timeout instead of hanging doctor.
//
// Negative controls: making UTMUpdateFor return UTMUpToDate when !Known fails
// the verdict check; dropping the context timeout in fetchUTMReleases fails
// the elapsed-time check (the server below answers only after 5 s).
func TestCheckUTMReleasesOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	old := utmReleasesAPI
	utmReleasesAPI = srv.URL
	t.Cleanup(func() { utmReleasesAPI = old })

	start := time.Now()
	c := CheckUTMReleases(200 * time.Millisecond)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("took %s with a 200ms timeout", took)
	}
	if c.Known || c.Err == "" {
		t.Fatalf("offline check = %+v, want unknown with a reason", c)
	}
	if got := UTMUpdateFor("4.7.5", c); got != UTMUpdateCannotTell {
		t.Fatalf("offline verdict %v, want cannot tell", got)
	}
}

// TestCheckUTMReleasesCache: a fresh answer is cached and reused without
// asking GitHub again; when GitHub then fails, an expired cache is still used
// and marked stale.
//
// Negative controls: skipping writeUTMReleaseCache makes the second call hit
// the server (hits == 2); skipping the TTL check likewise; dropping the
// stale-cache fallback makes the last check unknown.
func TestCheckUTMReleasesCache(t *testing.T) {
	hits := fakeGitHub(t, http.StatusOK, releasesJSON)
	for i := 0; i < 2; i++ {
		c := CheckUTMReleases(time.Second)
		if !c.Known || c.Stale || c.Releases.Stable == nil || c.Releases.Stable.Version != "4.7.10" {
			t.Fatalf("call %d: %+v", i, c)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("GitHub asked %d times, want 1 (second answer from cache)", n)
	}

	// Age the cache past its TTL and take GitHub away.
	cachePath := filepath.Join(Root(), utmReleaseCacheName)
	r, err := readUTMReleaseCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	r.Checked = time.Now().Add(-2 * utmReleaseCacheTTL)
	if err := writeUTMReleaseCache(cachePath, r); err != nil {
		t.Fatal(err)
	}
	old := utmReleasesAPI
	utmReleasesAPI = "http://127.0.0.1:1/unreachable"
	t.Cleanup(func() { utmReleasesAPI = old })
	c := CheckUTMReleases(time.Second)
	if !c.Known || !c.Stale || c.Err == "" {
		t.Fatalf("expired cache with GitHub down = %+v, want known, stale, with reason", c)
	}
	if _, err := os.Stat(cachePath + ".part"); err == nil {
		t.Fatal("left a .part file behind")
	}
}

// TestCheckUTMReleasesHTTPError: an error status is "cannot tell", and
// nothing is cached from it, even when the body happens to parse.
//
// Negative control: deleting the StatusOK check parses the body as an empty
// list and reports Known with no stable release. With GitHub's real
// rate-limit body (a JSON object) the control survived, because the parse
// failed anyway; hence a body that parses.
func TestCheckUTMReleasesHTTPError(t *testing.T) {
	fakeGitHub(t, http.StatusServiceUnavailable, `[]`)
	c := CheckUTMReleases(time.Second)
	if c.Known {
		t.Fatalf("rate-limited check = %+v, want unknown", c)
	}
	if _, err := os.Stat(filepath.Join(Root(), utmReleaseCacheName)); err == nil {
		t.Fatal("cached a failed answer")
	}
}

// TestUTMUpdateFor covers the three answers.
//
// Negative control: comparing with > on strings says "4.7.10" is older than
// "4.7.9" and fails the 4.7.9 row.
func TestUTMUpdateFor(t *testing.T) {
	known := func(stable string) UTMReleaseCheck {
		return UTMReleaseCheck{Known: true, Releases: UTMReleases{Stable: &UTMRelease{Version: stable}}}
	}
	for _, tc := range []struct {
		installed string
		c         UTMReleaseCheck
		want      UTMUpdate
	}{
		{"4.7.5", known("4.7.5"), UTMUpToDate},
		{"4.7.9", known("4.7.10"), UTMUpdateAvailable},
		{"5.0.6", known("4.7.5"), UTMUpToDate}, // a beta ahead of stable is not behind
		{"unknown", known("4.7.5"), UTMUpdateCannotTell},
		{"4.7.5", UTMReleaseCheck{}, UTMUpdateCannotTell},
		{"4.7.5", UTMReleaseCheck{Known: true}, UTMUpdateCannotTell},
	} {
		if got := UTMUpdateFor(tc.installed, tc.c); got != tc.want {
			t.Errorf("UTMUpdateFor(%q, %+v) = %v, want %v", tc.installed, tc.c.Releases.Stable, got, tc.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"4.7.10", "4.7.9", 1},
		{"5.0", "5.0.0", 0},
		{"4.7.5", "5.0.0", -1},
		{"unknown", "4.7.5", -1},
		{"5.0.0-beta", "4.7.5", -1},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
