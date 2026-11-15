// Package awssig signs AWS requests with Signature Version 4 and resolves
// credentials from the standard environment variables.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Credentials are AWS credentials and a region.
type Credentials struct {
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
}

// FromEnv fills empty fields from AWS_REGION (or AWS_DEFAULT_REGION, then
// us-east-1), AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and
// AWS_SESSION_TOKEN.
func (c Credentials) FromEnv() Credentials {
	if c.Region == "" {
		c.Region = os.Getenv("AWS_REGION")
	}
	if c.Region == "" {
		c.Region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.AccessKey == "" {
		c.AccessKey = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if c.SecretKey == "" {
		c.SecretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	if c.SessionToken == "" {
		c.SessionToken = os.Getenv("AWS_SESSION_TOKEN")
	}
	return c
}

// Sign adds a SigV4 Authorization header for service.
func Sign(req *http.Request, body []byte, service string, c Credentials) error {
	return signAt(req, body, service, c, time.Now().UTC())
}

func signAt(req *http.Request, body []byte, service string, c Credentials, now time.Time) error {
	if c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("kritrim: AWS credentials are not set")
	}
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payloadHash := sha256Hex(body)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}
	headers := map[string]string{
		"content-type":         req.Header.Get("Content-Type"),
		"host":                 req.URL.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if c.SessionToken != "" {
		headers["x-amz-security-token"] = c.SessionToken
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, k := range keys {
		canonicalHeaders.WriteString(k + ":" + strings.Join(strings.Fields(headers[k]), " ") + "\n")
	}
	signed := strings.Join(keys, ";")
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery(req.URL.RawQuery),
		canonicalHeaders.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := date + "/" + c.Region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+c.SecretKey), date)
	key = hmacSHA256(key, c.Region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, s string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return mac.Sum(nil)
}

// canonicalQuery is the SigV4 canonical query string: each key and value is
// percent-encoded with RFC 3986 rules (a space is %20, never "+"), pairs are
// sorted by key then value, and repeated keys are all kept. url.Values.Encode
// gets all three wrong for signing.
func canonicalQuery(raw string) string {
	type pair struct{ k, v string }
	var pairs []pair
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}
		pairs = append(pairs, pair{awsEscape(k), awsEscape(v)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// awsEscape percent-encodes everything except the unreserved set A-Z a-z 0-9 - _ . ~.
func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
