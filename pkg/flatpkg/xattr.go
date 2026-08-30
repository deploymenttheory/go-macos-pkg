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
