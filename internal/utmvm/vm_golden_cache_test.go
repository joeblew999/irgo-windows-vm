package utmvm

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // an ETag, as S3 makes one, not a security property
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeR2 is R2 in-process: the few S3 calls the cache makes (PUT, GET with
// Range, HEAD, DELETE, ListObjectsV2), path-style, and the two Cloudflare API
// calls that say whether the bucket is public. Signatures are not checked, but
// a request that carries none is refused, as R2 would.
type fakeR2 struct {
	t      *testing.T
	bucket string

	mu      sync.Mutex
	objects map[string]fakeObject

	managedOn bool // the r2.dev development URL
	customOn  bool // one custom domain, enabled
	apiFail   bool // the API refuses the token

	puts, gets map[string]int
	ranges     map[string][]string // the Range header of each GET, by key
	abortOnce  map[string]bool     // cut the next GET of this key off half way
	s3Calls    int
}

type fakeObject struct {
	body []byte
	meta map[string]string
}

func newFakeR2(t *testing.T) (*fakeR2, R2Config) {
	f := &fakeR2{
		t: t, bucket: "bkt",
		objects: map[string]fakeObject{},
		puts:    map[string]int{}, gets: map[string]int{},
		ranges: map[string][]string{}, abortOnce: map[string]bool{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, R2Config{
		AccountID: "acct", Bucket: f.bucket,
		AccessKeyID: "AKID", SecretAccessKey: "secret", APIToken: "token",
		s3Endpoint: srv.URL, apiBase: srv.URL + "/client/v4",
	}
}

func (f *fakeR2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/client/v4/") {
		f.api(w, r)
		return
	}
	f.mu.Lock()
	f.s3Calls++
	f.mu.Unlock()
	if r.Header.Get("Authorization") == "" && r.URL.Query().Get("X-Amz-Signature") == "" {
		http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != f.bucket {
		http.Error(w, "<Error><Code>NoSuchBucket</Code></Error>", http.StatusNotFound)
		return
	}
	if key == "" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		f.list(w, r.URL.Query().Get("prefix"))
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		meta := map[string]string{}
		for k, v := range r.Header {
			if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-amz-meta-") {
				meta[strings.TrimPrefix(lk, "x-amz-meta-")] = v[0]
			}
		}
		f.mu.Lock()
		f.objects[key] = fakeObject{body, meta}
		f.puts[key]++
		f.mu.Unlock()
		w.Header().Set("ETag", etag(body))
	case http.MethodGet, http.MethodHead:
		f.mu.Lock()
		o, ok := f.objects[key]
		abort := false
		if r.Method == http.MethodGet {
			f.gets[key]++
			f.ranges[key] = append(f.ranges[key], r.Header.Get("Range"))
			abort = f.abortOnce[key]
			delete(f.abortOnce, key)
		}
		f.mu.Unlock()
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>no</Message></Error>")
			return
		}
		for k, v := range o.meta {
			w.Header().Set("X-Amz-Meta-"+k, v)
		}
		w.Header().Set("ETag", etag(o.body))
		if abort {
			// The connection dies half way through the body.
			w.Header().Set("Content-Length", fmt.Sprint(len(o.body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(o.body[:len(o.body)/2])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		http.ServeContent(w, r, key, time.Time{}, bytes.NewReader(o.body))
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func etag(b []byte) string {
	s := md5.Sum(b) //nolint:gosec // see the import
	return `"` + hex.EncodeToString(s[:]) + `"`
}

func (f *fakeR2) list(w http.ResponseWriter, prefix string) {
	type content struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	}
	type result struct {
		XMLName     xml.Name  `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
		Name        string    `xml:"Name"`
		Prefix      string    `xml:"Prefix"`
		KeyCount    int       `xml:"KeyCount"`
		MaxKeys     int       `xml:"MaxKeys"`
		IsTruncated bool      `xml:"IsTruncated"`
		Contents    []content `xml:"Contents"`
	}
	f.mu.Lock()
	res := result{Name: f.bucket, Prefix: prefix, MaxKeys: 1000}
	for k, o := range f.objects {
		if strings.HasPrefix(k, prefix) {
			res.Contents = append(res.Contents, content{k, int64(len(o.body))})
		}
	}
	f.mu.Unlock()
	sort.Slice(res.Contents, func(i, j int) bool { return res.Contents[i].Key < res.Contents[j].Key })
	res.KeyCount = len(res.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

func (f *fakeR2) api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	managed, custom, fail := f.managedOn, f.customOn, f.apiFail
	f.mu.Unlock()
	if fail || r.Header.Get("Authorization") != "Bearer token" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"messages":[],"result":null}`)
		return
	}
	base := "/client/v4/accounts/acct/r2/buckets/" + f.bucket + "/domains/"
	var result any
	switch r.URL.Path {
	case base + "managed":
		result = map[string]any{"bucketId": "b1", "domain": "pub-b1.r2.dev", "enabled": managed}
	case base + "custom":
		domains := []map[string]any{
			{"domain": "off.example.com", "enabled": false, "status": map[string]string{"ownership": "active", "ssl": "active"}},
		}
		if custom {
			domains = append(domains, map[string]any{"domain": "golden.example.com", "enabled": true,
				"status": map[string]string{"ownership": "active", "ssl": "active"}})
		}
		result = map[string]any{"domains": domains}
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":7003,"message":"No route"}],"messages":[],"result":null}`)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result})
}

func (f *fakeR2) chunkPuts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, c := range f.puts {
		if strings.HasPrefix(k, goldenPrefix+"chunks/") {
			n += c
		}
	}
	return n
}

func (f *fakeR2) keys(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// testChunk keeps the tests quick: 1 MiB regions, not 64 MiB. The disk is
// still 64 MiB, because APFS keeps a file of 8 MiB fully allocated whatever
// its holes (measured 30 Sep 2026: 8 MiB with 2 MiB written allocates 8 MiB;
// 64 MiB with 2 MiB written allocates 2 MiB), and the holes are under test.
const testChunk = 1 << 20

// makeBundle writes a bundle like the golden image's: a sparse disk.img with
// holes, a region repeated, a zero block inside a written region and a short
// last region, plus two small files.
func makeBundle(t *testing.T, root string) string {
	t.Helper()
	b := filepath.Join(root, "irgo-golden.utm")
	data := filepath.Join(b, "Data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	random := func(n int) []byte {
		p := make([]byte, n)
		for i := range p {
			p[i] = byte(rng.Uint32())
		}
		return p
	}
	disk := filepath.Join(data, "disk.img")
	f, err := os.Create(disk)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(64*testChunk + 1000)
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	r0 := random(testChunk)
	r5 := random(testChunk)
	copy(r5[goldenHoleBlock:2*goldenHoleBlock], make([]byte, goldenHoleBlock))
	for off, p := range map[int64][]byte{0: r0, 3 * testChunk: r0, 5 * testChunk: r5, 64 * testChunk: random(1000)} {
		if _, err := f.WriteAt(p, off); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "efi_vars.fd"), random(5000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "config.plist"), []byte("<plist/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func testSay(t *testing.T) func(string, ...any) {
	return func(f string, a ...any) { t.Logf(f, a...) }
}

func pushOpts(r R2Config, bundle, golden string) GoldenPushOptions {
	return GoldenPushOptions{R2: r, Bundle: bundle, Golden: golden, ToolVersion: "test", Parallel: 3, chunkSize: testChunk}
}

// sameTree fails unless every file under want is under got with the same
// bytes and permissions, and got has nothing else.
func sameTree(t *testing.T, want, got string) {
	t.Helper()
	seen := 0
	err := filepath.Walk(want, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(want, p)
		seen++
		a, _ := os.ReadFile(p)
		b, bErr := os.ReadFile(filepath.Join(got, rel))
		if bErr != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs after the round trip (%v)", rel, bErr)
		}
		gi, sErr := os.Stat(filepath.Join(got, rel))
		if sErr == nil && gi.Mode().Perm() != fi.Mode().Perm() {
			t.Errorf("%s is %v, was %v", rel, gi.Mode().Perm(), fi.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	_ = filepath.Walk(got, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	if n != seen {
		t.Errorf("the pulled bundle has %d files, the pushed one %d", n, seen)
	}
}

// A bundle pushed and pulled comes back byte for byte, with its golden.json,
// its holes still holes, and its repeated region stored once.
//
// Negative controls, run by hand: writing zero regions as well (treat "" as a
// chunk of zeros) fills 61 MiB and the allocation check fails; writing each
// region at i*len(raw) instead of i*ChunkSize corrupts the short last region
// and the pull refuses the file. Skipping the zero-block check inside a
// written region still passes: APFS allocation is too coarse to see 64 KiB.
func TestGoldenCacheRoundTrip(t *testing.T) {
	fake, r := newFakeR2(t)
	src := t.TempDir()
	bundle := makeBundle(t, src)
	golden := filepath.Join(src, "golden.json")
	if err := os.WriteFile(golden, []byte(`{"windows":"26100.4061","webview2":"154.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	pushed, err := GoldenPush(context.Background(), pushOpts(r, bundle, golden), testSay(t))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	// disk.img: regions 0 and 3 are one chunk, 5, the short 8th; efi_vars.fd
	// and config.plist one each.
	if pushed.Uploaded != 5 || fake.chunkPuts() != 5 {
		t.Errorf("uploaded %d chunks (%d PUTs), want 5: the repeated region stored once, holes not at all",
			pushed.Uploaded, fake.chunkPuts())
	}

	dir := filepath.Join(t.TempDir(), "pull")
	pulled, err := GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: dir, Parallel: 3}, testSay(t))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pulled.ID != pushed.ID {
		t.Errorf("pulled manifest %s, pushed %s", pulled.ID, pushed.ID)
	}
	sameTree(t, bundle, pulled.Bundle)
	if g, err := os.ReadFile(pulled.Golden); err != nil || !strings.Contains(string(g), "26100.4061") {
		t.Errorf("golden.json did not come with it: %q %v", g, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".parts")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the downloaded chunks were left behind: %v", err)
	}

	disk := filepath.Join(pulled.Bundle, "Data", "disk.img")
	if alloc, ok := diskUsage(disk); ok {
		// Three regions and 1000 bytes hold data; the other 61 MiB must be
		// holes. APFS allocated 6 MiB for those ~3 MiB (measured 1 Oct 2026),
		// so the bound is loose, and it does not see the 64 KiB zero block
		// inside region 5: that the block skip saves space is not proven here.
		if fi, _ := os.Stat(disk); alloc > 8*testChunk {
			t.Errorf("disk.img has %d of %d bytes allocated: the holes were written", alloc, fi.Size())
		}
	}

	// Running it again finds it done, and fetches nothing.
	before := fake.gets[goldenChunkKey(pushed.ID)]
	again, err := GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: dir, Parallel: 3}, testSay(t))
	if err != nil || !again.Skipped || fake.gets[goldenChunkKey(pushed.ID)] != before {
		t.Errorf("a second pull: skipped=%v err=%v; want it to find the pull done", again.Skipped, err)
	}
}

// A second push after one region changed uploads that one chunk and nothing
// else, and moves latest to the new manifest.
//
// Negative control, run by hand: make ensure skip its HeadObject (treat every
// chunk as missing) and the second push uploads all five again.
func TestGoldenPushUploadsOnlyWhatChanged(t *testing.T) {
	fake, r := newFakeR2(t)
	bundle := makeBundle(t, t.TempDir())
	first, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t))
	if err != nil {
		t.Fatal(err)
	}
	puts := fake.chunkPuts()

	disk := filepath.Join(bundle, "Data", "disk.img")
	f, err := os.OpenFile(disk, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("changed"), 6*testChunk+17); err != nil { // a hole becomes data
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.chunkPuts() - puts; got != 1 || second.Uploaded != 1 || second.Reused != 5 {
		t.Errorf("second push sent %d chunks (reported %d sent, %d reused), want 1 sent and 5 reused",
			got, second.Uploaded, second.Reused)
	}
	if second.ID == first.ID {
		t.Error("the changed bundle got the same manifest id")
	}
	if latest := string(fake.objects[goldenLatestKey].body); strings.TrimSpace(latest) != second.ID {
		t.Errorf("latest is %q, want %s", latest, second.ID)
	}
}

// A chunk altered in the bucket fails its SHA-256, is fetched once more, and
// fails again; nothing is put in place.
//
// Negative control, run by hand: pass digest{} instead of sha256Digest in
// fetchChunk; the bad chunk is then fetched once and fails later, in zstd,
// and both the message and the fetch-count checks fail.
func TestGoldenPullRejectsACorruptChunk(t *testing.T) {
	fake, r := newFakeR2(t)
	bundle := makeBundle(t, t.TempDir())
	if _, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t)); err != nil {
		t.Fatal(err)
	}
	victim := fake.keys(goldenPrefix + "chunks/")[0]
	fake.mu.Lock()
	o := fake.objects[victim]
	o.body = append([]byte{}, o.body...)
	o.body[len(o.body)/2] ^= 0xff
	fake.objects[victim] = o
	fake.mu.Unlock()

	dir := filepath.Join(t.TempDir(), "pull")
	_, err := GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: dir, Parallel: 1}, testSay(t))
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("pull of a corrupted chunk gave %v, want a sha256 mismatch", err)
	}
	if n := fake.gets[victim]; n != 2 {
		t.Errorf("the corrupt chunk was fetched %d times, want 2 (once more before giving up)", n)
	}
	if _, sErr := os.Stat(filepath.Join(dir, "irgo-golden.utm")); !errors.Is(sErr, os.ErrNotExist) {
		t.Error("a bundle was put in place from a corrupt chunk")
	}
	if strings.Contains(err.Error(), "X-Amz-Signature") {
		t.Errorf("the error carries a presigned URL, a credential: %v", err)
	}
}

// A manifest altered in the bucket no longer hashes to its name and is refused
// before anything is downloaded.
//
// Negative control, run by hand: remove the hash compare in resolveManifest
// and the pull proceeds with the altered manifest.
func TestGoldenPullRejectsAnAlteredManifest(t *testing.T) {
	fake, r := newFakeR2(t)
	bundle := makeBundle(t, t.TempDir())
	res, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t))
	if err != nil {
		t.Fatal(err)
	}
	key := goldenManifestKey(res.ID)
	fake.mu.Lock()
	o := fake.objects[key]
	o.body = bytes.Replace(o.body, []byte(`"tool_version": "test"`), []byte(`"tool_version": "evil"`), 1)
	fake.objects[key] = o
	fake.mu.Unlock()

	_, err = GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: t.TempDir(), Parallel: 1}, testSay(t))
	if err == nil || !strings.Contains(err.Error(), "does not hash to its name") {
		t.Fatalf("an altered manifest gave %v", err)
	}
	for k := range fake.gets {
		if strings.HasPrefix(k, goldenPrefix+"chunks/") {
			t.Errorf("chunk %s was downloaded for a manifest that failed its hash", k)
		}
	}
}

// A file whose rebuilt bytes do not match the manifest's whole-file hash is
// refused even when every chunk is right: the tree hash is checked by reading
// the file back from disk.
//
// Negative control, run by hand: skip the got != f.SHA256 compare in
// GoldenPull and this pull succeeds.
func TestGoldenPullChecksTheWholeFile(t *testing.T) {
	fake, r := newFakeR2(t)
	bundle := makeBundle(t, t.TempDir())
	res, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t))
	if err != nil {
		t.Fatal(err)
	}
	var m cacheManifest
	if err := json.Unmarshal(fake.objects[goldenManifestKey(res.ID)].body, &m); err != nil {
		t.Fatal(err)
	}
	m.Files[0].SHA256 = strings.Repeat("c", 64)
	body, _ := json.MarshalIndent(m, "", " ")
	sum := sha256Hex(body)
	fake.mu.Lock()
	fake.objects[goldenManifestKey(sum)] = fakeObject{body: body}
	fake.mu.Unlock()

	dir := filepath.Join(t.TempDir(), "pull")
	_, err = GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: dir, ID: sum, Parallel: 2}, testSay(t))
	if err == nil || !strings.Contains(err.Error(), "tree hash") {
		t.Fatalf("a wrong whole-file hash gave %v", err)
	}
	if _, sErr := os.Stat(filepath.Join(dir, m.Bundle)); !errors.Is(sErr, os.ErrNotExist) {
		t.Error("a bundle that failed its file hash was put in place")
	}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// A download cut off mid-chunk fails, keeps what arrived, and the next pull
// asks for the rest with a Range rather than starting that chunk again.
//
// Negative control, run by hand: in isoDownload, delete the .part on error;
// the second pull then asks for the whole chunk and the Range check fails.
func TestGoldenPullResumesAnInterruptedDownload(t *testing.T) {
	fake, r := newFakeR2(t)
	bundle := makeBundle(t, t.TempDir())
	if _, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t)); err != nil {
		t.Fatal(err)
	}
	victim := fake.keys(goldenPrefix + "chunks/")[0]
	fake.mu.Lock()
	fake.abortOnce[victim] = true
	half := len(fake.objects[victim].body) / 2
	fake.mu.Unlock()

	dir := filepath.Join(t.TempDir(), "pull")
	if _, err := GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: dir, Parallel: 1}, testSay(t)); err == nil {
		t.Fatal("a pull whose connection died reported success")
	}
	id := strings.TrimSuffix(strings.TrimPrefix(victim, goldenPrefix+"chunks/"), ".zst")
	if fi, err := os.Stat(filepath.Join(dir, ".parts", id+".zst.part")); err != nil || fi.Size() != int64(half) {
		t.Fatalf("the half that arrived was not kept: %v", err)
	}
	res, err := GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: dir, Parallel: 1}, testSay(t))
	if err != nil {
		t.Fatalf("the second pull: %v", err)
	}
	sameTree(t, bundle, res.Bundle)
	rs := fake.ranges[victim]
	if want := fmt.Sprintf("bytes=%d-", half); len(rs) != 2 || rs[1] != want {
		t.Errorf("the chunk's GETs asked for ranges %q, want the second to be %q", rs, want)
	}
	for k, n := range fake.gets {
		if strings.HasPrefix(k, goldenPrefix+"chunks/") && k != victim && n != 1 {
			t.Errorf("chunk %s was fetched %d times; a chunk that arrived is not fetched again", k, n)
		}
	}
}

// A bucket that is public in any way, or whose settings cannot be read, is
// refused for push and for pull, before a single object is touched.
//
// Negative control, run by hand: have requirePrivate return nil on
// exposureUnknown and the "API refuses the token" cases fail.
func TestGoldenRefusesAPublicBucket(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeR2)
		want error
	}{
		{"r2.dev URL on", func(f *fakeR2) { f.managedOn = true }, errBucketPublic},
		{"a custom domain on", func(f *fakeR2) { f.customOn = true }, errBucketPublic},
		{"the API refuses the token", func(f *fakeR2) { f.apiFail = true }, errBucketExposureUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, r := newFakeR2(t)
			tc.set(fake)
			bundle := makeBundle(t, t.TempDir())
			if _, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t)); !errors.Is(err, tc.want) {
				t.Errorf("push gave %v, want %v", err, tc.want)
			}
			_, err := GoldenPull(context.Background(), GoldenPullOptions{R2: r, Dir: t.TempDir(), Parallel: 1}, testSay(t))
			if !errors.Is(err, tc.want) {
				t.Errorf("pull gave %v, want %v", err, tc.want)
			}
			if fake.s3Calls != 0 {
				t.Errorf("%d S3 requests were made to a bucket that was refused", fake.s3Calls)
			}
		})
	}
	// And the same fake with nothing public is accepted, so the refusals above
	// are about exposure and not about the fake.
	fake, r := newFakeR2(t)
	if e, why := r.bucketExposure(context.Background()); e != exposurePrivate {
		t.Errorf("a private bucket read as %v: %v", e, why)
	}
	_ = fake
}

// Removing a manifest removes the chunks only it used, keeps those another
// manifest shares, moves latest to what remains, and removing again, or from
// an empty bucket, is success.
//
// Negative control, run by hand: collect every chunk instead of only the
// unreferenced ones, and the pull of the remaining manifest fails.
func TestGoldenCacheDelete(t *testing.T) {
	fake, r := newFakeR2(t)
	bundle := makeBundle(t, t.TempDir())
	ctx := context.Background()
	first, err := GoldenPush(ctx, pushOpts(r, bundle, ""), testSay(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.plist"), []byte("<plist>2</plist>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // so the two manifests' dates differ
	second, err := GoldenPush(ctx, pushOpts(r, bundle, ""), testSay(t))
	if err != nil {
		t.Fatal(err)
	}

	rm, err := InspectGoldenCacheRemoval(ctx, r, "") // latest: the second
	if err != nil {
		t.Fatal(err)
	}
	if rm.ID != second.ID || len(rm.Chunks) != 1 || rm.NewLatest != first.ID || !rm.MovesLatest {
		t.Fatalf("removal plan %+v: want the second manifest, its one own chunk, latest back to the first", rm)
	}
	if err := GoldenCacheDelete(ctx, r, rm, testSay(t)); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(fake.objects[goldenLatestKey].body)); got != first.ID {
		t.Errorf("latest is %s after removing the second, want %s", got, first.ID)
	}
	if _, err := GoldenPull(ctx, GoldenPullOptions{R2: r, Dir: filepath.Join(t.TempDir(), "p"), Parallel: 2}, testSay(t)); err != nil {
		t.Errorf("the remaining manifest no longer pulls: %v", err)
	}

	rm, err = InspectGoldenCacheRemoval(ctx, r, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := GoldenCacheDelete(ctx, r, rm, testSay(t)); err != nil {
		t.Fatal(err)
	}
	if left := fake.keys(goldenPrefix); len(left) != 0 {
		t.Errorf("objects left after removing every manifest: %v", left)
	}

	rm, err = InspectGoldenCacheRemoval(ctx, r, "")
	if err != nil || rm.ID != "" || len(rm.Chunks) != 0 {
		t.Fatalf("an empty bucket: %+v %v; want nothing to remove", rm, err)
	}
	if err := GoldenCacheDelete(ctx, r, rm, testSay(t)); err != nil {
		t.Errorf("removing nothing failed: %v", err)
	}
}

// A manifest that would write outside its directory, or does not add up, is
// refused before anything is downloaded.
//
// Negative control, run by hand: drop the "../" test in validate and the
// traversal case passes.
func TestGoldenManifestValidate(t *testing.T) {
	id := strings.Repeat("a", 64)
	good := func() cacheManifest {
		return cacheManifest{
			Format: goldenCacheFormat, Bundle: "g.utm", ChunkSize: 10,
			Files:  []cacheFile{{Path: "Data/disk.img", Size: 15, SHA256: id, Regions: []string{id, ""}}},
			Chunks: map[string]cacheChunk{id: {Size: 10, ZSize: 4, ZSHA256: id}},
		}
	}
	if m := good(); m.validate() != nil {
		t.Fatalf("a good manifest was refused: %v", m.validate())
	}
	for name, breakIt := range map[string]func(*cacheManifest){
		"traversal":         func(m *cacheManifest) { m.Files[0].Path = "../escape" },
		"absolute":          func(m *cacheManifest) { m.Files[0].Path = "/etc/passwd" },
		"unclean":           func(m *cacheManifest) { m.Files[0].Path = "Data/../../x" },
		"bundle with slash": func(m *cacheManifest) { m.Bundle = "a/b" },
		"region count":      func(m *cacheManifest) { m.Files[0].Size = 25 },
		"unknown chunk":     func(m *cacheManifest) { m.Files[0].Regions[0] = strings.Repeat("b", 64) },
		"short last region": func(m *cacheManifest) { m.Files[0].Regions = []string{"", id} },
		"format":            func(m *cacheManifest) { m.Format = 2 },
	} {
		m := good()
		breakIt(&m)
		if m.validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Every missing variable is named at once.
//
// Negative control, run by hand: return on the first missing variable and the
// count check fails.
func TestR2ConfigFromEnvNamesEveryMissingVariable(t *testing.T) {
	for _, e := range r2Env {
		t.Setenv(e.name, "")
	}
	t.Setenv("IRGO_R2_BUCKET", "b")
	_, err := R2ConfigFromEnv()
	if !errors.Is(err, ErrR2NotConfigured) {
		t.Fatalf("got %v", err)
	}
	for _, e := range r2Env {
		if e.name != "IRGO_R2_BUCKET" && !strings.Contains(err.Error(), e.name) {
			t.Errorf("%s is missing and not named: %v", e.name, err)
		}
	}
	for _, e := range r2Env {
		t.Setenv(e.name, "x")
	}
	if _, err := R2ConfigFromEnv(); err != nil {
		t.Errorf("a full environment was refused: %v", err)
	}
}
