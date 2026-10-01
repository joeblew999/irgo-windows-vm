package utmvm

// The owner's private Cloudflare R2 bucket that caches the golden image, so a
// machine without one downloads it instead of installing Windows. This file is
// the bucket: credentials, the S3 client, and the refusal to use a bucket that
// anybody could read. vm_golden_cache.go is what is stored in it.
//
// Private by construction, because the Windows licence forbids redistribution
// (.plans/2026-09-30_1700_vm-golden-image.md, "Legal"): every push and pull
// first asks Cloudflare whether the bucket is reachable without credentials,
// and refuses on yes and on cannot tell.

import (
	"context"
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
)

// R2Config is where the cache is and the credentials for it. It comes from the
// environment only (R2ConfigFromEnv); nothing is baked into the binary.
type R2Config struct {
	AccountID       string
	Bucket          string
	AccessKeyID     string // S3 credentials: read-only is enough to pull
	SecretAccessKey string
	APIToken        string // Cloudflare API token that can read the bucket's settings

	// Tests point these at an in-process fake. Empty means Cloudflare.
	s3Endpoint string
	apiBase    string
}

// r2Env is every variable R2ConfigFromEnv reads, in the order it names them.
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
// every variable that is missing rather than the first.
func R2ConfigFromEnv() (R2Config, error) {
	var c R2Config
	var missing []string
	for _, e := range r2Env {
		v := strings.TrimSpace(os.Getenv(e.name))
		if v == "" {
			missing = append(missing, e.name)
		}
		*e.field(&c) = v
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("%w: %s not set.\n"+
			"  Put them in .env.r2 at the repository root (gitignored, loaded by mise);\n"+
			"  docs/DEVELOPMENT.md, \"The private R2 cache\", says how to get each one",
			ErrR2NotConfigured, strings.Join(missing, ", "))
	}
	return c, nil
}

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
func (c R2Config) Where() string { return c.endpoint() + "/" + c.Bucket }

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
func (c R2Config) requirePrivate(ctx context.Context, say func(string, ...any)) error {
	say("          is %s private? asking Cloudflare", c.Where())
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
