package appledouble

import (
	"bytes"
	"errors"
	"testing"

	shared "github.com/deploymenttheory/go-apfs-v2/pkg/appledouble"
)

func TestSharedCodecCompatibility(t *testing.T) {
	legacyFile := sharedFileIdentity(FromXattrs(map[string][]byte{"com.example.empty": {}, ResourceForkName: []byte("fork")}))
	sharedAttr := sharedAttrIdentity(Attr{Name: "com.example.marker", Value: []byte("marker")})
	legacyFile.Attrs = append(legacyFile.Attrs, sharedAttr)
	raw, err := legacyFile.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !Sniff(raw) || Sniff([]byte("ordinary")) {
		t.Fatal("sniff compatibility")
	}
	decoded, err := Decode(raw)
	if err != nil || decoded.Empty() || !bytes.Equal(decoded.ResourceFork, legacyFile.ResourceFork) {
		t.Fatal(decoded, err)
	}
	if _, ok := decoded.Xattrs()["com.example.empty"]; !ok {
		t.Fatal("empty attribute lost")
	}
	if ErrTooLarge != shared.ErrTooLarge || ErrNotAppleDouble != shared.ErrNotAppleDouble {
		t.Fatal("sentinel identity changed")
	}
	if _, err := Decode(nil); !errors.Is(err, ErrNotAppleDouble) {
		t.Fatal(err)
	}
	if name, ok := OwnerName("file"); ok || name != "" {
		t.Fatal(name, ok)
	}
	if FinderInfoName != shared.FinderInfoName || ResourceForkName != shared.ResourceForkName || MaxHeader != shared.MaxHeader {
		t.Fatal("constant compatibility")
	}
}

// Parameter and result assignability check both directions without conversions.
func sharedFileIdentity(value *shared.File) *File { return value }
func sharedAttrIdentity(value shared.Attr) Attr   { return value }
