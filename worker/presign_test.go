package main

import (
	"strings"
	"testing"
	"time"
)

// AWS's published presigned-URL example ("Authenticating Requests: Using Query
// Parameters", Amazon S3 API reference): its expected signature is AWS's, not
// one computed by this code.
//
// Negative control (run by hand, 1 Oct 2026): dropping "\n" from the
// "host:" line of the canonical request makes this fail with a different
// signature; restored.
func TestPresignMatchesAWSExample(t *testing.T) {
	u := presign(presignInput{
		Method: "GET", Host: "examplebucket.s3.amazonaws.com", Path: "/test.txt",
		Region: "us-east-1", AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Time:            time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC),
		Expires:         86400 * time.Second,
	})
	want := "https://examplebucket.s3.amazonaws.com/test.txt" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if u != want {
		t.Fatalf("presign:\n got %s\nwant %s", u, want)
	}
}

func TestAWSEscape(t *testing.T) {
	if got := awsEscape("/b/golden/a b+c", true); got != "/b/golden/a%20b%2Bc" {
		t.Fatalf("path: %s", got)
	}
	if got := awsEscape("a/b", false); !strings.Contains(got, "%2F") {
		t.Fatalf("query keeps '/': %s", got)
	}
}
