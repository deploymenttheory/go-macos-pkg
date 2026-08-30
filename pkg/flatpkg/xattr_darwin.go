package flatpkg

import (
	"errors"

	"golang.org/x/sys/unix"
)

// hostXattrs reads a path's extended attributes without following a
// symlink, as pkgbuild does.
func hostXattrs(p string) (map[string][]byte, error) {
	names, err := listxattr(p)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(names))
	for _, name := range names {
		v, err := getxattr(p, name)
		if err != nil {
			if errors.Is(err, unix.ENOATTR) {
				continue // removed between list and get
			}
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func listxattr(p string) ([]string, error) {
	buf := make([]byte, 4096)
	for {
		n, err := unix.Llistxattr(p, buf)
		if err == unix.ERANGE {
			buf = make([]byte, len(buf)*2)
			continue
		}
		if err != nil {
			if errors.Is(err, unix.ENOTSUP) {
				return nil, nil
			}
			return nil, err
		}
		return splitNames(buf[:n]), nil
	}
}

func getxattr(p, name string) ([]byte, error) {
	buf := make([]byte, 4096)
	for {
		n, err := unix.Lgetxattr(p, name, buf)
		if err == unix.ERANGE {
			buf = make([]byte, len(buf)*2)
			continue
		}
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), buf[:n]...), nil
	}
}

// setHostXattr applies one attribute to a path without following a symlink.
func setHostXattr(p, name string, value []byte) error {
	return unix.Lsetxattr(p, name, value, 0)
}

const hostXattrsSupported = true
