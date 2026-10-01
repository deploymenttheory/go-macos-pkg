// Package appledouble preserves the package tooling's AppleDouble API while
// delegating byte encoding and decoding to the shared filesystem SDK.
//
// New codec development lives in github.com/deploymenttheory/go-apfs-v2/pkg/appledouble.
// Native attributes and sidecar filesystem operations are separate concerns.
package appledouble

import shared "github.com/deploymenttheory/go-apfs-v2/pkg/appledouble"

// Attr is one extended attribute. This alias preserves shared type identity.
type Attr = shared.Attr

// File contains FinderInfo, a resource fork and ordinary extended attributes.
// Its Encode, Empty and Xattrs methods are supplied by the shared codec.
type File = shared.File

const (
	FinderInfoName   = shared.FinderInfoName
	ResourceForkName = shared.ResourceForkName
	MaxHeader        = shared.MaxHeader
)

var (
	ErrNotAppleDouble = shared.ErrNotAppleDouble
	ErrTooLarge       = shared.ErrTooLarge
)

// IsSidecarName reports whether a payload name has the AppleDouble prefix.
func IsSidecarName(name string) bool { return shared.IsSidecarName(name) }

// SidecarName returns the conventional carrier name for a payload path.
func SidecarName(path string) string { return shared.SidecarName(path) }

// OwnerName returns the payload path associated with a carrier name.
func OwnerName(path string) (string, bool) { return shared.OwnerName(path) }

// FromXattrs builds a shared AppleDouble value using the existing conversion semantics.
func FromXattrs(attrs map[string][]byte) *File { return shared.FromXattrs(attrs) }

// Sniff reports whether bytes start with the AppleDouble magic and version.
func Sniff(data []byte) bool { return shared.Sniff(data) }

// Decode parses AppleDouble bytes with the shared codec.
func Decode(data []byte) (*File, error) { return shared.Decode(data) }
