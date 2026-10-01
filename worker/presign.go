package main

// AWS Signature Version 4 query-string presigning, for one GET or HEAD on R2's
// S3 endpoint. Hand-written rather than the AWS SDK: the SDK would multiply the
// Wasm binary, and the algorithm is one canonical request and four HMACs. The
// test checks it against AWS's own published example.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

type presignInput struct {
	Method, Host, Path           string // Path is unescaped: /bucket/key
	Region                       string
	AccessKeyID, SecretAccessKey string
	Time                         time.Time
	Expires                      time.Duration
}

// presign returns the https URL for in, valid for in.Expires from in.Time.
func presign(in presignInput) string {
	t := in.Time.UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	scope := day + "/" + in.Region + "/s3/aws4_request"

	q := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    in.AccessKeyID + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       fmt.Sprint(int(in.Expires / time.Second)),
		"X-Amz-SignedHeaders": "host",
	}
	names := make([]string, 0, len(q))
	for k := range q {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = awsEscape(k, false) + "=" + awsEscape(q[k], false)
	}
	query := strings.Join(parts, "&")
	path := awsEscape(in.Path, true)

	canonical := strings.Join([]string{
		in.Method, path, query, "host:" + in.Host + "\n", "host", "UNSIGNED-PAYLOAD",
	}, "\n")
	ch := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch[:])

	k := hmacSHA256([]byte("AWS4"+in.SecretAccessKey), day)
	k = hmacSHA256(k, in.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))

	return "https://" + in.Host + path + "?" + query + "&X-Amz-Signature=" + sig
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// awsEscape is SigV4's URI encoding: every byte but A-Z a-z 0-9 - _ . ~ as
// %XX in upper case, and '/' kept only in a path.
func awsEscape(s string, path bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~', path && c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
