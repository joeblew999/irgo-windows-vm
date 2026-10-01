package utmvm

// What the golden image looks like in the private R2 cache, and moving it
// there and back: vm-golden-push and vm-golden-pull. Transport only. A bundle
// directory goes up; the same bytes come down into a directory of ours.
// Registering the result with UTM is the golden image's job (vm_golden.go),
// not this file's.
//
// In the bucket, under golden/:
//
//	chunks/<sha256>.zst     one region of one file, zstd, named by the SHA-256
//	                        of its UNCOMPRESSED bytes, so equal regions are one
//	                        object and a new image uploads only what changed
//	manifests/<sha256>.json the file list, named by the SHA-256 of its own bytes,
//	                        so a manifest that does not hash to its name is refused
//	latest                  the id of the newest manifest
//
// Files are cut at fixed offsets, not by content: a disk image does not shift,
// its blocks stay where they are. A region that is all zeros is not stored at
// all, and is not written back, so the holes of a sparse disk stay holes.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/klauspost/compress/zstd"
)

const (
	// goldenChunkSize is the region size. 64 MiB: large enough that a 64 GiB
	// disk is about a thousand objects, small enough that a failed chunk costs
	// seconds to fetch again, and under the 512 MB above which Cloudflare's
	// CDN will not cache an object.
	goldenChunkSize = 64 << 20

	// goldenHoleBlock is the granularity at which a pulled region's zeros are
	// skipped rather than written, so a region that is mostly zeros stays
	// mostly holes. APFS allocates in 4 KiB blocks; 64 KiB keeps the loop cheap.
	goldenHoleBlock = 64 << 10

	// GoldenParallel is how many chunks move at once by default. Each holds a
	// region and its compressed copy in memory, about 100 MB.
	GoldenParallel = 4

	goldenCacheFormat = 1
	goldenPrefix      = "golden/"
	goldenLatestKey   = goldenPrefix + "latest"

	// goldenJSONName is the golden image's own record under the app root,
	// carried in the manifest so a pulled image says what it is.
	goldenJSONName = "golden.json"
)

func goldenChunkKey(id string) string    { return goldenPrefix + "chunks/" + id + ".zst" }
func goldenManifestKey(id string) string { return goldenPrefix + "manifests/" + id + ".json" }

// GoldenPullDir is where vm-golden-pull puts what it downloads by default.
func GoldenPullDir() string { return filepath.Join(appRoot(), "golden-pull") }

// GoldenJSONPath is the golden image's record that vm-golden-push carries.
func GoldenJSONPath() string { return filepath.Join(appRoot(), goldenJSONName) }

// cacheManifest is one pushed golden image.
type cacheManifest struct {
	Format      int                   `json:"format"`
	Bundle      string                `json:"bundle"` // the bundle directory's name
	ToolVersion string                `json:"tool_version"`
	Created     time.Time             `json:"created"`
	Golden      json.RawMessage       `json:"golden,omitempty"` // golden.json as pushed
	ChunkSize   int64                 `json:"chunk_size"`
	Files       []cacheFile           `json:"files"`
	Chunks      map[string]cacheChunk `json:"chunks"` // by the SHA-256 of the uncompressed region
}

// cacheFile is one file of the bundle.
type cacheFile struct {
	Path string `json:"path"` // slash-separated, relative to the bundle
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"` // permission bits

	// SHA256 is the file's tree hash: the SHA-256 of its regions' SHA-256s,
	// concatenated in order (a zero region's being that of its zeros). It
	// verifies the whole file without hashing gigabytes of holes.
	SHA256 string `json:"sha256"`

	// Regions is the chunk for each region in order, "" for all zeros.
	Regions []string `json:"regions"`
}

// cacheChunk is one stored region.
type cacheChunk struct {
	Size    int64  `json:"size"`    // uncompressed
	ZSize   int64  `json:"zsize"`   // the object's length
	ZSHA256 string `json:"zsha256"` // of the object, which the download verifies
}

// validate refuses a manifest that could write outside its directory or does
// not add up, before anything is downloaded.
func (m *cacheManifest) validate() error {
	if m.Format != goldenCacheFormat {
		return fmt.Errorf("manifest format %d, this irgo-winvm reads %d", m.Format, goldenCacheFormat)
	}
	if m.ChunkSize <= 0 || m.ChunkSize > 1<<30 {
		return fmt.Errorf("manifest chunk size %d", m.ChunkSize)
	}
	if !safeName(m.Bundle) {
		return fmt.Errorf("manifest bundle name %q is not a plain directory name", m.Bundle)
	}
	for id, c := range m.Chunks {
		if !isSHA256Hex(id) || !isSHA256Hex(c.ZSHA256) || c.Size <= 0 || c.Size > m.ChunkSize || c.ZSize <= 0 {
			return fmt.Errorf("manifest chunk %q is malformed", id)
		}
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		clean := path.Clean(f.Path)
		if f.Path == "" || clean != f.Path || path.IsAbs(clean) || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, "../") || strings.Contains(f.Path, `\`) || seen[clean] {
			return fmt.Errorf("manifest file path %q is not a plain relative path", f.Path)
		}
		seen[clean] = true
		if f.Size < 0 || int64(len(f.Regions)) != regionCount(f.Size, m.ChunkSize) || !isSHA256Hex(f.SHA256) {
			return fmt.Errorf("manifest entry for %s does not add up", f.Path)
		}
		for i, id := range f.Regions {
			if id == "" {
				continue
			}
			c, ok := m.Chunks[id]
			if !ok {
				return fmt.Errorf("%s region %d names chunk %s, which the manifest does not list", f.Path, i, id)
			}
			if c.Size != regionLen(f.Size, m.ChunkSize, i) {
				return fmt.Errorf("%s region %d is %d bytes, and its chunk %d", f.Path, i, regionLen(f.Size, m.ChunkSize, i), c.Size)
			}
		}
	}
	return nil
}

func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func regionCount(size, chunk int64) int64 { return (size + chunk - 1) / chunk }

func regionLen(size, chunk int64, i int) int64 {
	return min(chunk, size-int64(i)*chunk)
}

func allZero(b []byte) bool {
	for len(b) >= 8 {
		if b[0]|b[1]|b[2]|b[3]|b[4]|b[5]|b[6]|b[7] != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// zeroDigests caches the SHA-256 of n zero bytes: every hole of a disk hashes
// to the same few values, and hashing 40 GB of zeros to learn that is waste.
var zeroDigests sync.Map // int64 -> [32]byte

func zeroDigest(n int64) [32]byte {
	if d, ok := zeroDigests.Load(n); ok {
		return d.([32]byte)
	}
	d := sha256.Sum256(make([]byte, n))
	zeroDigests.Store(n, d)
	return d
}

// treeHash is a file's SHA256 from its region digests.
func treeHash(digests [][32]byte) string {
	h := sha256.New()
	for _, d := range digests {
		h.Write(d[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fileTreeHash reads a file back and computes its tree hash, which is how a
// pull proves what is on disk rather than what it meant to write.
func fileTreeHash(p string, chunk int64) (string, error) {
	f, err := os.Open(p) //nolint:gosec // a path this package wrote
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	buf := make([]byte, chunk)
	digests := make([][32]byte, regionCount(fi.Size(), chunk))
	for i := range digests {
		b := buf[:regionLen(fi.Size(), chunk, i)]
		if _, err := io.ReadFull(f, b); err != nil {
			return "", err
		}
		if allZero(b) {
			digests[i] = zeroDigest(int64(len(b)))
		} else {
			digests[i] = sha256.Sum256(b)
		}
	}
	return treeHash(digests), nil
}

// zstd codecs. EncodeAll and DecodeAll are safe for concurrent use.
var (
	zstdEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	zstdDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(2<<30))
)

// throttle returns a printer that says at most one line every interval, and
// always the last one it was given when flushed.
func throttle(say func(string, ...any), every time.Duration) (func(string, ...any), func()) {
	var mu sync.Mutex
	var last time.Time
	var pending string
	tick := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		pending = fmt.Sprintf(f, a...)
		if time.Since(last) >= every {
			say("%s", pending)
			pending, last = "", time.Now()
		}
	}
	flush := func() {
		mu.Lock()
		defer mu.Unlock()
		if pending != "" {
			say("%s", pending)
			pending = ""
		}
	}
	return tick, flush
}

// GoldenPushOptions configures GoldenPush.
type GoldenPushOptions struct {
	R2          R2Config
	Bundle      string // the bundle directory to push
	Golden      string // golden.json to carry; missing is allowed and said
	ToolVersion string
	Parallel    int

	chunkSize int64 // tests use small regions; zero means goldenChunkSize
}

// GoldenPushResult is what a push did.
type GoldenPushResult struct {
	ID            string // the manifest's id, which vm-golden-pull -id takes
	Bytes         int64  // the bundle's apparent size
	Stored        int64  // uncompressed bytes in non-zero regions
	Uploaded      int    // chunks sent
	UploadedBytes int64  // compressed bytes sent
	Reused        int    // chunks already in the bucket
}

// GoldenPush uploads a bundle directory to the private cache: only the chunks
// the bucket does not already have, then the manifest, then latest. It refuses
// a bucket that is public or whose exposure cannot be read.
func GoldenPush(ctx context.Context, o GoldenPushOptions, say func(string, ...any)) (GoldenPushResult, error) {
	var res GoldenPushResult
	chunk := o.chunkSize
	if chunk == 0 {
		chunk = goldenChunkSize
	}
	par := max(o.Parallel, 1)
	say("%s", licenceCondition)

	say("STEP 1/4  the bucket")
	if err := o.R2.requirePrivate(ctx, say); err != nil {
		return res, err
	}
	cl := o.R2.client()

	say("STEP 2/4  the bundle %s", Home(o.Bundle))
	type entry struct {
		rel  string
		size int64
		mode fs.FileMode
	}
	var files []entry
	err := filepath.WalkDir(o.Bundle, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rErr := filepath.Rel(o.Bundle, p)
		if rErr != nil {
			return rErr
		}
		if d.Name() == ".DS_Store" {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file (%s); a bundle holds only files", Home(p), d.Type())
		}
		fi, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		files = append(files, entry{filepath.ToSlash(rel), fi.Size(), fi.Mode().Perm()})
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return res, fmt.Errorf("%w\n  macOS refuses this process UTM's container; push a copy UTM exported, with -bundle", err)
		}
		return res, err
	}
	if len(files) == 0 {
		return res, fmt.Errorf("%s holds no files", Home(o.Bundle))
	}
	for _, f := range files {
		res.Bytes += f.size
		say("          %-10s %s", HumanBytes(f.size), f.rel)
	}

	m := cacheManifest{
		Format:      goldenCacheFormat,
		Bundle:      filepath.Base(filepath.Clean(o.Bundle)),
		ToolVersion: o.ToolVersion,
		Created:     time.Now().UTC(),
		ChunkSize:   chunk,
		Chunks:      map[string]cacheChunk{},
	}
	if !safeName(m.Bundle) {
		return res, fmt.Errorf("%s is not a usable bundle directory name", m.Bundle)
	}
	if b, rErr := os.ReadFile(o.Golden); rErr == nil {
		if !json.Valid(b) {
			return res, fmt.Errorf("%s is not JSON", Home(o.Golden))
		}
		m.Golden = json.RawMessage(bytes.TrimSpace(b))
		say("          carrying %s", Home(o.Golden))
	} else if errors.Is(rErr, fs.ErrNotExist) {
		say("          no %s; the manifest will not say which Windows this is", Home(o.Golden))
	} else {
		return res, rErr
	}

	say("STEP 3/4  chunks: %s regions, only those the bucket lacks are sent, %d at a time", HumanBytes(chunk), par)
	t0 := time.Now()
	up := &chunkUploader{cl: cl, bucket: o.R2.Bucket, table: m.Chunks, claimed: map[string]bool{}}
	tick, flush := throttle(say, 2*time.Second)
	var read atomic.Int64
	for _, f := range files {
		cf, sErr := up.pushFile(ctx, filepath.Join(o.Bundle, filepath.FromSlash(f.rel)), f.size, chunk, par, func() {
			tick("          %s read of %s, %d sent (%s), %d already there  [%s]",
				HumanBytes(read.Load()), HumanBytes(res.Bytes), up.sent.Load(), HumanBytes(up.sentBytes.Load()),
				up.reused.Load(), time.Since(t0).Round(time.Second))
		}, &read)
		if sErr != nil {
			return res, sErr
		}
		cf.Path, cf.Mode = f.rel, uint32(f.mode)
		m.Files = append(m.Files, cf)
	}
	flush()
	res.Uploaded, res.UploadedBytes, res.Reused = int(up.sent.Load()), up.sentBytes.Load(), int(up.reused.Load())
	for _, c := range m.Chunks {
		res.Stored += c.Size
	}
	say("          %d chunks sent (%s), %d already there, %s of data in %d chunks  [%s]",
		res.Uploaded, HumanBytes(res.UploadedBytes), res.Reused, HumanBytes(res.Stored), len(m.Chunks),
		time.Since(t0).Round(time.Second))

	// Verified before the manifest names them: every chunk it lists is in the
	// bucket at the length recorded.
	for id, c := range m.Chunks {
		h, hErr := cl.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(o.R2.Bucket), Key: aws.String(goldenChunkKey(id))})
		if hErr != nil {
			return res, fmt.Errorf("checking chunk %s after the upload: %w", id, hErr)
		}
		if aws.ToInt64(h.ContentLength) != c.ZSize {
			return res, fmt.Errorf("chunk %s is %d bytes in the bucket, and %d were sent", id, aws.ToInt64(h.ContentLength), c.ZSize)
		}
	}

	say("STEP 4/4  the manifest")
	if err := m.validate(); err != nil {
		return res, fmt.Errorf("the manifest this push built is invalid: %w", err)
	}
	body, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return res, err
	}
	sum := sha256.Sum256(body)
	res.ID = hex.EncodeToString(sum[:])
	if err := putBytes(ctx, cl, o.R2.Bucket, goldenManifestKey(res.ID), body, nil); err != nil {
		return res, err
	}
	got, err := getBytes(ctx, cl, o.R2.Bucket, goldenManifestKey(res.ID))
	if err != nil {
		return res, fmt.Errorf("reading the manifest back: %w", err)
	}
	if !bytes.Equal(got, body) {
		return res, fmt.Errorf("the manifest read back from the bucket is not the one written")
	}
	if err := putBytes(ctx, cl, o.R2.Bucket, goldenLatestKey, []byte(res.ID+"\n"), nil); err != nil {
		return res, err
	}
	say("          %s", goldenManifestKey(res.ID))
	say("          latest -> %s", res.ID)
	return res, nil
}

// chunkUploader sends the regions of files as chunks, each once.
type chunkUploader struct {
	cl     *s3.Client
	bucket string

	mu      sync.Mutex
	table   map[string]cacheChunk // what the manifest will list
	claimed map[string]bool       // chunks some goroutine is already handling

	sent, reused atomic.Int64
	sentBytes    atomic.Int64
}

// pushFile reads one file region by region and makes sure each non-zero
// region is in the bucket. Up to par regions are in flight; the buffers are
// the bound.
func (u *chunkUploader) pushFile(ctx context.Context, p string, size, chunk int64, par int,
	progress func(), read *atomic.Int64) (cacheFile, error) {
	cf := cacheFile{Size: size}
	f, err := os.Open(p) //nolint:gosec // the bundle the caller named
	if err != nil {
		return cf, err
	}
	defer func() { _ = f.Close() }() // read-only

	n := regionCount(size, chunk)
	cf.Regions = make([]string, n)
	digests := make([][32]byte, n)

	free := make(chan []byte, par)
	for range par {
		free <- make([]byte, chunk)
	}
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	fail := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}
	failed := func() bool {
		errMu.Lock()
		defer errMu.Unlock()
		return firstErr != nil
	}

	for i := range int(n) {
		if failed() {
			break
		}
		buf := <-free
		b := buf[:regionLen(size, chunk, i)]
		if _, rErr := io.ReadFull(f, b); rErr != nil {
			fail(fmt.Errorf("reading %s: %w", Home(p), rErr))
			break
		}
		read.Add(int64(len(b)))
		if allZero(b) {
			digests[i] = zeroDigest(int64(len(b)))
			free <- buf
			progress()
			continue
		}
		wg.Add(1)
		go func(i int, buf, b []byte) {
			defer wg.Done()
			defer func() { free <- buf }()
			d := sha256.Sum256(b)
			id := hex.EncodeToString(d[:])
			digests[i], cf.Regions[i] = d, id
			if e := u.ensure(ctx, id, b); e != nil {
				fail(e)
			}
			progress()
		}(i, buf, b)
	}
	wg.Wait()
	if firstErr != nil {
		return cf, firstErr
	}
	cf.SHA256 = treeHash(digests)
	return cf, nil
}

// ensure puts one region in the bucket unless it is there already. A chunk
// already there is known by its name, the SHA-256 of what it holds, and its
// compressed digest is read from the object's metadata.
func (u *chunkUploader) ensure(ctx context.Context, id string, raw []byte) error {
	u.mu.Lock()
	if u.claimed[id] {
		u.mu.Unlock()
		return nil
	}
	u.claimed[id] = true
	u.mu.Unlock()

	key := goldenChunkKey(id)
	h, err := u.cl.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(u.bucket), Key: aws.String(key)})
	switch {
	case err == nil:
		z := h.Metadata["zsha256"]
		if isSHA256Hex(z) && aws.ToInt64(h.ContentLength) > 0 {
			u.record(id, cacheChunk{Size: int64(len(raw)), ZSize: aws.ToInt64(h.ContentLength), ZSHA256: z})
			u.reused.Add(1)
			return nil
		}
		// There, but not written by this code: sent again, over it.
	case isNotFound(err):
	default:
		return fmt.Errorf("asking the bucket for chunk %s: %w", id, err)
	}

	z := zstdEncoder.EncodeAll(raw, make([]byte, 0, len(raw)/2))
	zs := sha256.Sum256(z)
	c := cacheChunk{Size: int64(len(raw)), ZSize: int64(len(z)), ZSHA256: hex.EncodeToString(zs[:])}
	if err := putBytes(ctx, u.cl, u.bucket, key, z, map[string]string{"zsha256": c.ZSHA256}); err != nil {
		return err
	}
	u.record(id, c)
	u.sent.Add(1)
	u.sentBytes.Add(c.ZSize)
	return nil
}

func (u *chunkUploader) record(id string, c cacheChunk) {
	u.mu.Lock()
	u.table[id] = c
	u.mu.Unlock()
}

func isNotFound(err error) bool {
	var nf *types.NotFound
	var nk *types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nk)
}

func putBytes(ctx context.Context, cl *s3.Client, bucket, key string, b []byte, meta map[string]string) error {
	_, err := cl.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(b),
		ContentLength: aws.Int64(int64(len(b))),
		Metadata:      meta,
	})
	if err != nil {
		return fmt.Errorf("uploading %s: %w", key, err)
	}
	return nil
}

func getBytes(ctx context.Context, cl *s3.Client, bucket, key string) ([]byte, error) {
	out, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()
	return io.ReadAll(io.LimitReader(out.Body, 64<<20))
}

// resolveManifest returns the manifest id asked for, or latest's, with the
// manifest's bytes, refusing bytes that do not hash to the id. An empty id
// with no latest in the bucket is ("", nil, nil): nothing has been pushed.
func resolveManifest(ctx context.Context, cl *s3.Client, bucket, id string) (string, []byte, error) {
	if id == "" {
		b, err := getBytes(ctx, cl, bucket, goldenLatestKey)
		if isNotFound(err) {
			return "", nil, nil
		}
		if err != nil {
			return "", nil, fmt.Errorf("reading %s: %w", goldenLatestKey, err)
		}
		id = strings.TrimSpace(string(b))
	}
	if !isSHA256Hex(id) {
		return "", nil, fmt.Errorf("manifest id %q is not a SHA-256", id)
	}
	b, err := getBytes(ctx, cl, bucket, goldenManifestKey(id))
	if isNotFound(err) {
		return id, nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("reading manifest %s: %w", id, err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != id {
		return "", nil, fmt.Errorf("manifest %s does not hash to its name (it hashes to %s); refusing it",
			id, hex.EncodeToString(sum[:]))
	}
	return id, b, nil
}

// GoldenPullOptions configures GoldenPull.
type GoldenPullOptions struct {
	R2       R2Config
	Dir      string // GoldenPullDir by default
	ID       string // a manifest id; empty is latest
	Parallel int
}

// GoldenPullResult is what a pull left where.
type GoldenPullResult struct {
	ID         string
	Bundle     string // the bundle directory
	Golden     string // golden.json beside it, or "" when the push carried none
	Bytes      int64  // apparent size of the files
	Downloaded int64  // compressed bytes fetched this run
	Skipped    bool   // it was already there
}

// pulledManifestName is the manifest a pull keeps beside the bundle. It is
// written last, so its presence means the pull finished.
const pulledManifestName = "manifest.json"

// GoldenPull downloads the golden image from the private cache into
// Dir/<bundle>, verifying every chunk and every file. Chunks already fetched
// by an interrupted run are kept in Dir/.parts and not fetched again, and a
// chunk cut off mid-transfer resumes where it stopped.
func GoldenPull(ctx context.Context, o GoldenPullOptions, say func(string, ...any)) (GoldenPullResult, error) {
	var res GoldenPullResult
	par := max(o.Parallel, 1)
	say("%s", licenceCondition)

	say("STEP 1/5  the bucket")
	if err := o.R2.requirePrivate(ctx, say); err != nil {
		return res, err
	}
	cl := o.R2.client()

	say("STEP 2/5  the manifest")
	id, body, err := resolveManifest(ctx, cl, o.R2.Bucket, o.ID)
	if err != nil {
		return res, err
	}
	if body == nil {
		if id == "" {
			return res, fmt.Errorf("%s holds no golden image (no %s); vm-golden-push one first", o.R2.Where(), goldenLatestKey)
		}
		return res, fmt.Errorf("%s has no manifest %s", o.R2.Where(), id)
	}
	var m cacheManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return res, fmt.Errorf("manifest %s: %w", id, err)
	}
	if err := m.validate(); err != nil {
		return res, fmt.Errorf("manifest %s: %w", id, err)
	}
	res.ID = id
	for _, f := range m.Files {
		res.Bytes += f.Size
	}
	say("          %s: %s, %d files, %s, pushed %s by irgo-winvm %s",
		id[:12], m.Bundle, len(m.Files), HumanBytes(res.Bytes), m.Created.Format(time.RFC3339), m.ToolVersion)

	res.Bundle = filepath.Join(o.Dir, m.Bundle)
	marker := filepath.Join(o.Dir, pulledManifestName)
	if prev, rErr := os.ReadFile(marker); rErr == nil {
		if bytes.Equal(prev, body) {
			if _, sErr := os.Stat(res.Bundle); sErr == nil {
				res.Skipped = true
				if len(m.Golden) > 0 {
					res.Golden = filepath.Join(o.Dir, goldenJSONName)
				}
				say("          already pulled into %s", Home(res.Bundle))
				return res, nil
			}
		}
		return res, fmt.Errorf("%s already holds a different pull; vm-golden-pull -delete -force first", Home(o.Dir))
	} else if !errors.Is(rErr, fs.ErrNotExist) {
		return res, rErr
	}
	if _, sErr := os.Stat(res.Bundle); sErr == nil {
		return res, fmt.Errorf("%s exists and is not a finished pull; vm-golden-pull -delete -force first", Home(res.Bundle))
	}

	parts := filepath.Join(o.Dir, ".parts")
	var need []string
	var needBytes int64
	for cid, c := range m.Chunks {
		need = append(need, cid)
		needBytes += c.ZSize
	}
	sort.Strings(need)
	say("STEP 3/5  %d chunks, %s, into %s, %d at a time", len(need), HumanBytes(needBytes), Home(parts), par)
	if err := os.MkdirAll(parts, 0o755); err != nil {
		return res, err
	}
	t0 := time.Now()
	tick, flush := throttle(say, 2*time.Second)
	var done, fetched atomic.Int64
	err = forEach(need, par, func(cid string) error {
		c := m.Chunks[cid]
		dest := filepath.Join(parts, cid+".zst")
		if _, sErr := os.Stat(dest); sErr == nil {
			// Renamed into place only after its SHA-256 matched.
			done.Add(c.ZSize)
			return nil
		}
		if fErr := fetchChunk(ctx, o.R2, cl, cid, c, dest); fErr != nil {
			return fErr
		}
		done.Add(c.ZSize)
		fetched.Add(c.ZSize)
		tick("          %s of %s  [%s]", HumanBytes(done.Load()), HumanBytes(needBytes), time.Since(t0).Round(time.Second))
		return nil
	})
	flush()
	res.Downloaded = fetched.Load()
	if err != nil {
		return res, fmt.Errorf("%w\n  what arrived is kept in %s; run vm-golden-pull again to carry on", err, Home(parts))
	}
	say("          %s fetched, %s were already here  [%s]", HumanBytes(res.Downloaded),
		HumanBytes(needBytes-res.Downloaded), time.Since(t0).Round(time.Second))

	tmp := filepath.Join(o.Dir, ".tmp-"+m.Bundle)
	say("STEP 4/5  rebuilding the files in %s, holes left as holes", Home(tmp))
	if err := os.RemoveAll(tmp); err != nil {
		return res, err
	}
	for _, f := range m.Files {
		t1 := time.Now()
		p := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := assembleFile(p, f, &m, parts, par); err != nil {
			return res, err
		}
		got, err := fileTreeHash(p, m.ChunkSize)
		if err != nil {
			return res, err
		}
		if got != f.SHA256 {
			return res, fmt.Errorf("%s rebuilt with tree hash %s, and the manifest says %s", f.Path, got, f.SHA256)
		}
		say("          ✓ %-10s %s  [%s]", HumanBytes(f.Size), f.Path, time.Since(t1).Round(time.Millisecond))
	}

	say("STEP 5/5  into place")
	if err := os.Rename(tmp, res.Bundle); err != nil {
		return res, err
	}
	if len(m.Golden) > 0 {
		res.Golden = filepath.Join(o.Dir, goldenJSONName)
		if err := writeFileSynced(res.Golden, append(append([]byte{}, m.Golden...), '\n')); err != nil {
			return res, err
		}
	}
	if err := writeFileSynced(marker, body); err != nil {
		return res, err
	}
	if err := os.RemoveAll(parts); err != nil {
		return res, err
	}
	say("          %s", Home(res.Bundle))
	return res, nil
}

// fetchChunk downloads one chunk through the ISO downloader, which resumes a
// .part and verifies the SHA-256 before renaming. A mismatch is fetched once
// more from nothing before it is an error: a truncated resume is likelier than
// a bad object.
func fetchChunk(ctx context.Context, r R2Config, cl *s3.Client, id string, c cacheChunk, dest string) error {
	key := goldenChunkKey(id)
	presign := s3.NewPresignClient(cl)
	for attempt := 1; ; attempt++ {
		req, err := presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.Bucket), Key: aws.String(key)},
			s3.WithPresignExpires(30*time.Minute))
		if err != nil {
			return err
		}
		err = isoDownload(req.URL, dest, sha256Digest(c.ZSHA256), nil)
		if err == nil {
			return nil
		}
		// The presigned URL is a credential for this object until it expires,
		// and errors are logged: name the key instead.
		err = errors.New(strings.ReplaceAll(err.Error(), req.URL, key))
		if !strings.Contains(err.Error(), "mismatch") || attempt == 2 {
			return fmt.Errorf("chunk %s: %w", id, err)
		}
		_ = os.Remove(dest + ".part")
	}
}

// assembleFile writes a file from its chunks at their offsets. The file is
// truncated to its size first, so every region not written, and every zero
// block inside a written one, is a hole.
func assembleFile(p string, f cacheFile, m *cacheManifest, parts string, par int) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(f.Mode)&fs.ModePerm|0o200) //nolint:gosec // under our own directory
	if err != nil {
		return err
	}
	if err := out.Truncate(f.Size); err != nil {
		_ = out.Close() // already failing
		return err
	}
	idx := make([]int, 0, len(f.Regions))
	for i, id := range f.Regions {
		if id != "" {
			idx = append(idx, i)
		}
	}
	err = forEach(idx, par, func(i int) error {
		id := f.Regions[i]
		z, rErr := os.ReadFile(filepath.Join(parts, id+".zst")) //nolint:gosec // under our own directory
		if rErr != nil {
			return rErr
		}
		raw, dErr := zstdDecoder.DecodeAll(z, make([]byte, 0, m.Chunks[id].Size))
		if dErr != nil {
			return fmt.Errorf("chunk %s: %w", id, dErr)
		}
		if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != id || int64(len(raw)) != m.Chunks[id].Size {
			// Its compressed bytes matched the manifest, so the manifest itself
			// is wrong: fetching it again would give the same bytes.
			return fmt.Errorf("chunk %s decompresses to bytes that are not its name; the pushed manifest is wrong", id)
		}
		off := int64(i) * m.ChunkSize
		for b := 0; b < len(raw); b += goldenHoleBlock {
			blk := raw[b:min(b+goldenHoleBlock, len(raw))]
			if allZero(blk) {
				continue
			}
			if _, wErr := out.WriteAt(blk, off+int64(b)); wErr != nil {
				return wErr
			}
		}
		return nil
	})
	if err != nil {
		_ = out.Close() // already failing
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close() // already failing
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(p, fs.FileMode(f.Mode)&fs.ModePerm)
}

// forEach runs fn on every item, at most par at once, and returns the first
// error. Items not yet started when one fails are skipped.
func forEach[T any](items []T, par int, fn func(T) error) error {
	sem := make(chan struct{}, max(par, 1))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for _, it := range items {
		mu.Lock()
		stop := first != nil
		mu.Unlock()
		if stop {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(it T) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(it); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}(it)
	}
	wg.Wait()
	return first
}

// writeFileSynced writes a file whole under a temporary name, flushes it and
// renames it into place, so a reader never sees half of it.
func writeFileSynced(p string, b []byte) error {
	tmp := p + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // under our own directory
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close() // already failing
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close() // already failing
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// GoldenPullRemoval is what vm-golden-pull -delete would remove.
type GoldenPullRemoval struct {
	Dir   string
	Bytes int64 // allocated, so a sparse disk counts what it really holds
	Found bool
}

// InspectGoldenPull reports what is in a pull directory.
func InspectGoldenPull(dir string) (GoldenPullRemoval, error) {
	r := GoldenPullRemoval{Dir: dir}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r.Found = true
		if d.Type().IsRegular() {
			if n, ok := diskUsage(p); ok {
				r.Bytes += n
			}
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return GoldenPullRemoval{Dir: dir}, nil
	}
	return r, err
}

// GoldenPullDelete removes a pull directory. Nothing there is success.
func GoldenPullDelete(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is still there after removing it", Home(dir))
	}
	return nil
}

// GoldenCacheRemoval is what vm-golden-push -delete would do in the bucket.
type GoldenCacheRemoval struct {
	ID          string // the manifest to remove; "" when there is nothing
	Created     time.Time
	Chunks      []string // chunks no remaining manifest references
	Bytes       int64    // their total size
	NewLatest   string   // what latest will name afterwards; "" removes it
	MovesLatest bool
	Remaining   int // manifests left afterwards
}

// InspectGoldenCacheRemoval works out what removing manifest id (latest's
// when empty) from the bucket means: the manifest, every chunk no other
// manifest references, including those an interrupted push left, and where
// latest points afterwards. It changes nothing.
func InspectGoldenCacheRemoval(ctx context.Context, r R2Config, id string) (GoldenCacheRemoval, error) {
	var rm GoldenCacheRemoval
	cl := r.client()
	target, body, err := resolveManifest(ctx, cl, r.Bucket, id)
	if err != nil {
		return rm, err
	}
	latest := ""
	if b, lErr := getBytes(ctx, cl, r.Bucket, goldenLatestKey); lErr == nil {
		latest = strings.TrimSpace(string(b))
	} else if !isNotFound(lErr) {
		return rm, lErr
	}

	keys, err := listKeys(ctx, cl, r.Bucket, goldenPrefix+"manifests/")
	if err != nil {
		return rm, err
	}
	referenced := map[string]bool{}
	var newest time.Time
	for k := range keys {
		mid := strings.TrimSuffix(strings.TrimPrefix(k, goldenPrefix+"manifests/"), ".json")
		if mid == target && body != nil {
			continue
		}
		_, b, gErr := resolveManifest(ctx, cl, r.Bucket, mid)
		if gErr != nil {
			return rm, fmt.Errorf("another manifest, %s, cannot be read, so which chunks it needs is unknown: %w", mid, gErr)
		}
		var m cacheManifest
		if b == nil || json.Unmarshal(b, &m) != nil {
			return rm, fmt.Errorf("another manifest, %s, cannot be read, so which chunks it needs is unknown", mid)
		}
		rm.Remaining++
		for c := range m.Chunks {
			referenced[c] = true
		}
		if m.Created.After(newest) {
			newest, rm.NewLatest = m.Created, mid
		}
	}
	if body != nil {
		var m cacheManifest
		if err := json.Unmarshal(body, &m); err == nil {
			rm.Created = m.Created
		}
		rm.ID = target
		rm.MovesLatest = latest == target || latest == ""
	}

	chunks, err := listKeys(ctx, cl, r.Bucket, goldenPrefix+"chunks/")
	if err != nil {
		return rm, err
	}
	for k, size := range chunks {
		cid := strings.TrimSuffix(strings.TrimPrefix(k, goldenPrefix+"chunks/"), ".zst")
		if !referenced[cid] {
			rm.Chunks = append(rm.Chunks, cid)
			rm.Bytes += size
		}
	}
	sort.Strings(rm.Chunks)
	if rm.ID == "" && !rm.MovesLatest && latest != "" {
		// Nothing to remove, and latest names a manifest that is still there.
		rm.NewLatest = latest
	}
	return rm, nil
}

// GoldenCacheDelete carries out a removal: the manifest, then latest, then
// the chunks, so a failure part way leaves chunks nobody names, which the
// next removal collects, never a manifest naming chunks that are gone.
//
// Do not run it while a push to the same bucket is under way elsewhere: that
// push's chunks are unreferenced until its manifest is written.
func GoldenCacheDelete(ctx context.Context, r R2Config, rm GoldenCacheRemoval, say func(string, ...any)) error {
	cl := r.client()
	del := func(key string) error {
		if _, err := cl.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.Bucket), Key: aws.String(key)}); err != nil {
			return fmt.Errorf("deleting %s: %w", key, err)
		}
		return nil
	}
	if rm.ID != "" {
		if err := del(goldenManifestKey(rm.ID)); err != nil {
			return err
		}
		say("          removed %s", goldenManifestKey(rm.ID))
	}
	if rm.MovesLatest {
		if rm.NewLatest != "" {
			if err := putBytes(ctx, cl, r.Bucket, goldenLatestKey, []byte(rm.NewLatest+"\n"), nil); err != nil {
				return err
			}
			say("          latest -> %s", rm.NewLatest)
		} else {
			if err := del(goldenLatestKey); err != nil {
				return err
			}
			say("          removed %s", goldenLatestKey)
		}
	}
	for _, c := range rm.Chunks {
		if err := del(goldenChunkKey(c)); err != nil {
			return err
		}
	}
	if len(rm.Chunks) > 0 {
		say("          removed %d chunks, %s", len(rm.Chunks), HumanBytes(rm.Bytes))
	}
	// Checked, not assumed: a delete that answered 204 and left the object
	// would otherwise be reported as done.
	if rm.ID != "" {
		_, b, err := resolveManifest(ctx, cl, r.Bucket, rm.ID)
		if err != nil {
			return err
		}
		if b != nil {
			return fmt.Errorf("manifest %s is still in the bucket after deleting it", rm.ID)
		}
	}
	return nil
}

// listKeys lists every object under prefix, with its size.
func listKeys(ctx context.Context, cl *s3.Client, bucket, prefix string) (map[string]int64, error) {
	out := map[string]int64{}
	p := s3.NewListObjectsV2Paginator(cl, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			out[aws.ToString(o.Key)] = aws.ToInt64(o.Size)
		}
	}
	return out, nil
}
