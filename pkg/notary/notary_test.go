package notary

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeService scripts the notary API.
type fakeService struct {
	statuses []string
	calls    int
	submits  []string
	logURL   string
	// submitOptions records what the last submission asked for, so a test
	// can check a webhook reached the request.
	submitOptions SubmitOptions
}

func (f *fakeService) Submit(_ context.Context, name, sha string, o SubmitOptions) (*Submission, error) {
	f.submitOptions = o
	f.submits = append(f.submits, name+":"+sha)
	return &Submission{ID: "sub-1", Creds: S3Credentials{AccessKeyID: "a", SecretAccessKey: "s", SessionToken: "t", Bucket: "b", Object: "o"}}, nil
}

func (f *fakeService) Status(_ context.Context, id string) (*Status, error) {
	i := f.calls
	if i >= len(f.statuses) {
		i = len(f.statuses) - 1
	}
	f.calls++
	return &Status{ID: id, Name: "x.pkg", Status: f.statuses[i]}, nil
}

func (f *fakeService) LogURL(context.Context, string) (string, error) { return f.logURL, nil }
func (f *fakeService) List(context.Context) ([]Status, error)         { return nil, nil }

type fakeUploader struct{ got S3Credentials }

func (u *fakeUploader) Upload(_ context.Context, c S3Credentials, path, sum string, _ func(int64, int64)) error {
	u.got = c
	return nil
}

func TestSubmitAndWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.pkg")
	os.WriteFile(path, []byte("hello"), 0o644)
	svc := &fakeService{statuses: []string{StatusInProgress, StatusInProgress, StatusAccepted}}
	up := &fakeUploader{}
	sub, sum, err := Submit(context.Background(), svc, up, path, "x.pkg", SubmitOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ID != "sub-1" || up.got.Bucket != "b" || sum != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("submit: %+v %s", sub, sum)
	}
	var seen []string
	st, err := Wait(context.Background(), svc, sub.ID, WaitOptions{Interval: time.Millisecond, Progress: func(s *Status) { seen = append(seen, s.Status) }})
	if err != nil || st.Status != StatusAccepted || len(seen) != 3 {
		t.Errorf("wait: %v %+v %v", err, st, seen)
	}

	svc = &fakeService{statuses: []string{StatusInvalid}}
	if _, err := Wait(context.Background(), svc, "x", WaitOptions{Interval: time.Millisecond}); !errors.Is(err, ErrRejected) {
		t.Errorf("invalid: %v", err)
	}
	svc = &fakeService{statuses: []string{StatusInProgress}}
	if _, err := Wait(context.Background(), svc, "x", WaitOptions{Interval: time.Millisecond, Timeout: 5 * time.Millisecond}); !errors.Is(err, ErrTimeout) {
		t.Errorf("timeout: %v", err)
	}
}

func TestFetchLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"Invalid","issues":[{"severity":"error","message":"The signature does not include a secure timestamp.","path":"x.pkg"}]}`))
	}))
	defer srv.Close()
	svc := &fakeService{logURL: srv.URL + "/log"}
	log, err := FetchLog(context.Background(), svc, srv.Client(), "id")
	if err != nil {
		t.Fatal(err)
	}
	issues := ParseLogIssues(log)
	if len(issues) != 1 || issues[0].Severity != "error" {
		t.Errorf("issues = %+v", issues)
	}
	if !json.Valid(log) {
		t.Error("log not JSON")
	}
}

func TestCredentials(t *testing.T) {
	t.Setenv(EnvKeyID, "")
	t.Setenv(EnvIssuerID, "")
	t.Setenv(EnvPrivateKeyPEM, "")
	t.Setenv(EnvPrivateKeyPath, "")
	if _, err := CredentialsFromEnv(); !errors.Is(err, ErrCredentials) {
		t.Errorf("empty env: %v", err)
	}
	t.Setenv(EnvKeyID, "K")
	t.Setenv(EnvIssuerID, "I")
	t.Setenv(EnvPrivateKeyPEM, "-----BEGIN PRIVATE KEY-----")
	c, err := CredentialsFromEnv()
	if err != nil || c.KeyID != "K" || len(c.PrivateKey) == 0 {
		t.Errorf("env creds: %+v %v", c, err)
	}
	// A real ES256 key must be parseable by the SDK.
	if _, err := NewService(&Credentials{KeyID: "K", IssuerID: "I", PrivateKey: []byte("junk")}, "test"); !errors.Is(err, ErrCredentials) {
		t.Errorf("junk key: %v", err)
	}
}

// TestSubmitCarriesAWebhook checks the notification reaches the request.
//
// A webhook is the difference between a build that waits for Apple and one
// that gets told, so it is worth pinning that asking for one is not quietly
// dropped on the way to the service.
func TestSubmitCarriesAWebhook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.pkg")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &fakeService{statuses: []string{StatusAccepted}}
	if _, _, err := Submit(context.Background(), svc, &fakeUploader{}, path, "x.pkg",
		SubmitOptions{Webhook: "https://example.invalid/hook"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := svc.submitOptions.Webhook; got != "https://example.invalid/hook" {
		t.Errorf("the submission did not carry the webhook: %q", got)
	}

	// And that asking for none leaves none, so a submission without one
	// does not grow an empty notification.
	svc = &fakeService{statuses: []string{StatusAccepted}}
	if _, _, err := Submit(context.Background(), svc, &fakeUploader{}, path, "x.pkg", SubmitOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if svc.submitOptions.Webhook != "" {
		t.Errorf("a submission with no webhook asked for one: %q", svc.submitOptions.Webhook)
	}
}

// TestCredentialsFromEnvAcceptsBuilderNames covers the variable names
// electron-builder uses.
//
// A project that already notarizes has APPLE_API_KEY_ID, APPLE_API_ISSUER
// and APPLE_API_KEY set, and should not have to set the same three things
// again under different names. The key comes base64-encoded, which is how
// a .p8 survives being a CI secret.
func TestCredentialsFromEnvAcceptsBuilderNames(t *testing.T) {
	const pem = "-----BEGIN PRIVATE KEY-----\nZm9v\n-----END PRIVATE KEY-----\n"
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "electron-builder names, key base64 as documented",
			env: map[string]string{
				"APPLE_API_KEY_ID": "K", "APPLE_API_ISSUER": "I",
				"APPLE_API_KEY": base64.StdEncoding.EncodeToString([]byte(pem)),
			},
			want: pem,
		},
		{
			// Pasted in as-is rather than encoded. Recognisable, and the
			// intent is obvious, so it is taken rather than refused.
			name: "electron-builder names, key pasted unencoded",
			env: map[string]string{
				"APPLE_API_KEY_ID": "K", "APPLE_API_ISSUER": "I", "APPLE_API_KEY": pem,
			},
			want: pem,
		},
		{
			name: "our own names still win where both are set",
			env: map[string]string{
				"APPLE_KEY_ID": "ours", "APPLE_ISSUER_ID": "I",
				"APPLE_PRIVATE_KEY_PEM": pem,
				"APPLE_API_KEY_ID":      "theirs", "APPLE_API_ISSUER": "other",
			},
			want: pem,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			c, err := CredentialsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if string(c.PrivateKey) != tc.want {
				t.Errorf("private key = %q, want %q", c.PrivateKey, tc.want)
			}
			if c.IssuerID == "" {
				t.Error("no issuer was read")
			}
			if _, ours := tc.env["APPLE_KEY_ID"]; ours && c.KeyID != "ours" {
				t.Errorf("key ID = %q, want the name this tool documents to win", c.KeyID)
			}
		})
	}
}

// TestCredentialsFromEnvRejectsAMangledKey pins that a key that is neither
// base64 nor PEM is reported rather than passed on to fail later as an
// unparseable key.
func TestCredentialsFromEnvRejectsAMangledKey(t *testing.T) {
	t.Setenv("APPLE_API_KEY_ID", "K")
	t.Setenv("APPLE_API_ISSUER", "I")
	t.Setenv("APPLE_API_KEY", "not base64 and not a key !!!")
	if _, err := CredentialsFromEnv(); err == nil {
		t.Fatal("a key that is neither base64 nor PEM should be reported")
	}
}
