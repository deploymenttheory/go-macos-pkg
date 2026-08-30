package flatpkg

import "fmt"

// xattrError reports one attribute the host refused.
type xattrError struct {
	Name string
	Err  error
}

func (e *xattrError) Error() string { return fmt.Sprintf("%s: %v", e.Name, e.Err) }
func (e *xattrError) Unwrap() error { return e.Err }

// splitNames splits a NUL-separated attribute name list.
func splitNames(b []byte) []string {
	var names []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				names = append(names, string(b[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(b) {
		names = append(names, string(b[start:]))
	}
	return names
}

// setHostXattrs applies every attribute, stopping at the first the host
// refuses. Extraction sets them one at a time instead, so that it can
// keep what a host will not take.
func setHostXattrs(p string, attrs map[string][]byte) error {
	for name, value := range attrs {
		if err := setHostXattr(p, name, value); err != nil {
			return &xattrError{Name: name, Err: err}
		}
	}
	return nil
}
