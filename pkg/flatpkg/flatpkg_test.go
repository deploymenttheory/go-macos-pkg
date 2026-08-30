package flatpkg

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "cli", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not committed", name)
	}
	return path
}

func TestOpenComponent(t *testing.T) {
	p, err := Open(fixturePath(t, "component-basic.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Kind != KindComponent || len(p.Components) != 1 || p.Components[0].Name != "" {
		t.Fatalf("kind %s, components %d", p.Kind, len(p.Components))
	}
	c := p.Components[0]
	if c.Info.Identifier != "com.deploymenttheory.fixture.basic" || c.Info.Version != "1.0.0" || c.Info.InstallLocation != "/" {
		t.Errorf("PackageInfo: %+v", c.Info)
	}
	if c.Info.FormatVersion != 2 || c.Info.Auth != "root" || c.Info.Payload == nil || c.Info.Payload.NumberOfFiles == 0 {
		t.Errorf("PackageInfo details: %+v payload %+v", c.Info, c.Info.Payload)
	}
	if got := c.Info.Scripts.Names(); len(got) != 2 || got[0] != "preinstall" || got[1] != "postinstall" {
		t.Errorf("scripts = %v", got)
	}
	enc, err := c.PayloadEncoding()
	if err != nil || enc != PayloadGzip {
		t.Errorf("payload encoding = %s, %v", enc, err)
	}
	cr, _, closer, err := c.OpenPayloadCPIO()
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	n := 0
	found := false
	for {
		h, err := cr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n++
		if h.Name == "./usr/local/fixture/hello.txt" {
			data, _ := io.ReadAll(cr)
			if string(data) != "hello, world\n" {
				t.Errorf("hello.txt = %q", data)
			}
			found = true
		}
	}
	if !found {
		t.Error("hello.txt not in payload")
	}
	// pkgbuild's numberOfFiles counts every cpio entry, "." included.
	if n != c.Info.Payload.NumberOfFiles {
		t.Errorf("payload has %d entries, PackageInfo says %d", n, c.Info.Payload.NumberOfFiles)
	}
	b, err := c.Bom()
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := b.Paths()
	if len(entries) != n {
		t.Errorf("bom has %d entries, payload %d", len(entries), n)
	}
	sr, _, sc, err := c.OpenScriptsCPIO()
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	var scripts []string
	for {
		h, err := sr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// pkgbuild on a machine with provenance tracking adds ._ AppleDouble
		// entries beside each script; they are not scripts.
		if !strings.HasPrefix(filepath.Base(h.Name), "._") {
			scripts = append(scripts, h.Name)
		}
	}
	if len(scripts) != 3 || scripts[0] != "." {
		t.Errorf("scripts archive = %v", scripts)
	}
}

func TestOpenPBZXAndLarge(t *testing.T) {
	p, err := Open(fixturePath(t, "component-pbzx.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := p.Components[0]
	if enc, _ := c.PayloadEncoding(); enc != PayloadPBZX {
		t.Errorf("pbzx fixture encoding = %s", enc)
	}
	cr, enc, closer, err := c.OpenPayloadCPIO()
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if enc != PayloadPBZX {
		t.Errorf("OpenCPIO encoding = %s", enc)
	}
	var total int64
	n := 0
	for {
		h, err := cr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d entries: %v", n, err)
		}
		n++
		if h.Name == "./usr/local/fixture/huge.bin" {
			written, err := io.Copy(io.Discard, cr)
			if err != nil {
				t.Fatal(err)
			}
			total = written
		}
	}
	if total != 20<<20 {
		t.Errorf("huge.bin decoded to %d bytes, want %d", total, 20<<20)
	}

	lp, err := Open(fixturePath(t, "component-large-payload.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	defer lp.Close()
	c = lp.Components[0]
	if c.PayloadEntryName() != EntryLargePayload || c.Info.Payload.LargeSegmented != "true" {
		t.Errorf("large payload entry %q, attr %q", c.PayloadEntryName(), c.Info.Payload.LargeSegmented)
	}
	if enc, _ := c.PayloadEncoding(); enc != PayloadGzip {
		t.Errorf("large payload encoding = %s", enc)
	}
}

func TestOpenProduct(t *testing.T) {
	p, err := Open(fixturePath(t, "product-custom-dist.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Kind != KindProduct || p.Distribution == nil {
		t.Fatal("not recognised as a product archive")
	}
	d := p.Distribution
	if d.Title != "Fixture Custom" || d.MinSpecVersion != "2" {
		t.Errorf("title %q minSpecVersion %q", d.Title, d.MinSpecVersion)
	}
	if got := d.Options.Architectures(); len(got) != 2 || got[0] != "arm64" {
		t.Errorf("architectures = %v", got)
	}
	if got := d.ChoiceIDs(); len(got) != 2 || got[0] != "basic" || got[1] != "extra" {
		t.Errorf("choices = %v", got)
	}
	if got := d.PackagePaths(); len(got) != 2 || got[0] != "component-basic.pkg" {
		t.Errorf("package paths = %v", got)
	}
	if d.VolumeCheck == nil || d.VolumeCheck.AllowedOSVersions == nil || d.VolumeCheck.AllowedOSVersions.Versions[0].Min != "12.0" {
		t.Errorf("volume-check = %+v", d.VolumeCheck)
	}
	if len(d.Scripts) != 1 {
		t.Errorf("scripts = %d", len(d.Scripts))
	}
	if len(p.Components) != 2 || p.Components[0].Name != "component-basic.pkg" || p.Components[1].Name != "component-noscripts.pkg" {
		var names []string
		for _, c := range p.Components {
			names = append(names, c.Name)
		}
		t.Errorf("components = %v", names)
	}
	if p.Component("component-noscripts.pkg").Info.InstallLocation != "/opt/fixture" {
		t.Error("nested component PackageInfo not read")
	}
	if p.Component("nope") != nil {
		t.Error("unknown component found")
	}
}

func TestNotAPackage(t *testing.T) {
	plain := filepath.Join("..", "..", "testdata", "xar", "plain.xar")
	if _, err := os.Stat(plain); err != nil {
		t.Skip("plain.xar not committed")
	}
	if _, err := Open(plain); err != ErrNotPackage {
		t.Errorf("plain xar: err = %v, want ErrNotPackage", err)
	}
	if _, err := Open(filepath.Join("..", "..", "go.mod")); err == nil {
		t.Error("go.mod opened as a package")
	}
}

func TestSniffPayload(t *testing.T) {
	cases := []struct {
		head string
		want PayloadEncoding
	}{
		{"\x1f\x8b\x08xxxx", PayloadGzip},
		{"pbzx\x00\x00", PayloadPBZX},
		{"pbze\x00\x00", PayloadPBZE},
		{"pbz4\x00\x00", PayloadPBZ4},
		{"pbzz\x00\x00", PayloadPBZZ},
		{"pbzb\x00\x00", PayloadPBZB},
		{"070707000", PayloadCPIO},
		{"070701000", PayloadCPIO},
		{"AA01\x00", PayloadAppleArchive},
		{"YAA1\x00", PayloadAppleArchive},
		{"nope", PayloadUnknown},
	}
	for _, tc := range cases {
		if got := SniffPayload([]byte(tc.head)); got != tc.want {
			t.Errorf("SniffPayload(%q) = %s, want %s", tc.head, got, tc.want)
		}
	}
}
