package staple

import (
	"os"
	"path/filepath"
	"testing"
)

// makeBundle writes a minimal .app: Contents/Info.plist naming an executable
// and an (empty) executable file. It is enough for the path and ticket logic;
// the CDHash logic needs a real signed binary and is covered elsewhere.
func makeBundle(t *testing.T, exe string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Test.app")
	if err := os.MkdirAll(filepath.Join(dir, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>` + exe + `</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(dir, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Contents", "MacOS", exe), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIsAppBundle(t *testing.T) {
	app := makeBundle(t, "Test")
	if !IsAppBundle(app) {
		t.Error("a bundle with Contents/Info.plist should be recognized")
	}
	if IsAppBundle(filepath.Join(app, "Contents", "Info.plist")) {
		t.Error("a file is not a bundle")
	}
	if IsAppBundle(t.TempDir()) {
		t.Error("a plain directory is not a bundle")
	}
}

func TestBundleExecutable(t *testing.T) {
	app := makeBundle(t, "MyApp")
	got, err := BundleExecutable(app)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(app, "Contents", "MacOS", "MyApp")
	if got != want {
		t.Fatalf("executable = %s, want %s", got, want)
	}
}

func TestStapleUnstapleApp(t *testing.T) {
	app := makeBundle(t, "Test")
	ticket := append([]byte(nil), ticketMagic...)
	ticket = append(ticket, []byte("a stapled ticket body")...)

	if has, err := AppHasTicket(app); err != nil || has {
		t.Fatalf("a fresh bundle should carry no ticket (has=%v err=%v)", has, err)
	}
	if err := StapleApp(app, ticket); err != nil {
		t.Fatal(err)
	}
	has, err := AppHasTicket(app)
	if err != nil || !has {
		t.Fatalf("bundle should be stapled (has=%v err=%v)", has, err)
	}
	got, err := os.ReadFile(filepath.Join(app, "Contents", "CodeResources"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(ticket) {
		t.Fatalf("stapled bytes differ from the ticket")
	}
	// Restapling replaces in place.
	if err := StapleApp(app, ticket); err != nil {
		t.Fatalf("restapling: %v", err)
	}
	if err := UnstapleApp(app); err != nil {
		t.Fatal(err)
	}
	if has, _ := AppHasTicket(app); has {
		t.Fatal("unstaple should have removed the ticket")
	}
	// Unstapling a bundle with no ticket is a no-op.
	if err := UnstapleApp(app); err != nil {
		t.Fatalf("unstaple with no ticket: %v", err)
	}
}

func TestStapleAppRejectsNonTicket(t *testing.T) {
	app := makeBundle(t, "Test")
	if err := StapleApp(app, []byte("not a ticket")); err == nil {
		t.Fatal("a blob without the s8ch magic should be rejected")
	}
	// And it must not have written a file.
	if has, _ := AppHasTicket(app); has {
		t.Fatal("a rejected staple must leave no ticket behind")
	}
}
