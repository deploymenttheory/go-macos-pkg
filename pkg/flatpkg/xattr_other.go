//go:build !darwin && !linux

package flatpkg

import "errors"

var errNoXattrs = errors.New("extended attributes are not supported on this host")

func hostXattrs(p string) (map[string][]byte, error) { return nil, nil }

func setHostXattrs(p string, attrs map[string][]byte) error {
	for name := range attrs {
		return &xattrError{Name: name, Err: errNoXattrs}
	}
	return nil
}

const hostXattrsSupported = false
