package utmvm

import (
	"bytes"
	"compress/flate"
	"crypto/sha1" //nolint:gosec // the catalog publishes SHA-1; the choice is Microsoft's
	"encoding/binary"
	"encoding/hex"
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

// Getting Windows media: Microsoft's catalog, the download, and building an
// ISO from the ESD it serves.

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

// Downloading install media, with the two properties the GUI route does not
// give you: it says what the bytes should hash to, and it cannot destroy the
// media you already have.
//
// The second is not hypothetical. The working ISO is hardlinked into UTM's
// bundle, so the obvious implementation — open the destination, write to it —
// truncates the file the VM boots from. Everything here therefore writes to a
// staging path and only ever links or renames into place once the hash matches,
// and refuses outright if the destination is in use.

// Download fetches url to dest, resuming a partial file and verifying sha1.
//
// dest must not exist. The staging file is dest+".part", which is resumable
// across runs: a 4 GB download that dies at 90% costs the last 10%, not the
// whole thing, and the servers support ranged requests.
//
// progress, if non-nil, is called about once a second with bytes so far and the
// total. A 4 GB download with no output looks identical to a hung one.
func Download(url, dest, wantSHA1 string, progress func(done, total int64)) error {
	if err := refuseUnsafeDest(dest); err != nil {
		return err
	}
	part := dest + ".part"

	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}

	// No client timeout: this is measured in gigabytes, and a deadline that
	// makes sense for an API call fails a download at exactly the point the
	// work is nearly done. The transport's per-read timeouts still catch a
	// stalled connection.
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("utmvm: downloading: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// The server ignored the range, so anything already downloaded is not a
		// prefix of what is arriving now. Start again rather than concatenate.
		have = 0
	case http.StatusPartialContent:
	default:
		return fmt.Errorf("utmvm: downloading: server returned %s", resp.Status)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if have > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644) //nolint:gosec // path is the caller's chosen destination
	if err != nil {
		return err
	}

	total := have + resp.ContentLength
	done := have
	last := time.Now()
	buf := make([]byte, 1<<20)
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				f.Close()
				return wErr
			}
			done += int64(n)
			if progress != nil && time.Since(last) > time.Second {
				progress(done, total)
				last = time.Now()
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			f.Close()
			return fmt.Errorf("utmvm: downloading: %w", rErr)
		}
	}
	// Flushed before it counts as written. Without this a crash can leave the
	// final name at full length with an unflushed tail — the wrong bytes, under
	// a name that says the download finished.
	//
	// Note what is NOT here: a done == ContentLength check. net/http already
	// fails a body shorter than a declared Content-Length, so such a check is
	// unreachable — verified by disabling it and watching the test still pass.
	// The genuine gap is a chunked response (ContentLength -1) truncated
	// mid-stream, which is indistinguishable from a clean end at this layer and
	// is caught only by the SHA-1, when the caller supplies one.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if progress != nil {
		progress(done, total)
	}

	if wantSHA1 != "" {
		got, hErr := FileSHA1(part)
		if hErr != nil {
			return hErr
		}
		if !strings.EqualFold(got, wantSHA1) {
			// Kept, not deleted: 4 GB is expensive to re-fetch, and a mismatch
			// is more often a truncated resume than a corrupt server.
			return fmt.Errorf("utmvm: sha1 mismatch\n  want %s\n  got  %s\n  kept %s — delete it to start over",
				wantSHA1, got, part)
		}
	}
	return os.Rename(part, dest)
}

// FileSHA1 hashes a file, which for a 4 GB ESD takes a few seconds.
func FileSHA1(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // caller-supplied path
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New() //nolint:gosec // matching the catalog's published digest
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// refuseUnsafeDest stops a download from destroying media already in use.
//
// Three separate refusals, because each is a different mistake: writing over a
// file that exists, writing over a file some VM is booting from, and writing
// over one that was deliberately made immutable.
func refuseUnsafeDest(dest string) error {
	fi, err := os.Stat(dest)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return os.MkdirAll(filepath.Dir(dest), 0o755)
	}

	msg := fmt.Sprintf("utmvm: %s already exists (%s)", dest, HumanBytes(fi.Size()))

	if _, nlink, ok := inodeInfo(dest); ok && nlink > 1 {
		st, sErr := ISOLinks(dest, ISOSearchDirs())
		if sErr == nil {
			// Not len(Found)-1: dest may not be among Found at all, because the
			// search covers ~/Downloads and UTM's bundles, and dest is usually
			// .cache — which is neither. Count what is actually about to be
			// listed rather than assuming dest is in the list.
			var others []string
			for _, p := range st.Found {
				if abs, aErr := filepath.Abs(dest); aErr != nil || p != abs {
					others = append(others, p)
				}
			}
			if len(others) > 0 {
				msg += fmt.Sprintf("\n  and it is the SAME FILE as %d other name(s), including media a VM boots from:", len(others))
				for _, p := range others {
					msg += "\n    " + Home(p)
				}
				msg += "\n  Writing here would empty all of them."
			}
		}
	}
	return fmt.Errorf("%s\n  Move it aside, or choose another -o path. This will not overwrite it.", msg)
}

// ISOInfo is what we can learn about Windows install media without mounting it.
