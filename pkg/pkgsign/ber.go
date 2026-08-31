// BER to DER: Apple's Security framework writes the CMS signature with
// BER indefinite lengths and constructed strings, which encoding/asn1
// refuses. Rewriting the encoding with definite lengths, and joining
// constructed strings, gives the DER form the parser wants without
// changing any value.
package pkgsign

import (
	"errors"
	"fmt"
)

var errBER = errors.New("pkgsign: malformed BER")

// maxBERDepth bounds the nesting berToDER will follow. Apple's CMS nests
// well under ten deep; a stream of constructed indefinite-length headers
// (30 80 30 80 ...) otherwise recurses once per two input bytes, and each
// level re-copies its content, which is O(n^2) work and O(n) stack frames.
const maxBERDepth = 64

// berToDER re-encodes data with definite lengths. DER input passes
// through unchanged in value (and nearly always in bytes).
func berToDER(data []byte) ([]byte, error) {
	out, rest, err := berElement(data, 0)
	if err != nil {
		return nil, err
	}
	// Anything after the outermost element (zero padding in a xar heap)
	// is dropped: ParseCMS tolerated it before and still does.
	_ = rest
	return out, nil
}

// berElement converts one TLV at the start of data, returning its DER
// form and the bytes that follow it.
func berElement(data []byte, depth int) (der []byte, rest []byte, err error) {
	if depth > maxBERDepth {
		return nil, nil, fmt.Errorf("%w: nested deeper than %d", errBER, maxBERDepth)
	}
	if len(data) < 2 {
		return nil, nil, errBER
	}
	// Identifier octets (support multi-byte tags).
	idEnd := 1
	if data[0]&0x1f == 0x1f {
		for idEnd < len(data) && data[idEnd]&0x80 != 0 {
			idEnd++
		}
		idEnd++
		if idEnd > len(data) {
			return nil, nil, errBER
		}
	}
	id := data[:idEnd]
	constructed := id[0]&0x20 != 0
	p := idEnd
	if p >= len(data) {
		return nil, nil, errBER
	}

	var content []byte
	switch {
	case data[p] == 0x80:
		// Indefinite length: children until end-of-contents.
		if !constructed {
			return nil, nil, fmt.Errorf("%w: indefinite length on a primitive", errBER)
		}
		p++
		var children [][]byte
		for {
			if p+2 > len(data) {
				return nil, nil, fmt.Errorf("%w: unterminated indefinite length", errBER)
			}
			if data[p] == 0 && data[p+1] == 0 {
				p += 2
				break
			}
			child, r, err := berElement(data[p:], depth+1)
			if err != nil {
				return nil, nil, err
			}
			children = append(children, child)
			p = len(data) - len(r)
		}
		content = joinChildren(id[0], children)
	case data[p]&0x80 == 0:
		n := int(data[p])
		p++
		if p+n > len(data) {
			return nil, nil, fmt.Errorf("%w: length runs past the data", errBER)
		}
		content = data[p : p+n]
		p += n
		if constructed {
			// Definite-length constructed: its children may still use
			// indefinite lengths, so convert them too.
			c, err := berChildren(content, depth+1)
			if err != nil {
				return nil, nil, err
			}
			content = joinChildren(id[0], c)
		}
	default:
		nBytes := int(data[p] & 0x7f)
		p++
		if nBytes == 0 || nBytes > 4 || p+nBytes > len(data) {
			return nil, nil, fmt.Errorf("%w: bad length encoding", errBER)
		}
		n := 0
		for i := 0; i < nBytes; i++ {
			n = n<<8 | int(data[p+i])
		}
		p += nBytes
		if p+n > len(data) {
			return nil, nil, fmt.Errorf("%w: length runs past the data", errBER)
		}
		content = data[p : p+n]
		p += n
		if constructed {
			c, err := berChildren(content, depth+1)
			if err != nil {
				return nil, nil, err
			}
			content = joinChildren(id[0], c)
		}
	}

	// A constructed string type (OCTET STRING, BIT STRING, ...) becomes
	// the primitive form once its pieces are joined.
	outID := append([]byte(nil), id...)
	if constructed && isStringType(id[0]) {
		outID[0] &^= 0x20
	}
	der = append(der, outID...)
	der = append(der, encodeLength(len(content))...)
	der = append(der, content...)
	return der, data[p:], nil
}

// berChildren converts every element in a constructed body.
func berChildren(body []byte, depth int) ([][]byte, error) {
	var out [][]byte
	for len(body) > 0 {
		child, rest, err := berElement(body, depth)
		if err != nil {
			return nil, err
		}
		out = append(out, child)
		body = rest
	}
	return out, nil
}

// joinChildren concatenates converted children; for a constructed string
// the children are string pieces whose contents are joined into one.
func joinChildren(tag byte, children [][]byte) []byte {
	var out []byte
	if isStringType(tag) && tag&0x20 != 0 {
		for _, c := range children {
			// Each child is a primitive string TLV: strip its header.
			_, body := splitTLV(c)
			out = append(out, body...)
		}
		return out
	}
	for _, c := range children {
		out = append(out, c...)
	}
	return out
}

// splitTLV separates a DER element into header and content.
func splitTLV(el []byte) (header, content []byte) {
	idEnd := 1
	if el[0]&0x1f == 0x1f {
		for idEnd < len(el) && el[idEnd]&0x80 != 0 {
			idEnd++
		}
		idEnd++
	}
	p := idEnd
	if el[p]&0x80 == 0 {
		n := int(el[p])
		return el[:p+1], el[p+1 : p+1+n]
	}
	nBytes := int(el[p] & 0x7f)
	n := 0
	for i := 1; i <= nBytes; i++ {
		n = n<<8 | int(el[p+i])
	}
	return el[:p+1+nBytes], el[p+1+nBytes : p+1+nBytes+n]
}

// isStringType reports whether a universal tag is a string type that BER
// may encode constructed: OCTET STRING, BIT STRING and the character
// strings.
func isStringType(tag byte) bool {
	if tag&0xc0 != 0 { // not universal class
		return false
	}
	switch tag & 0x1f {
	case 3, 4, 12, 18, 19, 20, 21, 22, 25, 26, 27, 28, 29, 30:
		return true
	}
	return false
}

func encodeLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for v := n; v > 0; v >>= 8 {
		b = append([]byte{byte(v)}, b...)
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}
