package wire

import "strings"

// Headers.
const (
	// HeaderSHA256 is an object's SHA-256: claimed on PUT, the verified one
	// on GET and HEAD.
	HeaderSHA256 = "X-Golden-Sha256"
	// HeaderSize is the whole object's size, also on HEAD, where Workers does
	// not keep Content-Length.
	HeaderSize = "X-Golden-Size"
	// MetaSHA256 is the R2 custom metadata that holds an object's SHA-256.
	// The S3 path writes the same name (x-amz-meta-zsha256), so objects
	// pushed either way read the same.
	MetaSHA256 = "zsha256"
)

// The golden image's keys in its bucket, exactly as
// internal/utmvm/vm_golden_cache.go writes them and the Worker accepts them.
const (
	GoldenPrefix    = "golden/"
	GoldenLatestKey = GoldenPrefix + "latest"
)

// GoldenChunkKey is chunks/<sha256>.zst: one region, zstd, named by the
// SHA-256 of its uncompressed bytes.
func GoldenChunkKey(id string) string { return GoldenPrefix + "chunks/" + id + ".zst" }

// GoldenManifestKey is manifests/<sha256>.json, named by its own SHA-256.
func GoldenManifestKey(id string) string { return GoldenPrefix + "manifests/" + id + ".json" }

// GoldenListKinds are what golden-list lists, and the prefix of each. Only
// these two: latest is read directly, and nothing else in the bucket is
// reachable.
var GoldenListKinds = map[string]string{
	"manifests": GoldenPrefix + "manifests/",
	"chunks":    GoldenPrefix + "chunks/",
}

// IsGoldenKey is one of the golden image's keys: latest,
// manifests/<sha256>.json or chunks/<sha256>.zst. Anything else is refused.
//
// This and the validators below are plain loops, not regexps: compiling
// `[0-9a-f]{64}` at init overflows TinyGo's Wasm stack ("fatal error: stack
// overflow" before main, measured 1 Oct 2026 under wrangler dev), and host
// tests cannot see that.
func IsGoldenKey(k string) bool {
	if k == GoldenLatestKey {
		return true
	}
	if id, ok := strings.CutPrefix(k, GoldenPrefix+"manifests/"); ok {
		id, ok = strings.CutSuffix(id, ".json")
		return ok && IsHex(id, 64)
	}
	if id, ok := strings.CutPrefix(k, GoldenPrefix+"chunks/"); ok {
		id, ok = strings.CutSuffix(id, ".zst")
		return ok && IsHex(id, 64)
	}
	return false
}

// IsHex is s being exactly n lower-case hex digits.
func IsHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	return AllBytes(s, func(c byte) bool { return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' })
}

// AllBytes is every byte of s passing ok.
func AllBytes(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

// Glaze runs.
const (
	GlazeManifestPart = "manifest"   // the multipart part holding shots.json
	GlazeManifestFile = "shots.json" // its name among a run's stored files

	// Limits on a posted run. A run today is 7 pictures of 2–25 KB.
	MaxRunBytes     = 16 << 20
	MaxPictureBytes = 4 << 20
	MaxPictures     = 64

	// MaxGoldenPut is the largest golden object accepted. A chunk is a 64 MiB
	// region compressed with zstd, which adds a few KiB to data it cannot
	// compress, and Workers refuses a body over 100 MB on the Free and Pro
	// plans before the Worker sees it.
	MaxGoldenPut = 80 << 20
)

// GlazeTargets are the targets a run can be for.
var GlazeTargets = []string{"mac", "windows"}

// IsGlazeTarget is t being one of GlazeTargets.
func IsGlazeTarget(t string) bool {
	for _, x := range GlazeTargets {
		if x == t {
			return true
		}
	}
	return false
}

// IsRunID is a run's id: the first 8 bytes of its manifest's SHA-256, in hex.
func IsRunID(s string) bool { return IsHex(s, 16) }

// IsPicture is a safe picture name: [A-Za-z0-9_.-]{1,100} then .png, so it
// can be neither a path nor a hidden file.
func IsPicture(s string) bool {
	stem, ok := strings.CutSuffix(s, ".png")
	if !ok || stem == "" || len(stem) > 100 || stem[0] == '.' {
		return false
	}
	return AllBytes(stem, func(c byte) bool {
		return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-'
	})
}
