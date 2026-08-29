package notary

import (
	"context"
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
}

func (f *fakeService) Submit(_ context.Context, name, sha string) (*Submission, error) {
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
	sub, sum, err := Submit(context.Background(), svc, up, path, "x.pkg", nil)
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
