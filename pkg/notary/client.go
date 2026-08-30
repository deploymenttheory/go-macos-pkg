// Package notary submits packages to Apple's notary service, waits for
// the verdict and fetches the log (the parts of notarytool a CI job
// needs) on any platform.
//
// The four REST calls go through the deploymenttheory Apple services SDK;
// everything the SDK does not do (the S3 upload, polling, log download,
// stapling) is here. The SDK is wrapped behind Service so the rest of the
// package, and its tests, never see it.
package notary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/deploymenttheory/go-sdk-appleservices/notary"
	"github.com/deploymenttheory/go-sdk-appleservices/notary/notary_api/submissions"
)

// Submission statuses, as Apple reports them.
const (
	StatusAccepted   = "Accepted"
	StatusInProgress = "In Progress"
	StatusInvalid    = "Invalid"
	StatusRejected   = "Rejected"
)

// Submission is what Apple returns for a new submission: an id and where
// to upload.
type Submission struct {
	ID    string
	Creds S3Credentials
}

// Status is the state of a submission.
type Status struct {
	ID          string
	Name        string
	Status      string
	CreatedDate string
}

// Done reports whether Apple has finished with the submission.
func (s *Status) Done() bool { return s.Status != StatusInProgress && s.Status != "" }

// Service is the notary API as this package uses it.
type Service interface {
	Submit(ctx context.Context, name, sha256Hex string) (*Submission, error)
	Status(ctx context.Context, id string) (*Status, error)
	LogURL(ctx context.Context, id string) (string, error)
	List(ctx context.Context) ([]Status, error)
}

// Credentials are the App Store Connect API key details Apple's notary
// service authenticates with.
type Credentials struct {
	KeyID      string
	IssuerID   string
	PrivateKey []byte // PEM (.p8) content
}

// Environment variables the SDK reads, accepted here for the same reason.
const (
	EnvKeyID          = "APPLE_KEY_ID"
	EnvIssuerID       = "APPLE_ISSUER_ID"
	EnvPrivateKeyPEM  = "APPLE_PRIVATE_KEY_PEM"
	EnvPrivateKeyPath = "APPLE_PRIVATE_KEY_PATH"
)

// CredentialsFromEnv reads the APPLE_* variables.
func CredentialsFromEnv() (*Credentials, error) {
	c := &Credentials{KeyID: os.Getenv(EnvKeyID), IssuerID: os.Getenv(EnvIssuerID)}
	if pem := os.Getenv(EnvPrivateKeyPEM); pem != "" {
		c.PrivateKey = []byte(pem)
	} else if path := os.Getenv(EnvPrivateKeyPath); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // the variable names the key file on purpose
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCredentials, EnvPrivateKeyPath, err)
		}
		c.PrivateKey = data
	}
	return c, c.Validate()
}

// Validate checks that everything needed is present.
func (c *Credentials) Validate() error {
	var missing []string
	if c.KeyID == "" {
		missing = append(missing, "key ID")
	}
	if c.IssuerID == "" {
		missing = append(missing, "issuer ID")
	}
	if len(c.PrivateKey) == 0 {
		missing = append(missing, "private key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s (set %s, %s and %s or %s)", ErrCredentials, strings.Join(missing, ", "), EnvKeyID, EnvIssuerID, EnvPrivateKeyPEM, EnvPrivateKeyPath)
	}
	return nil
}

// sdkService adapts the SDK to Service.
type sdkService struct {
	client *sdk.Client
}

// NewService opens the SDK client with the credentials.
func NewService(c *Credentials, userAgent string) (Service, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	key, err := sdk.ParsePrivateKey(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: private key: %v", ErrCredentials, err)
	}
	opts := []sdk.ClientOption{sdk.WithTimeout(60 * time.Second), sdk.WithRetryCount(3)}
	if userAgent != "" {
		opts = append(opts, sdk.WithUserAgent(userAgent))
	}
	client, err := sdk.NewClient(c.KeyID, c.IssuerID, key, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCredentials, err)
	}
	return &sdkService{client: client}, nil
}

func (s *sdkService) Submit(ctx context.Context, name, sha256Hex string) (*Submission, error) {
	resp, _, err := s.client.NotaryAPI.Submissions.SubmitSoftwareV2(ctx, &submissions.NewSubmissionRequest{
		SHA256:         sha256Hex,
		SubmissionName: name,
	})
	if err != nil {
		return nil, apiError("submit", err)
	}
	a := resp.Data.Attributes
	return &Submission{
		ID: resp.Data.ID,
		Creds: S3Credentials{
			AccessKeyID: a.AWSAccessKeyID, SecretAccessKey: a.AWSSecretAccessKey, SessionToken: a.AWSSessionToken,
			Bucket: a.Bucket, Object: a.Object,
		},
	}, nil
}

func (s *sdkService) Status(ctx context.Context, id string) (*Status, error) {
	resp, _, err := s.client.NotaryAPI.Submissions.GetSubmissionStatusV2(ctx, id)
	if err != nil {
		return nil, apiError("status", err)
	}
	return &Status{ID: resp.Data.ID, Name: resp.Data.Attributes.Name, Status: resp.Data.Attributes.Status, CreatedDate: resp.Data.Attributes.CreatedDate}, nil
}

func (s *sdkService) LogURL(ctx context.Context, id string) (string, error) {
	resp, _, err := s.client.NotaryAPI.Submissions.GetSubmissionLogV2(ctx, id)
	if err != nil {
		return "", apiError("log", err)
	}
	return resp.Data.Attributes.DeveloperLogURL, nil
}

func (s *sdkService) List(ctx context.Context) ([]Status, error) {
	resp, _, err := s.client.NotaryAPI.Submissions.GetPreviousSubmissionsV2(ctx)
	if err != nil {
		return nil, apiError("list", err)
	}
	var out []Status
	for _, d := range resp.Data {
		out = append(out, Status{ID: d.ID, Name: d.Attributes.Name, Status: d.Attributes.Status, CreatedDate: d.Attributes.CreatedDate})
	}
	return out, nil
}

// apiError classifies SDK failures: authentication problems are
// credential errors, the rest are reported as they are.
func apiError(op string, err error) error {
	msg := err.Error()
	if strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(strings.ToLower(msg), "unauthorized") || strings.Contains(strings.ToLower(msg), "forbidden") {
		return fmt.Errorf("%w: Apple rejected the API key (%s): %v", ErrCredentials, op, err)
	}
	if sdk.IsNotFound(err) {
		return fmt.Errorf("notary: %s: no such submission", op)
	}
	return fmt.Errorf("notary: %s: %w", op, err)
}

// FileSHA256 hashes a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Submit hashes the file, registers the submission and uploads the file.
func Submit(ctx context.Context, svc Service, up Uploader, path, name string, progress func(written, total int64)) (*Submission, string, error) {
	sum, err := FileSHA256(path)
	if err != nil {
		return nil, "", err
	}
	sub, err := svc.Submit(ctx, name, sum)
	if err != nil {
		return nil, sum, err
	}
	if err := up.Upload(ctx, sub.Creds, path, sum, progress); err != nil {
		return sub, sum, err
	}
	return sub, sum, nil
}

// WaitOptions configures Wait.
type WaitOptions struct {
	Interval time.Duration // default 30s
	Timeout  time.Duration // default 30m
	// Progress, when set, is called after each poll.
	Progress func(status *Status)
}

// Wait polls until the submission is done or the timeout passes. A
// finished submission that Apple did not accept returns ErrRejected with
// the status.
func Wait(ctx context.Context, svc Service, id string, o WaitOptions) (*Status, error) {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Minute
	}
	deadline := time.Now().Add(o.Timeout)
	var last *Status
	for {
		st, err := svc.Status(ctx, id)
		if err != nil {
			return last, err
		}
		last = st
		if o.Progress != nil {
			o.Progress(st)
		}
		if st.Done() {
			if st.Status != StatusAccepted {
				return st, fmt.Errorf("%w: %s", ErrRejected, st.Status)
			}
			return st, nil
		}
		if time.Now().After(deadline) {
			return st, ErrTimeout
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(o.Interval):
		}
	}
}

// FetchLog downloads the developer log, a JSON document.
func FetchLog(ctx context.Context, svc Service, client *http.Client, id string) (json.RawMessage, error) {
	u, err := svc.LogURL(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == "" {
		return nil, errors.New("notary: no log is available for the submission yet")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("notary: log download: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("notary: log download returned %s", resp.Status)
	}
	if !json.Valid(data) {
		return nil, errors.New("notary: the log is not JSON")
	}
	return json.RawMessage(data), nil
}

// LogIssues extracts the issues list from a developer log for display.
type LogIssue struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path"`
	Code     any    `json:"code"`
	DocURL   string `json:"docUrl"`
}

// ParseLogIssues pulls the issues out of a developer log.
func ParseLogIssues(log json.RawMessage) []LogIssue {
	var doc struct {
		Issues []LogIssue `json:"issues"`
	}
	_ = json.Unmarshal(log, &doc)
	return doc.Issues
}
