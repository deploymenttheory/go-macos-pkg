package bom

import "testing"

func TestCksum(t *testing.T) {
	// Values from cksum(1) on macOS, which are also what pkgbuild writes
	// into the Bom for the same files (see the fixtures).
	cases := []struct {
		in   string
		want uint32
	}{
		{"", 0xFFFFFFFF},
		{"hello, world\n", 0x535fbd37},
		{"#!/bin/sh\necho tool\n", 0x50730d3a},
	}
	for _, tc := range cases {
		if got := CksumBytes([]byte(tc.in)); got != tc.want {
			t.Errorf("CksumBytes(%q) = %#x, want %#x", tc.in, got, tc.want)
		}
		// Chunked writes must agree with one write.
		c := NewCksum()
		for _, b := range []byte(tc.in) {
			c.Write([]byte{b})
		}
		if got := c.Sum32(); got != tc.want {
			t.Errorf("chunked Cksum(%q) = %#x, want %#x", tc.in, got, tc.want)
		}
	}
}
