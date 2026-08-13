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
	"unicode/utf16"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

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
type ISOInfo struct {
	// IsARM64 reports whether the ARM64 UEFI bootloader is present. An x86-64
	// ISO on Apple Silicon boots to a black screen with no diagnostic at all,
	// so this is the single most valuable check here.
	IsARM64 bool

	// HasNoPromptLoader reports whether cdboot_noprompt.efi is present. The
	// default loader (efi/boot/bootaa64.efi) prints "Press any key to boot from
	// CD or DVD" and gives up after about five seconds, which stops an
	// "unattended" install before it starts. The _noprompt variant is what
	// makes hands-off installation possible.
	HasNoPromptLoader bool

	// SizeBytes is the image size, useful for a sanity check: a Windows 11
	// image is several GB, and a few hundred MB means a truncated download.
	SizeBytes int64
}

// InspectISO reports what matters about Windows install media.
//
// Implementation note, because the obvious approach does not work: Windows 11
// ISOs are UDF, not ISO9660 — they have to be, since install.wim exceeds
// ISO9660's 4 GB file limit. Filesystem libraries that read the ISO9660
// descriptor find only a stub and fail to traverse it.
//
// Rather than embed a UDF reader for two yes/no questions, this streams the
// image looking for the filenames in the directory structures. UDF stores them
// as either 8-bit or UTF-16BE strings, so both encodings are searched. That is
// a heuristic — it proves a name is present, not that it is at a particular
// path — but for "is there an ARM64 bootloader in here" it is decisive, and it
// is honest about being a scan rather than pretending to be a parse.
func InspectISO(path string) (ISOInfo, error) {
	var info ISOInfo

	f, err := os.Open(path)
	if err != nil {
		return info, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return info, err
	}
	info.SizeBytes = st.Size()

	needles := [][]byte{
		encode8("BOOTAA64.EFI"), encode16be("bootaa64.efi"),
		encode8("CDBOOT_NOPROMPT.EFI"), encode16be("cdboot_noprompt.efi"),
	}
	found := make([]bool, len(needles))

	// A modest buffer with an overlap, so a name spanning a chunk boundary is
	// still matched. The overlap must exceed the longest needle.
	const chunk = 1 << 20
	const overlap = 128
	buf := make([]byte, chunk+overlap)
	var carry int

	for {
		n, err := io.ReadFull(f, buf[carry:chunk+overlap])
		if n > 0 {
			window := buf[:carry+n]
			up := bytes.ToUpper(window)
			for i, needle := range needles {
				if found[i] {
					continue
				}
				if bytes.Contains(up, bytes.ToUpper(needle)) {
					found[i] = true
				}
			}
			if allTrue(found) {
				break
			}
			// Keep the tail so the next pass can match across the seam.
			if len(window) > overlap {
				copy(buf, window[len(window)-overlap:])
				carry = overlap
			} else {
				carry = len(window)
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return info, fmt.Errorf("reading %s: %w", path, err)
		}
	}

	info.IsARM64 = found[0] || found[1]
	info.HasNoPromptLoader = found[2] || found[3]
	return info, nil
}

func allTrue(b []bool) bool {
	for _, v := range b {
		if !v {
			return false
		}
	}
	return true
}

// encode8 is the plain 8-bit form UDF uses for names that fit in Latin-1.
func encode8(s string) []byte { return []byte(strings.ToUpper(s)) }

// encode16be is UDF's UTF-16BE form. Note big-endian: UDF differs from the
// little-endian UTF-16 used elsewhere in Windows, and searching for the wrong
// byte order silently finds nothing.
func encode16be(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range utf16.Encode([]rune(s)) {
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}

// Building bootable Windows ARM64 media, as a command rather than a recipe.
//
// The steps are not hard. Getting them wrong is silent, which is why they are
// here in Go with the reasons attached, instead of in a shell script somebody
// has to read before they can trust it:
//
//   - the ISO must be UDF, or ISO9660 level 3, because install.wim is 4.099 GiB
//     and ISO9660's file-size limit is 4 GiB;
//   - the El Torito entry must be marked EFI (platform 0xEF), not BIOS;
//   - the boot image should be efisys_noprompt.bin, not efisys.bin, because the
//     difference is whether the disc stops at "Press any key to boot from CD";
//   - and `hdiutil makehybrid` cannot do it at all — measured, twice, see
//     RESULTS.md. An external masterer is required.
//
// Only the masterer is external. Everything else — mounting, copying, checking,
// refusing to overwrite media in use — is done here, so the part that can
// damage something is the part we control.

// Tool is an external program this package needs but cannot replace.
type Tool struct {
	Name    string // executable name
	Formula string // Homebrew formula that provides it
	Why     string
	Path    string // resolved, empty when missing
}

// Found reports whether the tool is installed.
func (t Tool) Found() bool { return t.Path != "" }

// resolve looks the executable up on PATH, recording where it was found. The
// one place in this package that asks that question about an external tool.
func (t *Tool) resolve() bool {
	p, err := exec.LookPath(t.Name)
	if err != nil {
		t.Path = ""
		return false
	}
	t.Path = p
	return true
}

// Install is the command that provides it.
func (t Tool) Install() string { return "brew install " + t.Formula }

// isoMasterers are the programs that can write a bootable Windows ISO, in
// order of preference.
//
// xorriso is first because it is the smaller, more actively maintained
// dependency and its ISO9660 level 3 multi-extent support covers the 4 GiB
// problem. cdrtools is the fallback for the case where Windows Setup turns out
// to need genuine UDF — Microsoft ships UDF, and their own CDFS driver handles
// multi-extent poorly, so this is a real possibility rather than a hedge.
//
// `hdiutil` is deliberately absent. It is built into macOS and would need no
// installation, and it does not work: two images mastered with it enumerate
// correctly in UTM's firmware and then refuse to boot. CrystalFetch reached the
// same conclusion — it bundles mkisofs and has its hdiutil line commented out.
func isoMasterers() []Tool {
	return []Tool{
		{
			Name:    "xorriso",
			Formula: "xorriso",
			Why:     "writes the bootable ISO. ISO9660 level 3 carries the 4.099 GiB install.wim as a multi-extent file.",
		},
		{
			Name:    "mkisofs",
			Formula: "cdrtools",
			Why:     "writes the bootable ISO, with real UDF — needed if Windows Setup rejects multi-extent ISO9660.",
		},
	}
}

// WimTool is the one thing with no Go alternative: reading LZMS-compressed ESD
// archives. No Go implementation exists that is worth trusting.
func WimTool() Tool {
	t := Tool{
		Name:    "wimlib-imagex",
		Formula: "wimlib",
		Why:     "reads Microsoft's .esd archives (LZMS compression, which no Go library implements).",
	}
	t.resolve()
	return t
}

// FindMasterer returns the first available ISO masterer, or the list of
// candidates when none is installed.
func FindMasterer() (Tool, []Tool) {
	all := isoMasterers()
	for i := range all {
		if all[i].resolve() {
			return all[i], all
		}
	}
	return Tool{}, all
}

// ExpandESD lays a Microsoft .esd archive out as a directory of installation
// media, ready for BuildISO.
//
// The sequence is not guessable and each step has a reason. It follows the one
// shipped, working reference — CrystalFetch's `esd2iso.sh`, itself Paul
// Rockwell's `w11arm_esd2iso` — because the alternative is rediscovering its
// scars:
//
//	image 1      the media layout: boot files, setup.exe, efi/
//	image 2      Windows PE                  -> sources/boot.wim
//	image 3      Windows Setup, MARKED BOOT  -> sources/boot.wim
//	images 4..N  the Windows editions        -> sources/install.wim
//
// Image 3 must be exported with --boot. Without it Setup fails, and it fails
// late, after the disc has booted and looked fine.
//
// boot.wim is LZX because Windows PE's loader reads it before anything that
// understands LZMS exists; install.wim is LZMS because it is the 4 GB one and
// compression is what keeps it near the disc size at all.
func ExpandESD(esd, dir string, progress func(step string)) error {
	wim := WimTool()
	if eErr := wim.Ensure(); eErr != nil {
		return eErr
	}
	if _, err := os.Stat(esd); err != nil {
		return fmt.Errorf("utmvm: %s: %w", esd, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	say := func(s string) {
		if progress != nil {
			progress(s)
		}
	}

	n, err := esdImageCount(wim.Path, esd)
	if err != nil {
		return err
	}
	if n < 4 {
		return fmt.Errorf("utmvm: %s holds %d images; Windows media has at least 4", esd, n)
	}

	run := func(what string, args ...string) error {
		say(what)
		cmd := exec.Command(wim.Path, args...) //nolint:gosec // args are built here
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("utmvm: %s: %w", what, err)
		}
		return nil
	}

	if err := run("laying out the media (image 1)", "apply", esd, "1", dir); err != nil {
		return err
	}

	bootWim := filepath.Join(dir, "sources", "boot.wim")
	if err := os.MkdirAll(filepath.Dir(bootWim), 0o755); err != nil {
		return err
	}
	if err := run("exporting Windows PE (image 2)",
		"export", esd, "2", bootWim, "--compress=LZX", "--chunk-size", "32K"); err != nil {
		return err
	}
	// --boot is the one that bites: Setup fails without it, long after boot.
	if err := run("exporting Windows Setup (image 3, marked bootable)",
		"export", esd, "3", bootWim, "--compress=LZX", "--chunk-size", "32K", "--boot"); err != nil {
		return err
	}

	installWim := filepath.Join(dir, "sources", "install.wim")
	for i := 4; i <= n; i++ {
		if err := run(fmt.Sprintf("exporting edition (image %d of %d)", i, n),
			"export", esd, fmt.Sprint(i), installWim, "--compress=LZMS", "--chunk-size", "128K"); err != nil {
			return err
		}
	}
	return nil
}

// esdImageCount reads how many images an ESD holds. There is no fixed number:
// it varies by release and by which editions Microsoft bundled.
func esdImageCount(wimPath, esd string) (int, error) {
	out, err := exec.Command(wimPath, "info", esd).Output() //nolint:gosec // wimPath resolved by LookPath
	if err != nil {
		return 0, fmt.Errorf("utmvm: reading %s: %w", esd, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "Image Count:") {
			continue
		}
		var n int
		if _, sErr := fmt.Sscanf(strings.TrimSpace(line), "Image Count: %d", &n); sErr == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("utmvm: %s: could not find an image count in wimlib's output", esd)
}

// RemasterOptions configures BuildISO.
type RemasterOptions struct {
	// Source is a directory holding the complete media layout.
	Source string

	// Output is the ISO to write. It must not exist, must not be immutable,
	// must not be hardlinked, and must not be inside a VM bundle.
	Output string

	// Label is the volume identifier. Windows does not require a particular
	// one, but keeping the source's makes a rebuilt image comparable to the
	// original.
	Label string

	// NoPrompt uses efisys_noprompt.bin when the media provides it, so the disc
	// boots without "Press any key to boot from CD or DVD".
	//
	// This is the reason building our own media is worth doing at all: the
	// alternative, which this repository does today, is typing a path at the
	// UEFI shell and firing eight keypresses over six seconds — a hack the
	// README documents as costing hours and as having once destroyed an install
	// when surplus presses reached Setup's UI.
	NoPrompt bool
}

// BootImage returns the El Torito image to use, and whether it is the
// no-prompt variant.
func (o RemasterOptions) BootImage() (rel string, noPrompt bool) {
	const dir = "efi/microsoft/boot"
	if o.NoPrompt {
		if _, err := os.Stat(filepath.Join(o.Source, dir, "efisys_noprompt.bin")); err == nil {
			return dir + "/efisys_noprompt.bin", true
		}
	}
	return dir + "/efisys.bin", false
}

// BuildISO writes a bootable Windows ARM64 ISO from a directory of media.
func BuildISO(opts RemasterOptions, paths Paths) error {
	if fi, err := os.Stat(opts.Source); err != nil || !fi.IsDir() {
		return fmt.Errorf("utmvm: source %s is not a directory", opts.Source)
	}
	if err := paths.CheckWritable(opts.Output); err != nil {
		return err
	}
	if _, err := os.Stat(opts.Output); err == nil {
		return fmt.Errorf("utmvm: %s already exists — this never overwrites; move it aside", opts.Output)
	}

	tool, candidates := FindMasterer()
	if !tool.Found() {
		tool = candidates[0]
		if eErr := tool.Ensure(); eErr != nil {
			return eErr
		}
	}

	boot, noPrompt := opts.BootImage()
	if _, err := os.Stat(filepath.Join(opts.Source, boot)); err != nil {
		return fmt.Errorf("utmvm: %s has no El Torito boot image at %s — is it Windows media?", opts.Source, boot)
	}

	label := opts.Label
	if label == "" {
		label = "WINDOWS_ARM64"
	}

	var args []string
	switch tool.Name {
	case "xorriso":
		// -e, not -b: it marks the El Torito entry as EFI (platform 0xEF),
		// which is what a UEFI-only ARM64 disc boots from. -b describes a BIOS
		// entry and the firmware ignores it.
		//
		// -iso-level 3 is what permits install.wim past 4 GiB, as a multi-extent
		// file. No -udf: xorriso does not write UDF at all (that is cdrtools).
		args = []string{
			"-as", "mkisofs",
			"-iso-level", "3",
			"-V", label,
			"-e", boot, "-no-emul-boot",
			"-o", opts.Output, opts.Source,
		}
	case "mkisofs":
		// cdrtools does write UDF, which is what Microsoft's own media uses.
		//
		// -e, not -b, for the same reason as above: -b marks a BIOS entry, which
		// a UEFI-only ARM64 machine ignores. This branch said -b and so produced
		// a correctly sized, correctly named, non-bootable ISO.
		args = []string{
			"-udf", "-iso-level", "3",
			"-V", label,
			"-e", boot, "-no-emul-boot",
			"-o", opts.Output, opts.Source,
		}
	default:
		return fmt.Errorf("utmvm: no argument recipe for ISO masterer %q", tool.Name)
	}

	cmd := exec.Command(tool.Path, args...) //nolint:gosec // arguments are built above, not user-supplied strings
	cmd.Stdout = os.Stderr                  // progress belongs on stderr; stdout is for results
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// A partial ISO is worse than none: it is the right size to look
		// plausible and will fail as an unbootable VM later.
		_ = os.Remove(opts.Output)
		return fmt.Errorf("utmvm: %s failed: %w", tool.Name, err)
	}

	fi, err := os.Stat(opts.Output)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s (%s) with %s\n", opts.Output, HumanBytes(fi.Size()), tool.Name)
	if noPrompt {
		fmt.Printf("  boot image: %s — no \"Press any key to boot from CD\"\n", boot)
	} else {
		fmt.Printf("  boot image: %s — this disc WILL stop at \"Press any key to boot from CD\"\n", boot)
	}
	return nil
}

// BuildISOImage writes an ISO9660 image containing srcDir.
//
// The answer file must be on an ISO9660 CD, not a FAT disk. This is not a
// preference — it was established by regression. An earlier version shipped the
// payload as a FAT removable disk, on the reasoning that Windows Setup scans
// removable drives and the UEFI shell can read FAT from the same image. Setup
// did not pick up autounattend.xml from it, and the install fell back to
// interactive with no error explaining why. Attached as a CD it works: Setup
// applied the DiskConfiguration and partitioned the disk exactly as specified.
//
// Joliet is enabled so long filenames survive; plain ISO9660 would truncate
// autounattend.xml to 8.3 and Setup would never find it.
func BuildISOImage(imagePath, srcDir string, sizeMiB int) error {
	if sizeMiB < 16 {
		sizeMiB = 16
	}
	_ = os.Remove(imagePath)

	// ISO9660 requires a 2048-byte logical block; the 512-byte default is
	// rejected outright.
	d, err := diskfs.Create(imagePath, int64(sizeMiB)<<20, diskfs.SectorSize(2048))
	if err != nil {
		return fmt.Errorf("create image: %w", err)
	}
	defer d.Close()

	fs, err := d.CreateFilesystem(disk.FilesystemSpec{
		Partition:   0,
		FSType:      filesystem.TypeISO9660,
		VolumeLabel: "UNATTEND",
	})
	if err != nil {
		return fmt.Errorf("create ISO9660: %w", err)
	}

	if err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := "/" + filepath.ToSlash(rel)
		if info.IsDir() {
			return fs.Mkdir(target)
		}
		return copyIntoISO(fs, path, target)
	}); err != nil {
		return err
	}

	iso, ok := fs.(*iso9660.FileSystem)
	if !ok {
		return fmt.Errorf("unexpected filesystem type %T", fs)
	}
	// Without Finalize the descriptors are never written and the image is not
	// a readable ISO at all.
	if err := iso.Finalize(iso9660.FinalizeOptions{
		VolumeIdentifier: "UNATTEND",
		RockRidge:        true,
		// Joliet is what Windows reads. Without it the image is 8.3 only and
		// autounattend.xml becomes AUTOUNAT.XML — a name Setup never looks for,
		// so the install silently runs interactive.
		Joliet:          true,
		DeepDirectories: true,
	}); err != nil {
		return err
	}
	return trimToVolumeSize(imagePath)
}

// trimToVolumeSize cuts the image down to the size its Primary Volume
// Descriptor declares.
//
// go-diskfs needs a size up front and leaves whatever is left over as a tail of
// zeros past the end of the volume. Readers disagree about that: macOS mounts
// such an image happily, which is why it looked fine, while Windows Setup did
// not pick up autounattend.xml from one and fell back to an interactive install
// with no error. The working reference image, built by hdiutil, has no tail.
//
// Volume space size lives at offset 0x8050 as a little-endian block count, with
// 2048-byte logical blocks.
func trimToVolumeSize(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	var b [4]byte
	if _, err := f.ReadAt(b[:], 0x8050); err != nil {
		return fmt.Errorf("reading volume size: %w", err)
	}
	blocks := int64(binary.LittleEndian.Uint32(b[:]))
	if blocks == 0 {
		return fmt.Errorf("ISO reports zero volume blocks; image is malformed")
	}
	want := blocks * 2048

	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() <= want {
		return nil
	}
	return f.Truncate(want)
}

func copyIntoISO(fs filesystem.FileSystem, hostPath, target string) error {
	src, err := os.Open(hostPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := fs.OpenFile(target, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("create %s in image: %w", strings.TrimPrefix(target, "/"), err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

// Protecting the working ISO from the tooling that will replace it.
//
// The Windows ISO is hardlinked, deliberately: ~/Downloads has the download,
// .cache/win11-arm64.iso is what this repo refers to, and the VM bundle's
// Data/install.iso is what UTM boots. One set of blocks, three names, 5 GB
// once instead of 15.
//
// That is also the trap. A hardlink is not a copy — every name is the same
// inode — so anything that opens ANY of those paths for writing destroys the
// other two. `fetch-iso` is the obvious candidate: the natural implementation
// downloads to .cache/win11-arm64.iso, and the natural way to do that is
// O_CREATE|O_TRUNC, which empties the file UTM is booting from. The failure is
// silent until the next install, and the ISO is 5 GB of gated download to get
// back.
//
// So the file is made immutable instead of trusted to nobody's carelessness.
// macOS's uchg flag is per-inode, which is exactly the granularity wanted: set
// it through one path and all three names are protected, including the one
// inside UTM's container that this repo never mentions.

// ISOStatus is what is known about one ISO and every other name for it.
type ISOStatus struct {
	Path      string
	Bytes     int64
	Links     int      // how many names this inode has, per the filesystem
	Found     []string // the ones that could be located
	Protected bool
}

// ISOLinks reports an ISO's size, protection, and every other path that shares
// its blocks.
//
// searchIn bounds the hunt for sibling names. There is no reverse index from
// inode to paths on macOS, so they have to be looked for; Links is the count
// the filesystem reports and is authoritative, while Found is only what was
// looked for and located. Links > len(Found) means a name exists somewhere not
// searched — a Time Machine local snapshot, usually — and is not cause for
// alarm.
func ISOLinks(path string, searchIn []string) (ISOStatus, error) {
	st := ISOStatus{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return st, err
	}
	st.Bytes = fi.Size()

	ino, nlink, ok := inodeInfo(path)
	if !ok {
		return st, fmt.Errorf("utmvm: cannot stat %s", path)
	}
	st.Links = int(nlink)

	if flags, ok := fileFlags(path); ok {
		st.Protected = flags&uchgFlag != 0
	}

	for _, dir := range searchIn {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable subtree just yields no matches
			}
			if got, _, ok := inodeInfo(p); ok && got == ino {
				st.Found = append(st.Found, p)
			}
			return nil
		})
	}
	return st, nil
}

// ProtectISO makes the ISO immutable: it cannot be written, truncated, renamed
// or deleted until UnprotectISO clears the flag. Idempotent.
func ProtectISO(path string) error { return chflags(path, true) }

// UnprotectISO clears the immutable flag, so the ISO can be replaced or the VM
// holding a hardlink to it can be deleted. Idempotent.
//
// `irgo-winvm vm-delete` needs this: rm refuses an immutable file, and the VM
// bundle's install.iso is the same inode as the protected one.
func UnprotectISO(path string) error { return chflags(path, false) }

func chflags(path string, set bool) error {
	if !immutableSupported {
		return setFileFlags(path, 0) // reports the platform honestly
	}
	flags, ok := fileFlags(path)
	if !ok {
		return fmt.Errorf("utmvm: stat %s", path)
	}
	if set {
		flags |= uchgFlag
	} else {
		flags &^= uchgFlag
	}
	if err := setFileFlags(path, flags); err != nil {
		return fmt.Errorf("utmvm: setting flags on %s: %w", path, err)
	}
	return nil
}

// ISOSearchDirs are the places worth looking for other names for an ISO: where
// a browser puts a download, and where UTM keeps the bundles that use it.
func ISOSearchDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dirs := []string{filepath.Join(home, "Downloads")}
	if vmDir, err := DefaultVMDir(); err == nil {
		dirs = append(dirs, vmDir)
	}
	return dirs
}

// FoundISO is one installation image found on this machine.
type FoundISO struct {
	Path  string
	Bytes int64
	Inode uint64
	Links int
	InUse bool // shares its blocks with a VM bundle or the repo's cache
}

// ScanISOs finds every large ISO in the usual places and says which are
// actually used.
//
// It exists because these are 5 GB each, they are produced by a GUI tool that
// names them after a Windows build rather than anything meaningful, and a
// second one is invisible until the disk fills. Asking "which of these is the
// one that works?" from filenames alone is not answerable — but "does it share
// blocks with a VM bundle" is, and that is what InUse reports.
//
// minBytes filters out the small ISOs that are not Windows media: the answer
// file this repo generates is 32 MB and the guest tools are 121 MB.
func ScanISOs(extraDirs []string, minBytes int64) []FoundISO {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dirs := []string{
		filepath.Join(home, "Downloads"),
		filepath.Join(home, "Documents"),
		filepath.Join(home, "Desktop"),
	}
	dirs = append(dirs, extraDirs...)

	// Inodes reachable from a VM bundle, which is what "in use" means. Anything
	// UTM boots is here, whatever it is called elsewhere.
	used := map[uint64]bool{}
	if vmDir, dErr := DefaultVMDir(); dErr == nil {
		_ = filepath.WalkDir(vmDir, func(p string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() {
				return nil //nolint:nilerr
			}
			if ino, _, ok := inodeInfo(p); ok {
				used[ino] = true
			}
			return nil
		})
	}

	seen := map[uint64]bool{}
	var out []FoundISO
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() {
				return nil //nolint:nilerr
			}
			if !strings.EqualFold(filepath.Ext(p), ".iso") {
				return nil
			}
			info, iErr := d.Info()
			if iErr != nil || info.Size() < minBytes {
				return nil
			}
			ino, nlink, ok := inodeInfo(p)
			if !ok || seen[ino] {
				return nil
			}
			seen[ino] = true
			out = append(out, FoundISO{
				Path:  p,
				Bytes: info.Size(),
				Inode: ino,
				Links: int(nlink),
				InUse: used[ino],
			})
			return nil
		})
	}
	return out
}
