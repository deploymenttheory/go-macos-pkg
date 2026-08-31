// Test harness for the acceptance suite: locates the repository, builds the
// macospkg binary under test, loads the committed fixture manifest and
// provides the run helpers every acceptance test uses.
package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

var (
	binPath    string
	repoRoot   string
	fixtureDir string
	manifest   fixtureManifest
)

// fixtureManifest describes the committed packages in testdata/cli. It is
// written by scripts/gen-fixtures.sh from what Apple's tools reported, so a
// test comparing against it is comparing against pkgbuild, not against us.
type fixtureManifest struct {
	Generator struct {
		MacOS    string `json:"macos"`
		Pkgbuild string `json:"pkgbuild"`
		Script   string `json:"script"`
		// AppleDouble records that the generating machine attached
		// com.apple.provenance to every file, so pkgbuild wrote ._ sidecar
		// entries into the payloads and bills of materials.
		AppleDouble bool `json:"appleDouble"`
		// CompressionLatest records, per --min-os-version tried, what
		// container pkgbuild --compression latest wrote.
		CompressionLatest map[string]string `json:"compressionLatest"`
	} `json:"generator"`
	Packages map[string]manifestPackage `json:"packages"`
}

type manifestPackage struct {
	manifestComponent

	Kind       string                       `json:"kind"` // component | product
	SHA256     string                       `json:"sha256"`
	Entries    []string                     `json:"entries"`
	Components map[string]manifestComponent `json:"components"`
	Resources  []string                     `json:"resources"`
	Title      string                       `json:"title"`
	Choices    []string                     `json:"choices"`
	SignedBy   string                       `json:"signedBy"`
	Digest     string                       `json:"digest"`
}

// manifestComponent is what pkgbuild wrote into one component's
// PackageInfo, plus what lsbom reported from its Bom.
type manifestComponent struct {
	Identifier      string `json:"identifier"`
	Version         string `json:"version"`
	InstallLocation string `json:"installLocation"`
	PayloadEntry    string `json:"payloadEntry"`
	PayloadEncoding string `json:"payloadEncoding"`
	// PayloadBlockSize and PayloadChunks describe a pbz* payload's
	// container; ScriptsEncoding is the Scripts archive's container.
	PayloadBlockSize uint64                  `json:"payloadBlockSize"`
	PayloadChunks    int                     `json:"payloadChunks"`
	ScriptsEncoding  string                  `json:"scriptsEncoding"`
	NumberOfFiles    int                     `json:"numberOfFiles"`
	InstallKBytes    int                     `json:"installKBytes"`
	Scripts          []string                `json:"scripts"`
	Files            map[string]manifestFile `json:"files"`
}

type manifestFile struct {
	Type   string `json:"type"` // file | dir | link
	Mode   string `json:"mode"` // octal, e.g. "0755"
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Target string `json:"target"`
}

func TestMain(m *testing.M) {
	var err error
	repoRoot, err = findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to find repo root: %v\n", err)
		os.Exit(1)
	}

	tempDir, err := os.MkdirTemp("", "macospkg-acceptance")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Build the CLI binary under test.
	binPath = filepath.Join(tempDir, "macospkg")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binPath, "./cmd/macospkg")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "unable to build CLI: %v\n%s", err, out)
		os.Exit(1)
	}

	fixtureDir = filepath.Join(repoRoot, "testdata", "cli")

	// The manifest may be absent before the fixtures are generated; tests that
	// need it skip. A manifest that is present but unparseable is fatal rather
	// than ignored: silently degrading to "no fixture coverage" is how a whole
	// tier of tests disappears unnoticed.
	if data, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json")); err == nil {
		if err := json.Unmarshal(data, &manifest); err != nil {
			fmt.Fprintf(os.Stderr, "unable to parse manifest: %v\n", err)
			os.Exit(1)
		}
	}

	code := m.Run()
	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// fixture returns the path of a committed fixture package, skipping the test
// when the manifest does not list it.
func fixture(t *testing.T, name string) (string, manifestPackage) {
	t.Helper()
	pkg, ok := manifest.Packages[name]
	if !ok {
		t.Skipf("fixture %s is not in testdata/cli/manifest.json; run scripts/gen-fixtures.sh on macOS", name)
	}
	return filepath.Join(fixtureDir, name), pkg
}

// cleanEnv returns the ambient environment with the variables the CLI reads
// blanked, so a value set in the developer's shell or by a CI reproducible-build
// wrapper cannot perturb a test. SOURCE_DATE_EPOCH matters as much as the
// MACOSPKG_ variables here, because it changes the bytes every write command
// produces; the APPLE_ variables are the notary credentials.
func cleanEnv(extra ...string) []string {
	env := append(os.Environ(),
		"MACOSPKG_OUTPUT=",
		"MACOSPKG_QUIET=",
		"MACOSPKG_VERBOSE=",
		"SOURCE_DATE_EPOCH=",
		"MACOSPKG_SOURCE_DATE_EPOCH=",
		"APPLE_KEY_ID=",
		"APPLE_ISSUER_ID=",
		"APPLE_PRIVATE_KEY_PEM=",
		"APPLE_PRIVATE_KEY_PATH=",
	)
	return append(env, extra...)
}

// run executes the CLI and returns stdout, stderr and the exit code.
func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runEnv(t, nil, args...)
}

// runEnv is run() with extra environment entries appended, for the tests that
// exercise SOURCE_DATE_EPOCH and its precedence.
func runEnv(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = cleanEnv(env...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("unable to run %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), exitCode
}

// runTimeout is run() with a deadline, for tests whose failure mode is the
// command never returning. Without it a regression that reintroduces a hang
// stalls the whole suite until the package timeout fires, and reports as a
// panic in whichever test happened to be running rather than as a failure here.
//
//nolint:unused // used by the write-side tests that follow
func runTimeout(t *testing.T, limit time.Duration, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = cleanEnv()
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("%v did not finish within %s; it is hanging\nstderr: %s", args, limit, stderr.String())
	}
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("unable to run %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), exitCode
}

// runWithStdin is run() with input piped to the command's stdin, for
// --p12-password-stdin.
//
//nolint:unused // used by the signing tests that follow
func runWithStdin(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = cleanEnv()
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("unable to run %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), exitCode
}

// mustRun executes the CLI and fails the test on a non-zero exit.
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, code := run(t, args...)
	if code != exitcode.OK {
		t.Fatalf("%v exited %d (%s)\nstderr: %s", args, code, exitcode.Name(code), stderr)
	}
	return stdout
}

// mustRunJSON runs the CLI with -o json and decodes the single JSON document
// it prints into out.
func mustRunJSON(t *testing.T, out any, args ...string) {
	t.Helper()
	stdout := mustRun(t, append([]string{"-o", "json"}, args...)...)
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		t.Fatalf("%v printed invalid JSON: %v\n%s", args, err, stdout)
	}
}

// mustRunOnlineJSON is mustRunJSON for a command that talks to Apple's
// servers. A failure to reach them skips the test rather than failing it.
// These references check what we send and how we read the answer back, not
// whether a shared runner can open a TLS connection to
// api.apple-cloudkit.com; a build that goes red for a handshake timeout
// only teaches people to press re-run without reading it. A refusal from
// Apple still fails, because that is the thing being tested.
func mustRunOnlineJSON(t *testing.T, out any, args ...string) {
	t.Helper()
	stdout, stderr, code := run(t, append([]string{"-o", "json"}, args...)...)
	if code != 0 {
		if unreachable(stderr) {
			t.Skipf("Apple's service is unreachable, so there is nothing to compare against: %s", strings.TrimSpace(stderr))
		}
		t.Fatalf("%v exited %d\nstderr: %s", args, code, stderr)
	}
	decodeJSON(t, stdout, out)
}

// unreachable reports whether a command failed to get to the server at
// all, as opposed to getting an answer it did not like.
func unreachable(stderr string) bool {
	for _, s := range []string{
		"TLS handshake timeout",
		"i/o timeout",
		"context deadline exceeded",
		"no such host",
		"connection refused",
		"connection reset",
		"unexpected EOF",
		"network is unreachable",
		"server misbehaving",
	} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// decodeJSON decodes one JSON document a command printed.
func decodeJSON(t *testing.T, stdout string, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
}

// requireTools skips the test unless every named tool is on PATH. reference
// tests use it so a machine without Apple's tools skips rather than fails.
func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available on this machine", tool)
		}
	}
}

// hostTool runs an operating-system tool (never the binary under test) and
// returns its combined output, failing the test on a non-zero exit.
func hostTool(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// requireInstallerOptIn skips the tests that run macOS's installer as
// root unless this is CI or the user opted in. A third-party package's
// scripts run as root and may touch the boot volume regardless of the
// target (Go's preinstall removes /usr/local/go), which is fine on an
// ephemeral runner and not on a developer's machine.
func requireInstallerOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "true" && os.Getenv("MACOSPKG_ACCEPTANCE_INSTALL") == "" {
		t.Skip("installer tests run as root and may modify the boot volume; set MACOSPKG_ACCEPTANCE_INSTALL=1 to run them outside CI")
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skip("passwordless sudo is not available; installer needs root")
	}
}

// hostToolOutput runs an operating-system tool and returns its combined
// output and error, for tests that expect a failure.
func hostToolOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// attest logs an observation and, in CI, appends it to the step summary, so
// a green run states what it saw rather than only that it passed.
func attest(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf("ATTEST "+format, args...)
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "- `%s`: "+format+"\n", append([]any{t.Name()}, args...)...)
}

// --- helpers ---

func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func fileSHA256(path string) (string, error) {
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
