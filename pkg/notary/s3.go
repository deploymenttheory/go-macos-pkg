// Uploading to the S3 bucket Apple's notary service hands out: one PUT,
// signed with AWS Signature Version 4, using the temporary credentials
// the submission response carries.
//
// The region is not in Apple's documentation; us-west-2 is what every
// working client uses.
package notary

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Region is where Apple's submission buckets live.
const S3Region = "us-west-2"

// maxSinglePut is S3's limit for one PUT.
const maxSinglePut = 5 << 30

// S3Credentials are the temporary credentials a submission returns.
type S3Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Bucket          string
	Object          string
}

// Uploader uploads a submission's file.
type Uploader interface {
	Upload(ctx context.Context, creds S3Credentials, path string, sha256Hex string, progress func(written, total int64)) error
}

// S3Uploader puts the file into the bucket directly.
type S3Uploader struct {
	Client *http.Client
	// Endpoint overrides the S3 endpoint (tests). Empty means AWS.
	Endpoint string
	// Now overrides the clock (tests).
	Now func() time.Time
}

// NewS3Uploader returns an uploader with a generous timeout: a package
// can be gigabytes.
func NewS3Uploader() *S3Uploader {
	return &S3Uploader{Client: &http.Client{Timeout: 4 * time.Hour}}
}

// Upload PUTs the file. sha256Hex is the file's SHA-256, which S3 wants
// as x-amz-content-sha256 and which the submission already computed.
func (u *S3Uploader) Upload(ctx context.Context, creds S3Credentials, path, sha256Hex string, progress func(written, total int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > maxSinglePut {
		return fmt.Errorf("%w: %d bytes exceeds the 5 GiB single-upload limit", ErrUnsupported, st.Size())
	}

	endpoint := u.Endpoint
	if endpoint == "" {
		endpoint = "https://" + creds.Bucket + ".s3." + S3Region + ".amazonaws.com"
	}
	target := endpoint + "/" + escapePath(creds.Object)
	// Virtual-hosted URLs carry the bucket in the host; a test endpoint
	// does not, so put it in the path there.
	if u.Endpoint != "" {
		target = endpoint + "/" + creds.Bucket + "/" + escapePath(creds.Object)
	}

	const maxAttempts = 4
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		body := &progressReader{r: f, total: st.Size(), fn: progress}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, body)
		if err != nil {
			return err
		}
		req.ContentLength = st.Size()
		req.Header.Set("Content-Type", "application/octet-stream")
		now := time.Now().UTC()
		if u.Now != nil {
			now = u.Now().UTC()
		}
		signV4(req, creds, S3Region, "s3", sha256Hex, now)

		client := u.Client
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("S3 returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return fmt.Errorf("notary: upload failed: %w", lastErr)
			}
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
	}
	return fmt.Errorf("notary: upload failed after %d attempts: %w", maxAttempts, lastErr)
}

type progressReader struct {
	r       io.Reader
	total   int64
	written int64
	fn      func(written, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.written += int64(n)
	if p.fn != nil && n > 0 {
		p.fn(p.written, p.total)
	}
	return n, err
}

// escapePath URI-encodes each path segment the way SigV4 canonicalises
// it (RFC 3986 unreserved characters kept, everything else percent-encoded).
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = awsEscape(part, false)
	}
	return strings.Join(parts, "/")
}

// awsEscape percent-encodes for SigV4: unreserved characters pass, and
// for a path segment "/" is kept out of the encoding by the caller.
func awsEscape(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// signV4 adds the Authorization, x-amz-date, x-amz-content-sha256 and
// x-amz-security-token headers per AWS Signature Version 4.
func signV4(req *http.Request, creds S3Credentials, region, service, payloadHash string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if creds.SessionToken != "" {
		req.Header.Set("x-amz-security-token", creds.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	// Canonical headers: host plus every x-amz-* and content-type, lower
	// case, sorted.
	headers := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "content-type" {
			headers[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	var names []string
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL),
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(sha256Bytes([]byte(canonicalRequest))),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, scope, signedHeaders, signature))
}

func canonicalPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	// S3 expects the path as sent, encoded once; awsEscape above already
	// produced that form, so re-escaping would double-encode.
	return p
}

func canonicalQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	values, _ := url.ParseQuery(u.RawQuery)
	var keys []string
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := values[k]
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k, true)+"="+awsEscape(v, true))
		}
	}
	return strings.Join(parts, "&")
}

func sha256Bytes(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
