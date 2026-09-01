package notary

import (
	"encoding/base64"
	"errors"
	"testing"
)

// TestCredentials covers the environment-variable credential reader and the
// error a key the SDK cannot parse produces. The submit / upload / wait /
// log workflow lives in the SDK now and is tested there.
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
			// Pasted in as-is rather than encoded. Recognizable, and the
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
