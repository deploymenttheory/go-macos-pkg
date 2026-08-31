// The build-info manifest: munkipkg's project format, which puts the
// package's identity next to its payload and scripts so a repository can
// carry everything a build needs.
//
//	project/
//	  build-info.yaml     (or .json or .plist)
//	  payload/            the files to install
//	  scripts/            preinstall, postinstall, ...
//
// Keys are munkipkg's, so an existing project builds unchanged; the ones
// this tool adds are marked.
package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"gopkg.in/yaml.v3"
	"howett.net/plist"
)

// buildManifest mirrors build-info.*.
type manifestFile struct {
	Name                     string `yaml:"name" json:"name" plist:"name"`
	Identifier               string `yaml:"identifier" json:"identifier" plist:"identifier"`
	Version                  string `yaml:"version" json:"version" plist:"version"`
	InstallLocation          string `yaml:"install_location" json:"install_location" plist:"install_location"`
	Ownership                string `yaml:"ownership" json:"ownership" plist:"ownership"`
	PostinstallAction        string `yaml:"postinstall_action" json:"postinstall_action" plist:"postinstall_action"`
	DistributionStyle        bool   `yaml:"distribution_style" json:"distribution_style" plist:"distribution_style"`
	SuppressBundleRelocation bool   `yaml:"suppress_bundle_relocation" json:"suppress_bundle_relocation" plist:"suppress_bundle_relocation"`
	MinimumOSVersion         string `yaml:"minimum_os_version" json:"minimum_os_version" plist:"minimum_os_version"`
	PreserveXattr            bool   `yaml:"preserve_xattr" json:"preserve_xattr" plist:"preserve_xattr"`
	NoPayload                bool   `yaml:"nopayload" json:"nopayload" plist:"nopayload"`       // added
	Compression              string `yaml:"compression" json:"compression" plist:"compression"` // gzip | pbzx | latest | lzfse | lzbitmap (added)
	Xattrs                   string `yaml:"xattrs" json:"xattrs" plist:"xattrs"`                // fs | none (added)
	HardLinks                string `yaml:"hard_links" json:"hard_links" plist:"hard_links"`    // auto | copy (added)
	ProductID                string `yaml:"product_id" json:"product_id" plist:"product_id"`

	// Added by macospkg: payload paths and their modes, for trees that
	// come from hosts without execute bits.
	Filter       []string `yaml:"filter" json:"filter" plist:"filter"`
	ExcludeXattr []string `yaml:"exclude_xattr" json:"exclude_xattr" plist:"exclude_xattr"`
	// LegacyExclude catches the old spelling of Filter. Unknown keys are
	// ignored by every decoder here, so without this a manifest written
	// against the old name would quietly stop filtering anything.
	LegacyExclude []string `yaml:"exclude" json:"exclude" plist:"exclude"`
	// FileXattrs overrides extended attributes by payload path (a folder
	// when it ends in "/"); values are base64 (added).
	FileXattrs         []manifestXattrs `yaml:"file_xattrs" json:"file_xattrs" plist:"file_xattrs"`
	ExecutablePatterns []string         `yaml:"executable_patterns" json:"executable_patterns" plist:"executable_patterns"`
	Files              []manifestMode   `yaml:"files" json:"files" plist:"files"`

	SigningInfo      *manifestSigning `yaml:"signing_info" json:"signing_info" plist:"signing_info"`
	NotarizationInfo *manifestNotary  `yaml:"notarization_info" json:"notarization_info" plist:"notarization_info"`

	// dir is where the manifest was found; payload/ and scripts/ are
	// resolved against it.
	dir string
}

// manifestXattrs overrides one path's extended attributes. Path names a
// file, or a folder when it ends in "/", in which case it covers the
// folder and everything beneath it. Replace makes the listed attributes
// the complete set for those paths; without it they are merged over what
// the tree carries, and a name given here wins.
type manifestXattrs struct {
	Path    string            `yaml:"path" json:"path" plist:"path"`
	Xattrs  map[string]string `yaml:"xattrs" json:"xattrs" plist:"xattrs"`
	Replace bool              `yaml:"replace" json:"replace" plist:"replace"`
}

// xattrOverrides decodes the manifest's per-path attribute rules, in the
// order they are written: a later rule overrides an earlier one.
func (m *manifestFile) xattrOverrides() ([]flatpkg.XattrOverride, error) {
	if len(m.FileXattrs) == 0 {
		return nil, nil
	}
	out := make([]flatpkg.XattrOverride, 0, len(m.FileXattrs))
	for _, fx := range m.FileXattrs {
		if fx.Path == "" {
			return nil, fmt.Errorf("file_xattrs: an entry has no path")
		}
		attrs := map[string][]byte{}
		for name, b64 := range fx.Xattrs {
			v, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("file_xattrs %s %s: not base64: %v", fx.Path, name, err)
			}
			attrs[name] = v
		}
		out = append(out, flatpkg.XattrOverride{Path: fx.Path, Xattrs: attrs, Replace: fx.Replace})
	}
	return out, nil
}

type manifestMode struct {
	Path string `yaml:"path" json:"path" plist:"path"`
	Mode string `yaml:"mode" json:"mode" plist:"mode"`
}

// manifestSigning holds signing settings. munkipkg's identity key names a
// keychain identity, which does not exist off macOS; this tool uses files.
type manifestSigning struct {
	Identity        string `yaml:"identity" json:"identity" plist:"identity"`
	P12Path         string `yaml:"p12_path" json:"p12_path" plist:"p12_path"`
	P12PasswordEnv  string `yaml:"p12_password_env" json:"p12_password_env" plist:"p12_password_env"`
	CertificatePath string `yaml:"certificate_path" json:"certificate_path" plist:"certificate_path"`
	KeyPath         string `yaml:"key_path" json:"key_path" plist:"key_path"`
	ChainPath       string `yaml:"chain_path" json:"chain_path" plist:"chain_path"`
	Timestamp       *bool  `yaml:"timestamp" json:"timestamp" plist:"timestamp"`
}

type manifestNotary struct {
	KeyID          string `yaml:"key_id" json:"key_id" plist:"key_id"`
	IssuerID       string `yaml:"issuer_id" json:"issuer_id" plist:"issuer_id"`
	PrivateKeyPath string `yaml:"private_key_path" json:"private_key_path" plist:"private_key_path"`
}

var manifestNames = []string{"build-info.yaml", "build-info.yml", "build-info.json", "build-info.plist"}

// loadManifestFor finds and parses the manifest for a build: the explicit
// path if given, else a build-info.* inside src. A src without one is a
// plain payload root and yields an empty manifest.
func loadManifestFor(src, explicit string) (*manifestFile, error) {
	path := explicit
	if path == "" {
		for _, n := range manifestNames {
			candidate := filepath.Join(src, n)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return &manifestFile{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, usageErrorf("unable to read manifest %s: %v", path, err)
	}
	m := &manifestFile{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, m)
	case ".json":
		err = json.Unmarshal(data, m)
	case ".plist":
		_, err = plist.Unmarshal(data, m)
	default:
		return nil, usageErrorf("manifest %s: unknown format (want .yaml, .json or .plist)", path)
	}
	if err != nil {
		return nil, usageErrorf("manifest %s: %v", path, err)
	}
	if len(m.LegacyExclude) > 0 {
		return nil, usageErrorf("manifest %s: the exclude key is now named filter", path)
	}
	m.dir = filepath.Dir(path)
	if explicit != "" {
		// An explicit manifest beside a payload root: src is the root.
		m.dir = ""
	}
	return m, nil
}

// payloadRoot returns the payload directory: src/payload for a project,
// src itself otherwise.
func (m *manifestFile) payloadRoot(src string) string {
	if m.dir != "" {
		return filepath.Join(m.dir, "payload")
	}
	return src
}

// scriptsDir returns the project's scripts directory if it exists.
func (m *manifestFile) scriptsDir(src string) string {
	if m.dir == "" {
		return ""
	}
	dir := filepath.Join(m.dir, "scripts")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir
	}
	return ""
}

// outputPath derives the output name from the manifest's name, with
// munkipkg's ${version} substitution, into the project's build/ directory.
func (m *manifestFile) outputPath(src, version string) string {
	if m.dir == "" || m.Name == "" {
		return ""
	}
	name := strings.ReplaceAll(m.Name, "${version}", version)
	if !strings.HasSuffix(name, ".pkg") {
		name += ".pkg"
	}
	return filepath.Join(m.dir, "build", name)
}

// fileModes converts the files list to the builder's override map.
func (m *manifestFile) fileModes() map[string]uint32 {
	out := map[string]uint32{}
	for _, f := range m.Files {
		v, err := strconv.ParseUint(strings.TrimPrefix(f.Mode, "0o"), 8, 32)
		if err != nil {
			continue
		}
		p := f.Path
		if !strings.HasPrefix(p, "./") {
			p = "./" + strings.TrimPrefix(p, "/")
		}
		out[p] = uint32(v)
	}
	return out
}
