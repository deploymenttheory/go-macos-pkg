package flatpkg

import (
	"errors"

	"golang.org/x/sys/unix"
)

// hostXattrs reads a path's user.* extended attributes without following
// a symlink. Other namespaces (security, system, trusted) describe the
// host, not the file, and are left alone.
func hostXattrs(p string) (map[string][]byte, error) {
	buf := make([]byte, 4096)
	var n int
	var err error
	for {
		n, err = unix.Llistxattr(p, buf)
		if err == unix.ERANGE {
			buf = make([]byte, len(buf)*2)
			continue
		}
		break
	}
	if err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPERM) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string][]byte{}
	for _, name := range splitNames(buf[:n]) {
		if len(name) < 5 || name[:5] != "user." {
			continue
		}
		vbuf := make([]byte, 4096)
		for {
			m, err := unix.Lgetxattr(p, name, vbuf)
			if err == unix.ERANGE {
				vbuf = make([]byte, len(vbuf)*2)
				continue
			}
			if err != nil {
				if errors.Is(err, unix.ENODATA) {
					break
				}
				return nil, err
			}
			out[name] = append([]byte(nil), vbuf[:m]...)
			break
		}
	}
	return out, nil
}

// setHostXattrs applies attributes to a path without following a symlink.
// Linux accepts only the user.* namespace on ordinary files, and none on
// symlinks; the caller records what could not be set.
func setHostXattrs(p string, attrs map[string][]byte) error {
	for name, value := range attrs {
		if err := unix.Lsetxattr(p, name, value, 0); err != nil {
			return &xattrError{Name: name, Err: err}
		}
	}
	return nil
}

const hostXattrsSupported = true
