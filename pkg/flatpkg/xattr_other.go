//go:build !darwin && !linux

package flatpkg

import "errors"

var errNoXattrs = errors.New("extended attributes are not supported on this host")

func hostXattrs(p string) (map[string][]byte, error) { return nil, nil }

func setHostXattr(p, name string, value []byte) error { return errNoXattrs }

const hostXattrsSupported = false
