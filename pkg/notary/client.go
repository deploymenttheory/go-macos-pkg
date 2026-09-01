// Package notary submits packages to Apple's notary service, waits for the
// verdict and fetches the log (the parts of notarytool a CI job needs) on any
// platform.
//
// The submit, upload, poll and log-download workflow lives in the
// deploymenttheory Apple services SDK. This package is a thin layer over it:
// it keeps the credential handling and environment-variable UX macospkg
// documents, and wraps the SDK behind a small Client the CLI talks to.
package notary

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	sdk "github.com/deploymenttheory/go-sdk-appleservices/notary"
)

// Status is the state of a submission, as the SDK reports it.
type Status = sdk.Status

// LogIssue is one entry from the issues list of a developer log.
type LogIssue = sdk.LogIssue

// SubmitInput describes a file to notarize.
type SubmitInput = sdk.SubmitInput

// Result is the outcome of SubmitAndWait.
type Result = sdk.Result

// WaitOptions configures Wait and SubmitAndWait.
type WaitOptions = sdk.WaitOptions

// Submission statuses, as Apple reports them.
const (
	StatusAccepted   = sdk.StatusAccepted
	StatusInProgress = sdk.StatusInProgress
	StatusInvalid    = sdk.StatusInvalid
	StatusRejected   = sdk.StatusRejected
)

// WebhookChannel is the only notification channel Apple's notary service
// defines.
const WebhookChannel = sdk.WebhookChannel

// ErrRejected and ErrTimeout come from the SDK so errors.Is keeps matching
// after the workflow moved there.
var (
	// ErrRejected reports a submission Apple marked Invalid or Rejected.
	ErrRejected = sdk.ErrRejected
	// ErrTimeout reports a wait that ended while still in progress.
	ErrTimeout = sdk.ErrTimeout
)

// ParseLogIssues pulls the issues out of a developer log for display.
var ParseLogIssues = sdk.ParseLogIssues

// FileSHA256 hashes a file and returns the lowercase hexadecimal digest.
var FileSHA256 = sdk.FileSHA256

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

// The names electron-builder uses for the same three things, accepted as
// well because a project that already notarizes has them set and should
// not have to set them twice under different names. APPLE_API_KEY is the
// .p8 base64-encoded, which is how a key survives being a CI secret.
const (
	EnvBuilderKeyID   = "APPLE_API_KEY_ID"
	EnvBuilderIssuer  = "APPLE_API_ISSUER"
	EnvBuilderKeyData = "APPLE_API_KEY"
)

// CredentialsFromEnv reads the APPLE_* variables.
//
// Where a name has an electron-builder equivalent, either will do, and the
// name this tool documents wins if both are set.
func CredentialsFromEnv() (*Credentials, error) {
	c := &Credentials{
		KeyID:    firstSet(EnvKeyID, EnvBuilderKeyID),
		IssuerID: firstSet(EnvIssuerID, EnvBuilderIssuer),
	}
	switch {
	case os.Getenv(EnvPrivateKeyPEM) != "":
		c.PrivateKey = []byte(os.Getenv(EnvPrivateKeyPEM))
	case os.Getenv(EnvPrivateKeyPath) != "":
		path := os.Getenv(EnvPrivateKeyPath)
		data, err := os.ReadFile(path) //nolint:gosec // the variable names the key file on purpose
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCredentials, EnvPrivateKeyPath, err)
		}
		c.PrivateKey = data
	case os.Getenv(EnvBuilderKeyData) != "":
		// Base64, as electron-builder's documentation says to encode it.
		// A key pasted in as-is is accepted too rather than rejected on a
		// technicality: PEM is recognizable and the intent is obvious.
		raw := os.Getenv(EnvBuilderKeyData)
		if strings.Contains(raw, "-----BEGIN") {
			c.PrivateKey = []byte(raw)
			break
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: %s is not base64: %v", ErrCredentials, EnvBuilderKeyData, err)
		}
		c.PrivateKey = data
	}
	return c, c.Validate()
}

// firstSet returns the value of the first variable that has one.
func firstSet(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
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

// Client is macospkg's handle on the notary service: a thin wrapper over the
// SDK client that keeps the CLI free of the SDK's package layout and maps
// failures onto this package's error sentinels.
type Client struct {
	sdk *sdk.Client
}

// NewService opens the SDK client with the credentials.
func NewService(c *Credentials, userAgent string) (*Client, error) {
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
	return &Client{sdk: client}, nil
}

// SubmitAndWait notarizes a file end to end via the SDK: hash, register,
// upload and poll for a verdict. On a non-Accepted verdict it returns
// ErrRejected (wrapped) with the Result populated, including the log's issues.
func (c *Client) SubmitAndWait(ctx context.Context, in SubmitInput, wo WaitOptions) (*Result, error) {
	return c.sdk.SubmitAndWait(ctx, in, wo)
}

// Wait polls an existing submission until it is done or the timeout passes.
func (c *Client) Wait(ctx context.Context, id string, o WaitOptions) (*Status, error) {
	return sdk.Wait(ctx, c.sdk, id, o)
}

// FetchLog downloads the developer log, a JSON document, for a submission.
func (c *Client) FetchLog(ctx context.Context, id string) (json.RawMessage, error) {
	return sdk.FetchLog(ctx, c.sdk, nil, id)
}

// Status returns the current state of a submission.
func (c *Client) Status(ctx context.Context, id string) (*Status, error) {
	resp, _, err := c.sdk.NotaryAPI.Submissions.GetSubmissionStatusV2(ctx, id)
	if err != nil {
		return nil, apiError("status", err)
	}
	return &Status{ID: resp.Data.ID, Name: resp.Data.Attributes.Name, Status: resp.Data.Attributes.Status, CreatedDate: resp.Data.Attributes.CreatedDate}, nil
}

// LogURL returns the temporary URL Apple hands out for a submission's log.
func (c *Client) LogURL(ctx context.Context, id string) (string, error) {
	resp, _, err := c.sdk.NotaryAPI.Submissions.GetSubmissionLogV2(ctx, id)
	if err != nil {
		return "", apiError("log", err)
	}
	return resp.Data.Attributes.DeveloperLogURL, nil
}

// List returns the team's recent submissions, newest first.
func (c *Client) List(ctx context.Context) ([]Status, error) {
	resp, _, err := c.sdk.NotaryAPI.Submissions.GetPreviousSubmissionsV2(ctx)
	if err != nil {
		return nil, apiError("list", err)
	}
	var out []Status
	for _, d := range resp.Data {
		out = append(out, Status{ID: d.ID, Name: d.Attributes.Name, Status: d.Attributes.Status, CreatedDate: d.Attributes.CreatedDate})
	}
	return out, nil
}

// apiError classifies SDK failures: authentication problems are credential
// errors, a missing submission is reported as such, the rest as they are.
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
