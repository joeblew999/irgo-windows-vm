package utmvm

// The owner's private Cloudflare R2 bucket that caches the golden image, so a
// machine without one downloads it instead of installing Windows. This file is
// the bucket: settings, the two ways to reach it (goldenStore), and the
// refusal to use a bucket that anybody could read. vm_golden_cache.go is what
// is stored in it.
//
// Two transports, one format:
//
//   - the project's Worker (vm_golden_worker.go), which has the bucket bound
//     and needs no S3 keys, when IRGO_GOLDEN_URL is set;
//   - R2's S3 API directly, with an access key, otherwise.
//
// Private by construction, because the Windows licence forbids redistribution
// (.plans/2026-09-30_1700_vm-golden-image.md, "Legal"): every push and pull
// first asks Cloudflare whether the bucket is reachable without credentials,
// and refuses on yes and on cannot tell.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/joeblew999/irgo-windows-vm/internal/workerclient"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

// R2Config is where the cache is and the credentials for it. It comes from the
// environment only (R2ConfigFromEnv); nothing is baked into the binary.
type R2Config struct {
	AccountID       string
	Bucket          string
	AccessKeyID     string // S3 credentials: read-only is enough to pull
	SecretAccessKey string
	APIToken        string // Cloudflare API token that can read the bucket's settings

	// The Worker transport, used when WorkerURL is set: the Worker's origin,
	// its read token, and its write token (push and delete only).
	WorkerURL string
	Token     string
	PushToken string

	// Tests point these at an in-process fake. Empty means Cloudflare.
	s3Endpoint string
	apiBase    string
}

// r2Env is every variable the S3 transport needs, in the order they are named.
var r2Env = []struct {
	name  string
	field func(*R2Config) *string
}{
	{"IRGO_R2_ACCOUNT_ID", func(c *R2Config) *string { return &c.AccountID }},
	{"IRGO_R2_BUCKET", func(c *R2Config) *string { return &c.Bucket }},
	{"IRGO_R2_ACCESS_KEY_ID", func(c *R2Config) *string { return &c.AccessKeyID }},
	{"IRGO_R2_SECRET_ACCESS_KEY", func(c *R2Config) *string { return &c.SecretAccessKey }},
	{"IRGO_R2_API_TOKEN", func(c *R2Config) *string { return &c.APIToken }},
}

// ErrR2NotConfigured is an environment that does not name a bucket and its
// credentials.
var ErrR2NotConfigured = errors.New("the R2 cache is not configured")

// R2ConfigFromEnv reads the cache's settings from the environment, and names
// every variable that is missing rather than the first. write is whether the
// caller will change the bucket (push, delete), which through the Worker
// needs its write token as well.
//
// IRGO_GOLDEN_URL selects the Worker. Through it IRGO_R2_API_TOKEN is
// optional: with it the bucket's public access is still checked, which needs
// IRGO_R2_ACCOUNT_ID and IRGO_R2_BUCKET too.
func R2ConfigFromEnv(write bool) (R2Config, error) {
	var c R2Config
	var missing []string
	need := func(name string, into *string) {
		*into = strings.TrimSpace(os.Getenv(name))
		if *into == "" {
			missing = append(missing, name)
		}
	}
	if u := strings.TrimSpace(os.Getenv("IRGO_GOLDEN_URL")); u != "" {
		c.WorkerURL = strings.TrimRight(u, "/")
		if err := checkWorkerURL(c.WorkerURL); err != nil {
			return c, fmt.Errorf("%w: IRGO_GOLDEN_URL: %v", ErrR2NotConfigured, err)
		}
		need("IRGO_GOLDEN_TOKEN", &c.Token)
		if write {
			need("IRGO_GOLDEN_PUSH_TOKEN", &c.PushToken)
		}
		if c.APIToken = strings.TrimSpace(os.Getenv("IRGO_R2_API_TOKEN")); c.APIToken != "" {
			need("IRGO_R2_ACCOUNT_ID", &c.AccountID)
			need("IRGO_R2_BUCKET", &c.Bucket)
		}
	} else if strings.TrimSpace(os.Getenv("IRGO_GOLDEN_TOKEN")) != "" || strings.TrimSpace(os.Getenv("IRGO_GOLDEN_PUSH_TOKEN")) != "" {
		// A Worker token and no Worker: the URL is what is missing, not S3 keys.
		missing = append(missing, "IRGO_GOLDEN_URL")
	} else {
		for _, e := range r2Env {
			need(e.name, e.field(&c))
		}
	}
	if len(missing) > 0 {
		what := strings.Join(missing, ", ") + " not set"
		if !GoldenCacheEnvSet() {
			// Nothing at all: name the simple way first, not five S3 variables.
			what = "set IRGO_GOLDEN_URL and IRGO_GOLDEN_TOKEN (through the Worker), " +
				"or IRGO_R2_ACCOUNT_ID, IRGO_R2_BUCKET, IRGO_R2_ACCESS_KEY_ID, " +
				"IRGO_R2_SECRET_ACCESS_KEY and IRGO_R2_API_TOKEN (R2's S3 API)"
		}
		return c, fmt.Errorf("%w: %s.\n"+
			"  Export them in your shell; in a checkout, .env.r2 at the root is loaded by mise.\n"+
			"  How to get each one: %s",
			ErrR2NotConfigured, what, SiteURL+"using.html#setting-up-the-bucket")
	}
	return c, nil
}

// GoldenCacheEnvSet reports whether any of the private cache's variables is
// set, which is whether the user meant to use it: vm-create then pulls the
// golden image, or says what is missing, rather than quietly installing.
func GoldenCacheEnvSet() bool {
	names := []string{"IRGO_GOLDEN_URL", "IRGO_GOLDEN_TOKEN"}
	for _, e := range r2Env {
		names = append(names, e.name)
	}
	for _, n := range names {
		if strings.TrimSpace(os.Getenv(n)) != "" {
			return true
		}
	}
	return false
}

// checkWorkerURL is the client's: an origin, and the tokens only over https
// or to localhost.
func checkWorkerURL(s string) error { return workerclient.CheckOrigin(s) }

func (c R2Config) viaWorker() bool { return c.WorkerURL != "" }

func (c R2Config) endpoint() string {
	if c.s3Endpoint != "" {
		return c.s3Endpoint
	}
	return "https://" + c.AccountID + ".r2.cloudflarestorage.com"
}

func (c R2Config) api() string {
	if c.apiBase != "" {
		return c.apiBase
	}
	return "https://api.cloudflare.com/client/v4"
}

// Where names the bucket for a person, without any credential.
func (c R2Config) Where() string {
	if c.viaWorker() {
		return newWorkerStore(c).Where()
	}
	return c.endpoint() + "/" + c.Bucket
}

// goldenStore is the bucket as the cache uses it, whichever way it is reached.
type goldenStore interface {
	// head returns an object's size and the SHA-256 recorded when it was
	// stored ("" when none was); ok=false when there is no such object.
	head(ctx context.Context, key string) (size int64, sum string, ok bool, err error)
	// get returns a small object (a manifest, latest); ok=false when absent.
	get(ctx context.Context, key string) (b []byte, ok bool, err error)
	// put stores b, recording its SHA-256 beside it.
	put(ctx context.Context, key string, b []byte) error
	// del removes an object; nothing there is success.
	del(ctx context.Context, key string) error
	// list returns every key under prefix, with its size.
	list(ctx context.Context, prefix string) (map[string]int64, error)
	// fetch downloads an object to dest through isoDownload, which resumes
	// dest.part with Range and renames only once want matches.
	fetch(ctx context.Context, key, dest string, want digest) error
}

// store is the transport the settings select.
func (c R2Config) store() goldenStore {
	if c.viaWorker() {
		return newWorkerStore(c)
	}
	return s3Store{c: c, cl: c.client()}
}

// client is the S3 client for R2.
//
// Checksums only when an operation requires them: the SDK otherwise sends
// every PutObject as aws-chunked with a trailing CRC, and the chunk's own
// SHA-256 is already the integrity check that matters.
func (c R2Config) client() *s3.Client {
	return s3.New(s3.Options{
		Region:                     "auto", // R2 has no regions; "auto" is what it documents
		BaseEndpoint:               aws.String(c.endpoint()),
		Credentials:                credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""),
		UsePathStyle:               true,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// s3Store is the bucket through R2's S3 API.
type s3Store struct {
	c  R2Config
	cl *s3.Client
}

// metaSHA256 is the object metadata that holds its SHA-256
// (x-amz-meta-zsha256). The Worker reads and writes the same name, so either
// transport reads what the other stored.
const metaSHA256 = wire.MetaSHA256

func (s s3Store) head(ctx context.Context, key string) (int64, string, bool, error) {
	h, err := s.cl.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.c.Bucket), Key: aws.String(key)})
	if isNotFound(err) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return aws.ToInt64(h.ContentLength), h.Metadata[metaSHA256], true, nil
}

func (s s3Store) get(ctx context.Context, key string) ([]byte, bool, error) {
	out, err := s.cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.c.Bucket), Key: aws.String(key)})
	if isNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(out.Body, 64<<20))
	return b, err == nil, err
}

func (s s3Store) put(ctx context.Context, key string, b []byte) error {
	sum := sha256.Sum256(b)
	_, err := s.cl.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.c.Bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(b),
		ContentLength: aws.Int64(int64(len(b))),
		Metadata:      map[string]string{metaSHA256: hex.EncodeToString(sum[:])},
	})
	if err != nil {
		return fmt.Errorf("uploading %s: %w", key, err)
	}
	return nil
}

func (s s3Store) del(ctx context.Context, key string) error {
	if _, err := s.cl.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.c.Bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("deleting %s: %w", key, err)
	}
	return nil
}

func (s s3Store) list(ctx context.Context, prefix string) (map[string]int64, error) {
	out := map[string]int64{}
	p := s3.NewListObjectsV2Paginator(s.cl, &s3.ListObjectsV2Input{Bucket: aws.String(s.c.Bucket), Prefix: aws.String(prefix)})
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

// fetch downloads from a 30-minute presigned URL.
func (s s3Store) fetch(ctx context.Context, key, dest string, want digest) error {
	req, err := s3.NewPresignClient(s.cl).PresignGetObject(ctx,
		&s3.GetObjectInput{Bucket: aws.String(s.c.Bucket), Key: aws.String(key)}, s3.WithPresignExpires(30*time.Minute))
	if err != nil {
		return err
	}
	if err := isoDownload(req.URL, nil, dest, want, nil); err != nil {
		// The presigned URL is a credential for this object until it expires,
		// and errors are logged: name the key instead.
		return errors.New(strings.ReplaceAll(err.Error(), req.URL, key))
	}
	return nil
}

func isNotFound(err error) bool {
	var nf *types.NotFound
	var nk *types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nk)
}

// exposure is whether a bucket can be read without credentials.
type exposure int

const (
	exposureUnknown exposure = iota // cannot tell, which is refused like public
	exposurePrivate
	exposurePublic
)

func (e exposure) String() string {
	switch e {
	case exposurePrivate:
		return "private"
	case exposurePublic:
		return "PUBLIC"
	default:
		return "cannot tell"
	}
}

// errBucketPublic is a bucket anybody can read. errBucketExposureUnknown is one
// whose settings could not be read: cannot tell is not safe.
var (
	errBucketPublic          = errors.New("the bucket can be read without credentials")
	errBucketExposureUnknown = errors.New("cannot tell whether the bucket can be read without credentials")
)

// cfEnvelope is the Cloudflare API's wrapper around every result.
type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// bucketExposure asks Cloudflare whether the bucket is reachable without
// credentials. R2 has exactly two ways to be: the r2.dev development URL, and
// custom domains. Each is read from the API; the answer is private only when
// both were read and both are off. Any failure to read either is
// exposureUnknown, with why.
func (c R2Config) bucketExposure(ctx context.Context) (exposure, []string) {
	var why []string

	var managed struct {
		Domain  string `json:"domain"`
		Enabled *bool  `json:"enabled"`
	}
	if err := c.cfGet(ctx, "domains/managed", &managed); err != nil {
		return exposureUnknown, []string{"the r2.dev development URL: " + err.Error()}
	}
	if managed.Enabled == nil {
		return exposureUnknown, []string{"the r2.dev development URL: the API's answer has no \"enabled\""}
	}
	public := false
	if *managed.Enabled {
		public = true
		why = append(why, fmt.Sprintf("the r2.dev development URL is ON: https://%s", managed.Domain))
	} else {
		why = append(why, "the r2.dev development URL is off")
	}

	var custom struct {
		Domains *[]struct {
			Domain  string `json:"domain"`
			Enabled *bool  `json:"enabled"`
		} `json:"domains"`
	}
	if err := c.cfGet(ctx, "domains/custom", &custom); err != nil {
		return exposureUnknown, append(why, "custom domains: "+err.Error())
	}
	if custom.Domains == nil {
		return exposureUnknown, append(why, "custom domains: the API's answer has no \"domains\"")
	}
	on := 0
	for _, d := range *custom.Domains {
		switch {
		case d.Enabled == nil:
			return exposureUnknown, append(why, "custom domain "+d.Domain+": the API does not say whether it is enabled")
		case *d.Enabled:
			public = true
			on++
			why = append(why, "custom domain "+d.Domain+" is ON")
		}
	}
	if on == 0 {
		why = append(why, fmt.Sprintf("no custom domain is enabled (%d connected)", len(*custom.Domains)))
	}
	if public {
		return exposurePublic, why
	}
	return exposurePrivate, why
}

// cfGet reads one of the bucket's settings from the Cloudflare API.
func (c R2Config) cfGet(ctx context.Context, what string, into any) error {
	u := fmt.Sprintf("%s/accounts/%s/r2/buckets/%s/%s",
		c.api(), url.PathEscape(c.AccountID), url.PathEscape(c.Bucket), what)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var env cfEnvelope
	if jErr := json.Unmarshal(body, &env); jErr != nil {
		return fmt.Errorf("HTTP %s, not the API's JSON", resp.Status)
	}
	if resp.StatusCode != http.StatusOK || !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		hint := ""
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			hint = " (IRGO_R2_API_TOKEN needs Workers R2 Storage Read on the account)"
		}
		return fmt.Errorf("HTTP %s: %s%s", resp.Status, strings.Join(msgs, "; "), hint)
	}
	return json.Unmarshal(env.Result, into)
}

// requirePrivate refuses a bucket that is public or whose exposure cannot be
// read, and prints what was checked either way.
//
// Through the Worker without IRGO_R2_API_TOKEN there is nothing to ask, and
// it says so: the Worker's binding is not a public route and every request
// needs a token, but the bucket's own public routes go unchecked.
func (c R2Config) requirePrivate(ctx context.Context, say func(string, ...any)) error {
	if c.viaWorker() && c.APIToken == "" {
		say("          through the Worker %s, which has the bucket bound: a binding is", c.WorkerURL)
		say("          not a public route, and every request to it needs a token. The bucket's own")
		say("          public routes (r2.dev, custom domains) are NOT checked: set IRGO_R2_API_TOKEN,")
		say("          IRGO_R2_ACCOUNT_ID and IRGO_R2_BUCKET to check them as well")
		return nil
	}
	say("          is %s private? asking Cloudflare", c.endpoint()+"/"+c.Bucket)
	e, why := c.bucketExposure(ctx)
	for _, w := range why {
		say("            %s", w)
	}
	switch e {
	case exposurePrivate:
		say("          %s: nothing in it is reachable without credentials", e)
		return nil
	case exposurePublic:
		return fmt.Errorf("%w: %s.\n"+
			"  The Windows licence forbids passing this image to anyone. Turn public access off\n"+
			"  (R2 > the bucket > Settings: Public Development URL and Custom Domains) and run it again",
			errBucketPublic, strings.Join(why, "; "))
	default:
		return fmt.Errorf("%w: %s.\n"+
			"  Refusing: a bucket that cannot be shown private is treated as public",
			errBucketExposureUnknown, strings.Join(why, "; "))
	}
}

// licenceCondition is printed on every push and pull.
const licenceCondition = "licence: this cache is for your own licensed machines and CI only. " +
	"Every running clone needs its own Windows 11 Pro licence (§2d(iv)); never share the bucket or its credentials"
