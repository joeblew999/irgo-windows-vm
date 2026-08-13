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

// ISOCatalogEntry is one downloadable image.
type ISOCatalogEntry struct {
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
func (e ISOCatalogEntry) IsARM64() bool { return strings.EqualFold(e.Architecture, "ARM64") }

// Build is the Windows build string the filename starts with, e.g.
// "26100.4349.250607-1500". It is the only version identifier in the catalog —
// there is no field for it — and it is what distinguishes two entries that are
// otherwise identical.
func (e ISOCatalogEntry) Build() string {
	name := e.FileName
	// Three dot-separated groups, then an underscore: 26100.4349.250607-1500.ge_...
	parts := strings.SplitN(name, ".", 4)
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:3], ".")
}

// ISOFetchCatalog downloads and parses the ISOGet Creation ISOTool catalog.
func ISOFetchCatalog(timeout time.Duration) ([]ISOCatalogEntry, error) {
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, ISOCatalogURL, nil)
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
	xmlBytes, err := isoExtractCatalogCAB(cab)
	if err != nil {
		return nil, err
	}
	return isoParseCatalog(xmlBytes)
}

// ISOParseCatalog exposes the parser for a products.xml already on disk, so a
// cached copy (CrystalFetch keeps one) can be used without a network call.
func ISOParseCatalog(b []byte) ([]ISOCatalogEntry, error) { return isoParseCatalog(b) }

func isoParseCatalog(b []byte) ([]ISOCatalogEntry, error) {
	var doc struct {
		Files []ISOCatalogEntry `xml:"Catalogs>Catalog>PublishedMedia>Files>File"`
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
	out := make([]ISOCatalogEntry, 0, len(doc.Files))
	for _, f := range doc.Files {
		if f.FilePath == "" || seen[f.FilePath] {
			continue
		}
		seen[f.FilePath] = true
		out = append(out, f)
	}
	return out, nil
}

// isoErrNotMSZIP says which compression a cabinet used, so the caller can decide
// whether it has another way to read it.
type isoErrNotMSZIP struct{ kind uint16 }

func (e isoErrNotMSZIP) Error() string {
	return fmt.Sprintf("cabinet uses %s compression, not MSZIP", isoCompressionName(e.kind))
}

// isoExtractCatalogCAB reads the catalog out of its cabinet.
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
func isoExtractCatalogCAB(cab []byte) ([]byte, error) {
	xmlBytes, err := isoExtractSingleFileCAB(cab)
	if err == nil {
		return xmlBytes, nil
	}
	var notMSZIP isoErrNotMSZIP
	if !errors.As(err, &notMSZIP) {
		return nil, fmt.Errorf("utmvm: catalog cab: %w", err)
	}

	xmlBytes, exErr := isoExtractCABWithLibarchive(cab)
	if exErr != nil {
		return nil, fmt.Errorf("utmvm: catalog cab: %w, and %w", err, exErr)
	}
	return xmlBytes, nil
}

// isoExtractCABWithLibarchive shells to bsdtar for the LZX case.
//
// The temporary directory is ours and is removed: bsdtar extracts by name, and
// letting an archive choose where to write in a directory somebody else uses is
// how a download becomes a path-traversal bug.
func isoExtractCABWithLibarchive(cab []byte) ([]byte, error) {
	tar := ISOTool{Name: "bsdtar", Formula: "libarchive",
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

func isoExtractSingleFileCAB(cab []byte) ([]byte, error) {
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
		return nil, isoErrNotMSZIP{typeCompress}
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
		dict = isoTailBytes(plain, 1<<15)
		off = start + cbData
	}
	return out.Bytes(), nil
}

func isoCompressionName(t uint16) string {
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

// ISOCachedCatalogPaths are products.xml files another tool has already extracted.
//
// CrystalFetch — UTM's own authors' ISO downloader — keeps one, which is both
// the fallback while LZX is unimplemented and the evidence that this catalog is
// the right source: the ISO this project uses is the entry it lists.
func ISOCachedCatalogPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Library", "Containers", "llc.turing.CrystalFetch",
			"Data", "Library", "Caches", "products.xml"),
	}
}

func isoTailBytes(b []byte, n int) []byte {
	if len(b) <= n {
		// Copy: flate keeps a reference to the dictionary, and the caller's
		// slice is reused on the next iteration.
		return append([]byte(nil), b...)
	}
	return append([]byte(nil), b[len(b)-n:]...)
}

// isoFilterCatalog narrows the catalog to what this project can use. Every
// argument is optional; an empty string matches everything.
func isoFilterCatalog(all []ISOCatalogEntry, arch, lang, edition string) []ISOCatalogEntry {
	var out []ISOCatalogEntry
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

// isoDownload fetches url to dest, resuming a partial file and verifying sha1.
//
// dest must not exist. The staging file is dest+".part", which is resumable
// across runs: a 4 GB download that dies at 90% costs the last 10%, not the
// whole thing, and the servers support ranged requests.
//
// progress, if non-nil, is called about once a second with bytes so far and the
// total. A 4 GB download with no output looks identical to a hung one.
func isoDownload(url, dest, wantSHA1 string, progress func(done, total int64)) error {
	if err := isoRefuseUnsafeDest(dest); err != nil {
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
		got, hErr := isoFileSHA1(part)
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

// isoFileSHA1 hashes a file, which for a 4 GB ESD takes a few seconds.
func isoFileSHA1(path string) (string, error) {
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

// isoRefuseUnsafeDest stops a download from destroying media already in use.
//
// Three separate refusals, because each is a different mistake: writing over a
// file that exists, writing over a file some VM is booting from, and writing
// over one that was deliberately made immutable.
func isoRefuseUnsafeDest(dest string) error {
	fi, err := os.Stat(dest)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return os.MkdirAll(filepath.Dir(dest), 0o755)
	}

	msg := fmt.Sprintf("utmvm: %s already exists (%s)", dest, HumanBytes(fi.Size()))

	if _, nlink, ok := inodeInfo(dest); ok && nlink > 1 {
		st, sErr := isoLinks(dest, isoSearchDirs())
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

// isoInfo is what we can learn about Windows install media without mounting it.

// ISOGetOptions configures ISOGet.
type ISOGetOptions struct {
	ISO   string // use this file; empty means find or make one
	Fetch bool   // permit a 4.2 GB download when nothing local works
}

// ISOGet finds usable Windows media, or makes some.
//
// It has nothing to do with UTM, and deliberately does not touch it. Getting a
// Windows ISO is a download from Microsoft or an expansion of an ESD; a
// hypervisor is not involved, and requiring one to be installed first — which
// this did, via the setup chain — made fetching media impossible on a machine
// that had not installed UTM yet.

// ensureMedia finds usable Windows media, or makes some.
//
// The order is what a developer would want if they thought about it: use what
// is here, then what they built earlier, then build from an ESD they already
// downloaded, and only then spend 4.2 GB of somebody's bandwidth.
func ISOGet(opts ISOGetOptions, say func(string, ...any)) (iso, detail string, skipped bool, err error) {
	// Named explicitly.
	if opts.ISO != "" {
		if _, sErr := os.Stat(opts.ISO); sErr != nil {
			return "", "", false, fmt.Errorf("no such ISO: %s", opts.ISO)
		}
		return opts.ISO, filepath.Base(opts.ISO), true, nil
	}

	// Every step announces itself BEFORE doing the work, with what it is about
	// to try. A command that prints nothing for fifty seconds is
	// indistinguishable from one that has hung, and the first thing anybody
	// asks when this goes wrong is "what was it doing".
	say("STEP 1/4  looking for media already here")
	for _, candidate := range []string{
		isoPath(),
		isoBuiltPath(),
	} {
		if _, sErr := os.Stat(candidate); sErr != nil {
			say("          not there: %s", Home(candidate))
			continue
		}
		say("          found %s — checking it is ARM64 (reads the whole file the first time)", Home(candidate))
		info, iErr := isoInspect(candidate)
		if iErr != nil || !info.IsARM64 {
			say("  … %s is not ARM64 media; ignoring it", filepath.Base(candidate))
			continue
		}
		say("          it is ARM64 — using it, nothing to do")
		return candidate, filepath.Base(candidate), true, nil
	}

	// An ESD already downloaded — build from it rather than downloading again.
	esd := isoESDPath()
	say("STEP 2/4  looking for a downloaded .esd to build from")
	if _, sErr := os.Stat(esd); sErr == nil {
		built := isoBuiltPath()
		say("          found %s — skipping the 4.2 GB download", Home(esd))
		say("STEP 4/4  expanding it and mastering a bootable ISO")
		if bErr := isoBuildFromESD(esd, built, say); bErr != nil {
			return "", "", false, bErr
		}
		return built, filepath.Base(built), false, nil
	}

	say("          not there: %s", Home(esd))
	if !opts.Fetch {
		return "", "", false, fmt.Errorf(
			"no Windows media found.\n"+
				"     Put an ARM64 ISO at %s, or re-run with -fetch to download\n"+
				"     4.2 GB from Microsoft and build one (needs wimlib and xorriso).",
			Home(isoPath()))
	}

	say("STEP 3/4  asking Microsoft which build to download")
	all, cErr := ISOFetchCatalog(2 * time.Minute)
	if cErr != nil {
		return "", "", false, cErr
	}
	match := isoFilterCatalog(all, "ARM64", "en-us", "CLIENTCONSUMER")
	if len(match) != 1 {
		return "", "", false, fmt.Errorf("catalog matched %d ARM64 en-us images, expected exactly 1", len(match))
	}
	e := match[0]
	say("          build %s, %s", e.Build(), HumanBytes(e.Size))
	say("          downloading to %s", Home(esd))
	if err := os.MkdirAll(ISODir(), 0o755); err != nil {
		return "", "", false, err
	}
	if dErr := isoDownload(e.FilePath, esd, e.Sha1, func(done, total int64) {
		if total > 0 {
			say("      %s / %s", HumanBytes(done), HumanBytes(total))
		}
	}); dErr != nil {
		return "", "", false, dErr
	}

	built := isoBuiltPath()
	say("STEP 4/4  expanding it and mastering a bootable ISO at %s", Home(built))
	if bErr := isoBuildFromESD(esd, built, say); bErr != nil {
		return "", "", false, bErr
	}
	return built, filepath.Base(built), false, nil
}

func isoBuildFromESD(esd, out string, say func(string, ...any)) error {
	// Scratch lives beside the media, not in a shared work directory: the ISO
	// code owns everywhere it writes.
	media, err := isoWorkDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(media); err != nil {
		return err
	}
	defer os.RemoveAll(media)

	say("          expanding %s into %s", Home(esd), Home(media))
	if err := isoExpandESD(esd, media, func(step string) { say("            %s", step) }); err != nil {
		return err
	}
	say("          mastering the ISO with xorriso (takes a minute)")
	if err := isoBuild(isoRemasterOptions{
		Source:   media,
		Output:   out,
		Label:    "WINDOWS_ARM64",
		NoPrompt: true,
	}); err != nil {
		return err
	}

	// Record what we already know, so nothing re-reads 4.9 GB to learn it.
	//
	// This ISO was mastered here from an ARM64 .esd — the architecture is not
	// in question. Without writing it down the next command scanned the whole
	// file to rediscover it, which measured at 77 seconds and looked like a
	// hang every time.
	isoStoreVerdict(out, isoInfo{IsARM64: true})
	return nil
}

// isoWorkDir is scratch space for expanding an ESD, emptied before use. It
// needs about 12 GB: the expanded tree is roughly the size of the ISO it will
// become, plus the images being written into it.
func isoWorkDir() (string, error) {
	d := filepath.Join(ISODir(), "work")
	if err := os.RemoveAll(d); err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}
