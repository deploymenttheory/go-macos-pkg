// The checksum a bill of materials records for each file.
//
// It is not the CRC-32 that zlib and gzip use. It is the POSIX cksum(1)
// algorithm: the CRC-32 polynomial 0x04C11DB7 run unreflected with a zero
// initial value over the file's bytes and then over the file's length in
// little-endian bytes (as many as it takes), finally inverted. The
// checksum of an empty file is therefore 0xFFFFFFFF, which is what mkbom
// writes for one. This was established by comparing what pkgbuild writes
// against cksum(1) rather than from documentation; bomutils documents the
// field as a plain CRC-32, which does not match.
package bom

import "hash"

// cksumTable is the unreflected CRC-32 table for polynomial 0x04C11DB7.
var cksumTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// Cksum implements hash.Hash32 for the POSIX cksum algorithm.
type Cksum struct {
	crc uint32
	n   uint64
}

// NewCksum returns a new cksum hash.
func NewCksum() *Cksum { return &Cksum{} }

func (c *Cksum) Write(p []byte) (int, error) {
	crc := c.crc
	for _, b := range p {
		crc = crc<<8 ^ cksumTable[byte(crc>>24)^b]
	}
	c.crc = crc
	c.n += uint64(len(p))
	return len(p), nil
}

// Sum32 returns the checksum of everything written so far.
func (c *Cksum) Sum32() uint32 {
	crc := c.crc
	for n := c.n; n != 0; n >>= 8 {
		crc = crc<<8 ^ cksumTable[byte(crc>>24)^byte(n)]
	}
	return ^crc
}

// Sum appends the big-endian checksum to b.
func (c *Cksum) Sum(b []byte) []byte {
	s := c.Sum32()
	return append(b, byte(s>>24), byte(s>>16), byte(s>>8), byte(s))
}

// Reset clears the state.
func (c *Cksum) Reset() { c.crc, c.n = 0, 0 }

// Size returns the checksum size in bytes.
func (c *Cksum) Size() int { return 4 }

// BlockSize returns the hash's block size.
func (c *Cksum) BlockSize() int { return 1 }

var _ hash.Hash32 = (*Cksum)(nil)

// CksumBytes returns the checksum of b.
func CksumBytes(b []byte) uint32 {
	c := NewCksum()
	_, _ = c.Write(b)
	return c.Sum32()
}
