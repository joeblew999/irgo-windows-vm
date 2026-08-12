package utmvm

// Microsoft's Media Creation Tool catalog: where Windows install media actually
// comes from, without a browser and without being told no.
//
// There are two ways to get Windows media from Microsoft, and only one of them
// works from a script.
//
// The advertised one is the software-download page's API. It still answers for
// the product edition and the SKU list, and then refuses the step that returns
// the link:
//
//	{"Errors":[{"Key":"ErrorSettings.SentinelReject",
//	            "Value":"Sentinel marked this request as rejected."}]}
//
// That is deliberate anti-automation, it is not a bug to be worked around, and
// building on it would give this project a dependency that Microsoft actively
// maintains against us.
//
// The other is this: the catalog the Media Creation Tool itself reads. A 44 KB
// CAB at a stable fwlink, listing ~2000 ESD images with direct
// dl.delivery.mp.microsoft.com URLs, sizes, and SHA-1 hashes. No session, no
// cookies, no fingerprint, no rejection — and, unlike a browser download, it
// says what the bytes should hash to.
//
// It is also demonstrably the source of the ISO this project already uses:
// CrystalFetch caches this same file, and the ISO in .cache is built from the
// entry named 26100.4349...A64FRE_en-us.esd.

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/xml"
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

// CatalogURL is the fwlink the Windows 11 Media Creation Tool reads. It
// redirects to a versioned products.cab on download.microsoft.com.
//
// The number matters and is easy to get wrong: LinkId=841361 is the WINDOWS 10
// catalog. It downloads, extracts and parses perfectly, and then contains 19045
// builds and no Windows 11 at all — a failure that looks like success right up
// until an ISO installs the wrong operating system. 2156292 is Windows 11
// (catalog version 2.0, ~1978 images, 1976 of them ARM64).
const CatalogURL = "https://go.microsoft.com/fwlink/?linkid=2156292"

// CatalogURLWindows10 is kept only to name the trap above.
const CatalogURLWindows10 = "https://go.microsoft.com/fwlink/?LinkId=841361"

// CatalogEntry is one downloadable image.
type CatalogEntry struct {
	FileName     string `xml:"FileName"`
	LanguageCode string `xml:"LanguageCode"`
	Language     string `xml:"Language"`
	Edition      string `xml:"Edition"`
	Architecture string `xml:"Architecture"`
	Size         int64  `xml:"Size"`
	Sha1         string `xml:"Sha1"`
	FilePath     string `xml:"FilePath"`
}

// IsARM64 reports whether this entry is for Apple Silicon's guest architecture.
func (e CatalogEntry) IsARM64() bool { return strings.EqualFold(e.Architecture, "ARM64") }

// Build is the Windows build string the filename starts with, e.g.
// "26100.4349.250607-1500". It is the only version identifier in the catalog —
// there is no field for it — and it is what distinguishes two entries that are
// otherwise identical.
func (e CatalogEntry) Build() string {
	name := e.FileName
	// Three dot-separated groups, then an underscore: 26100.4349.250607-1500.ge_...
	parts := strings.SplitN(name, ".", 4)
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:3], ".")
}

// FetchCatalog downloads and parses the Media Creation Tool catalog.
func FetchCatalog(timeout time.Duration) ([]CatalogEntry, error) {
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, CatalogURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("utmvm: fetching catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("utmvm: catalog returned %s", resp.Status)
	}
	cab, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	xmlBytes, err := extractCatalogCAB(cab)
	if err != nil {
		return nil, err
	}
	return parseCatalog(xmlBytes)
}

// ParseCatalogXML exposes the parser for a products.xml already on disk, so a
// cached copy (CrystalFetch keeps one) can be used without a network call.
func ParseCatalogXML(b []byte) ([]CatalogEntry, error) { return parseCatalog(b) }

func parseCatalog(b []byte) ([]CatalogEntry, error) {
	var doc struct {
		Files []CatalogEntry `xml:"Catalogs>Catalog>PublishedMedia>Files>File"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("utmvm: parsing catalog xml: %w", err)
	}
	if len(doc.Files) == 0 {
		return nil, fmt.Errorf("utmvm: catalog parsed but listed no files")
	}
	// The catalog repeats each file once per edition it belongs to. Deduplicate
	// on the URL: 1978 entries collapse to a few hundred real images, and a
	// listing that shows the same build twenty times is unreadable.
	seen := map[string]bool{}
	out := make([]CatalogEntry, 0, len(doc.Files))
	for _, f := range doc.Files {
		if f.FilePath == "" || seen[f.FilePath] {
			continue
		}
		seen[f.FilePath] = true
		out = append(out, f)
	}
	return out, nil
}

// errNotMSZIP says which compression a cabinet used, so the caller can decide
// whether it has another way to read it.
type errNotMSZIP struct{ kind uint16 }

func (e errNotMSZIP) Error() string {
	return fmt.Sprintf("cabinet uses %s compression, not MSZIP", compressionName(e.kind))
}

// extractCatalogCAB reads the catalog out of its cabinet.
//
// Two attempts, in this order, and the order is the point:
//
//  1. **MSZIP, in Go.** Deflate, so the standard library covers it and nothing
//     needs installing. Microsoft has shipped MSZIP catalogs in the past and
//     may again.
//
//  2. **LZX, via libarchive.** What Microsoft ships today (0x1503: LZX with a
//     21-bit window). Go cannot read it — Microsoft's own decoder, in
//     go-winio/wim/lzx, is hardcoded to LZX's 32 KB WIM variant and refuses
//     anything larger — and writing one is several hundred lines of Huffman
//     and position-slot tables for a 44 KB file.
//
//     macOS ships libarchive as /usr/bin/bsdtar and it reads these cabinets
//     correctly, so this costs a developer nothing to install. That is the only
//     reason it is acceptable: an external tool that is already present on
//     every machine this runs on is a different proposition from one somebody
//     has to go and get.
func extractCatalogCAB(cab []byte) ([]byte, error) {
	xmlBytes, err := extractSingleFileCAB(cab)
	if err == nil {
		return xmlBytes, nil
	}
	var notMSZIP errNotMSZIP
	if !errors.As(err, &notMSZIP) {
		return nil, fmt.Errorf("utmvm: catalog cab: %w", err)
	}

	xmlBytes, exErr := extractCABWithLibarchive(cab)
	if exErr != nil {
		return nil, fmt.Errorf("utmvm: catalog cab: %w, and %w", err, exErr)
	}
	return xmlBytes, nil
}

// extractCABWithLibarchive shells to bsdtar for the LZX case.
//
// The temporary directory is ours and is removed: bsdtar extracts by name, and
// letting an archive choose where to write in a directory somebody else uses is
// how a download becomes a path-traversal bug.
func extractCABWithLibarchive(cab []byte) ([]byte, error) {
	tar := Tool{Name: "bsdtar", Formula: "libarchive",
		Why: "extracts Microsoft's LZX-compressed catalog cabinet"}
	if !tar.resolve() {
		return nil, fmt.Errorf("bsdtar not found; it ships with macOS at /usr/bin/bsdtar")
	}
	bsdtar := tar.Path
	dir, err := os.MkdirTemp("", "irgo-catalog-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	cabPath := filepath.Join(dir, "products.cab")
	if err := os.WriteFile(cabPath, cab, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(bsdtar, "-x", "-f", cabPath, "-C", dir) //nolint:gosec // paths are ours
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("bsdtar could not read the cabinet: %w", err)
	}

	// Whatever the single file inside is called. It has been products.xml for
	// years, but the cabinet names it and we should read what it says.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "products.cab" {
			continue
		}
		return os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // path is ours
	}
	return nil, fmt.Errorf("bsdtar extracted nothing from the cabinet")
}

// --- CAB (MSZIP) ------------------------------------------------------------
//
// Just enough of the format to read a cabinet holding one file, which is all
// this catalog has ever been. Written out rather than pulled in as a dependency
// for the same reason the rest of this repository is: the whole point is that a
// clone builds and cross-compiles with nothing installed.

func extractSingleFileCAB(cab []byte) ([]byte, error) {
	if len(cab) < 44 || string(cab[:4]) != "MSCF" {
		return nil, fmt.Errorf("not a cabinet (bad signature)")
	}
	le := binary.LittleEndian
	cFolders := le.Uint16(cab[26:28])
	cFiles := le.Uint16(cab[28:30])
	flags := le.Uint16(cab[30:32])
	if cFolders == 0 || cFiles == 0 {
		return nil, fmt.Errorf("cabinet is empty")
	}
	// Reserved-area fields shift every offset after the header, and the catalog
	// has never used them — but guessing wrong here yields garbage rather than
	// an error, so refuse instead.
	if flags&0x0004 != 0 {
		return nil, fmt.Errorf("cabinet uses reserved fields, which are not supported")
	}

	// First CFFOLDER sits immediately after the 36-byte header.
	const folderOff = 36
	if len(cab) < folderOff+8 {
		return nil, fmt.Errorf("truncated folder table")
	}
	coffCabStart := le.Uint32(cab[folderOff : folderOff+4])
	cCFData := le.Uint16(cab[folderOff+4 : folderOff+6])
	typeCompress := le.Uint16(cab[folderOff+6:folderOff+8]) & 0x000f
	if typeCompress != 1 {
		// Type 3 is LZX, which is what Microsoft ships today (0x1503: LZX with a
		// 21-bit window). It is not deflate and the standard library cannot read
		// it; a decoder is a few hundred lines of Huffman and delta-encoded
		// block trees, and is the one thing standing between this and a
		// zero-dependency fetch.
		//
		// Named rather than glossed, because "compression type 3" tells whoever
		// hits this nothing about what to do next.
		return nil, errNotMSZIP{typeCompress}
	}

	var out bytes.Buffer
	// MSZIP blocks share a history window: each block's deflate stream may refer
	// back up to 32 KB into the PREVIOUS block's output. Decompressing them
	// independently silently produces corrupt bytes in the middle of the file,
	// which for XML means a parse error a long way from the cause.
	var dict []byte
	off := int(coffCabStart)
	for i := 0; i < int(cCFData); i++ {
		if off+8 > len(cab) {
			return nil, fmt.Errorf("truncated data block %d", i)
		}
		cbData := int(le.Uint16(cab[off+4 : off+6]))
		cbUncomp := int(le.Uint16(cab[off+6 : off+8]))
		start := off + 8
		if start+cbData > len(cab) {
			return nil, fmt.Errorf("data block %d runs past end of cabinet", i)
		}
		blk := cab[start : start+cbData]
		if len(blk) < 2 || blk[0] != 'C' || blk[1] != 'K' {
			return nil, fmt.Errorf("data block %d is missing the CK signature", i)
		}
		fr := flate.NewReaderDict(bytes.NewReader(blk[2:]), dict)
		plain, err := io.ReadAll(fr)
		_ = fr.Close()
		if err != nil && len(plain) != cbUncomp {
			return nil, fmt.Errorf("inflating block %d: %w", i, err)
		}
		out.Write(plain)
		dict = tailBytes(plain, 1<<15)
		off = start + cbData
	}
	return out.Bytes(), nil
}

func compressionName(t uint16) string {
	switch t {
	case 0:
		return "uncompressed"
	case 1:
		return "MSZIP"
	case 2:
		return "Quantum"
	case 3:
		return "LZX"
	default:
		return "type " + strconv.Itoa(int(t))
	}
}

// CachedCatalogPaths are products.xml files another tool has already extracted.
//
// CrystalFetch — UTM's own authors' ISO downloader — keeps one, which is both
// the fallback while LZX is unimplemented and the evidence that this catalog is
// the right source: the ISO this project uses is the entry it lists.
func CachedCatalogPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Library", "Containers", "llc.turing.CrystalFetch",
			"Data", "Library", "Caches", "products.xml"),
	}
}

func tailBytes(b []byte, n int) []byte {
	if len(b) <= n {
		// Copy: flate keeps a reference to the dictionary, and the caller's
		// slice is reused on the next iteration.
		return append([]byte(nil), b...)
	}
	return append([]byte(nil), b[len(b)-n:]...)
}

// FilterCatalog narrows the catalog to what this project can use. Every
// argument is optional; an empty string matches everything.
func FilterCatalog(all []CatalogEntry, arch, lang, edition string) []CatalogEntry {
	var out []CatalogEntry
	for _, e := range all {
		if arch != "" && !strings.EqualFold(e.Architecture, arch) {
			continue
		}
		if lang != "" && !strings.EqualFold(e.LanguageCode, lang) {
			continue
		}
		if edition != "" && !strings.Contains(strings.ToLower(e.FileName), strings.ToLower(edition)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// HumanSize is HumanBytes for a catalog entry's declared size, which is a
// string in the XML in some catalog revisions.
func HumanSize(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return HumanBytes(n)
}
