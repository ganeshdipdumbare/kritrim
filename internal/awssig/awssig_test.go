package awssig

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func sign(t *testing.T, body string, c Credentials) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.eu-west-1.amazonaws.com/model/m/converse", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if err := signAt(req, []byte(body), "bedrock", c, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	return req.Header.Get("Authorization")
}

func TestSign(t *testing.T) {
	c := Credentials{Region: "eu-west-1", AccessKey: "AK", SecretKey: "SK"}
	got := sign(t, `{"a":1}`, c)
	if !strings.HasPrefix(got, "AWS4-HMAC-SHA256 Credential=AK/20260102/eu-west-1/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, Signature=") {
		t.Fatalf("authorization = %s", got)
	}
	if got != sign(t, `{"a":1}`, c) {
		t.Fatal("signature is not deterministic")
	}
	if got == sign(t, `{"a":2}`, c) {
		t.Fatal("body change did not change the signature")
	}
	c.SessionToken = "tok"
	if !strings.Contains(sign(t, `{"a":1}`, c), "x-amz-security-token") {
		t.Fatal("session token not signed")
	}
}

func TestSignNeedsCredentials(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := Sign(req, nil, "bedrock", Credentials{Region: "us-east-1"}); err == nil {
		t.Fatal("signed without credentials")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "ap-south-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "envAK")
	c := Credentials{AccessKey: "explicit"}.FromEnv()
	if c.Region != "ap-south-1" || c.AccessKey != "explicit" {
		t.Fatalf("%+v", c)
	}
}

// The canonical query follows the SigV4 rules: %20 for a space, pairs sorted
// by key then value, repeated keys kept.
func TestCanonicalQuery(t *testing.T) {
	cases := map[string]string{
		"b=2&a=x y&a=1": "a=1&a=x%20y&b=2",
		"":              "",
		"flag":          "flag=",
		"k=a%2Fb":       "k=a%2Fb",
		"tilde=~-_.":    "tilde=~-_.",
	}
	for in, want := range cases {
		if got := canonicalQuery(in); got != want {
			t.Errorf("canonicalQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
