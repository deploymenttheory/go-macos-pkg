package machoidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"testing"
)

func codeDirectory(kind byte) []byte {
	b := make([]byte, 96)
	be := binary.BigEndian
	be.PutUint32(b, 0xfade0c02)
	be.PutUint32(b[4:], uint32(len(b)))
	be.PutUint32(b[8:], 0x20200)
	be.PutUint32(b[12:], 2)
	be.PutUint32(b[16:], 96)
	be.PutUint32(b[20:], 52)
	be.PutUint32(b[48:], 72)
	copy(b[52:], "com.example.test\x00")
	copy(b[72:], "TEAM123456\x00")
	b[36] = 32
	b[37] = kind
	if kind == 1 || kind == 3 {
		b[36] = 20
	}
	if kind == 4 {
		b[36] = 48
	}
	return b
}

func superBlob(cds ...[]byte) []byte {
	n := 12 + len(cds)*8
	for _, cd := range cds {
		n += len(cd)
	}
	b := make([]byte, n)
	be := binary.BigEndian
	be.PutUint32(b, 0xfade0cc0)
	be.PutUint32(b[4:], uint32(n))
	be.PutUint32(b[8:], uint32(len(cds)))
	off := 12 + len(cds)*8
	for i, cd := range cds {
		kind := uint32(0)
		if i > 0 {
			kind = 0x1000 + uint32(i-1)
		}
		be.PutUint32(b[12+i*8:], kind)
		be.PutUint32(b[16+i*8:], uint32(off))
		copy(b[off:], cd)
		off += len(cd)
	}
	return b
}

func thin(cpu, sub uint32, order binary.ByteOrder, wide bool, signature []byte) []byte {
	n := 28
	magic := uint32(0xfeedface)
	if wide {
		n = 32
		magic = 0xfeedfacf
	}
	cmd := 0
	if signature != nil {
		cmd = 16
	}
	b := make([]byte, n+cmd+len(signature))
	order.PutUint32(b, magic)
	order.PutUint32(b[4:], cpu)
	order.PutUint32(b[8:], sub)
	if signature != nil {
		order.PutUint32(b[16:], 1)
		order.PutUint32(b[20:], 16)
		order.PutUint32(b[n:], 0x1d)
		order.PutUint32(b[n+4:], 16)
		order.PutUint32(b[n+8:], uint32(n+16))
		order.PutUint32(b[n+12:], uint32(len(signature)))
		copy(b[n+16:], signature)
	}
	return b
}

func fat(order binary.ByteOrder, wide bool, slices ...[]byte) []byte {
	stride := 20
	magic := uint32(0xcafebabe)
	if wide {
		stride = 32
		magic = 0xcafebabf
	}
	size := 8 + len(slices)*stride
	for _, s := range slices {
		size += len(s)
	}
	b := make([]byte, size)
	order.PutUint32(b, magic)
	order.PutUint32(b[4:], uint32(len(slices)))
	off := 8 + len(slices)*stride
	for i, s := range slices {
		var so binary.ByteOrder = binary.LittleEndian
		if binary.BigEndian.Uint32(s) == 0xfeedface || binary.BigEndian.Uint32(s) == 0xfeedfacf {
			so = binary.BigEndian
		}
		e := b[8+i*stride:]
		order.PutUint32(e, so.Uint32(s[4:]))
		order.PutUint32(e[4:], so.Uint32(s[8:]))
		if wide {
			order.PutUint64(e[8:], uint64(off))
			order.PutUint64(e[16:], uint64(len(s)))
		} else {
			order.PutUint32(e[8:], uint32(off))
			order.PutUint32(e[12:], uint32(len(s)))
		}
		copy(b[off:], s)
		off += len(s)
	}
	return b
}

func parse(b []byte) ([]Architecture, error) {
	return Read(context.Background(), bytes.NewReader(b), int64(len(b)))
}

func TestReadArchitecturesAndAlgorithms(t *testing.T) {
	cd := codeDirectory(2)
	hash := sha256.Sum256(cd)
	for _, order := range []binary.ByteOrder{binary.BigEndian, binary.LittleEndian} {
		for _, wide := range []bool{false, true} {
			image := thin(0x100000c, 0, order, wide, superBlob(cd, codeDirectory(1), codeDirectory(3), codeDirectory(4)))
			arches, err := parse(image)
			if err != nil || len(arches) != 1 {
				t.Fatalf("thin: %v %v", arches, err)
			}
			a := arches[0]
			if a.Name != "arm64" || a.CPU != 0x100000c || a.Subtype != 0 || len(a.CodeDirectories) != 4 {
				t.Fatal(a)
			}
			for _, d := range a.CodeDirectories {
				if d.SigningID != "com.example.test" || d.TeamID != "TEAM123456" || len(d.CDHash) != 40 || d.Flags != 2 {
					t.Fatal(d)
				}
			}
			if a.CodeDirectories[0].FullHash != hex.EncodeToString(hash[:]) || a.CodeDirectories[0].CDHash != hex.EncodeToString(hash[:20]) {
				t.Fatal(a)
			}
			universal := fat(order, wide, image, thin(0x1000007, 3, binary.LittleEndian, true, superBlob(cd)))
			arches, err = parse(universal)
			if err != nil || len(arches) != 2 || arches[1].Name != "x86_64" {
				t.Fatalf("fat: %v %v", arches, err)
			}
		}
	}
}

func TestUnsignedAndMetadata(t *testing.T) {
	for _, tt := range []struct {
		cpu, sub uint32
		name     string
	}{{0x100000c, 2, "arm64e"}, {0x1000007, 8, "x86_64h"}, {7, 3, "i386"}, {0x100000c, 9, "CpuArm64:9"}, {0x1000007, 9, "CpuAmd64:9"}, {7, 9, "Cpu386:9"}, {12, 9, "CpuArm:9"}} {
		a, err := parse(thin(tt.cpu, tt.sub, binary.LittleEndian, true, nil))
		if err != nil || a[0].Name != tt.name || len(a[0].CodeDirectories) != 0 {
			t.Fatal(a, err)
		}
	}
	b := codeDirectory(2)
	binary.BigEndian.PutUint32(b[48:], 0)
	d, err := directory(b)
	if err != nil || d.TeamID != "" {
		t.Fatal(d, err)
	}
	for _, v := range []uint32{0x20000, 0x20100, 0x20200, 0x20300, 0x20400, 0x20500, 0x20600} {
		b = make([]byte, 160)
		copy(b, codeDirectory(2))
		be := binary.BigEndian
		be.PutUint32(b[4:], 160)
		be.PutUint32(b[8:], v)
		be.PutUint32(b[16:], 160)
		be.PutUint32(b[20:], 112)
		be.PutUint32(b[48:], 140)
		copy(b[112:], "com.example.test\x00")
		copy(b[140:], "TEAM123456\x00")
		if _, err := directory(b); err != nil {
			t.Fatalf("version %x: %v", v, err)
		}
	}
}

func TestMalformedImages(t *testing.T) {
	valid := thin(0x100000c, 0, binary.LittleEndian, true, superBlob(codeDirectory(2)))
	for n := 0; n < len(valid); n++ {
		if _, err := parse(valid[:n]); err == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	for _, tt := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"magic", func(b []byte) { b[0] = 0 }},
		{"loadCount", func(b []byte) { binary.LittleEndian.PutUint32(b[16:], 100) }},
		{"loadLength", func(b []byte) { binary.LittleEndian.PutUint32(b[20:], maxMetadata+1) }},
		{"commandSmall", func(b []byte) { binary.LittleEndian.PutUint32(b[36:], 4) }},
		{"commandLarge", func(b []byte) { binary.LittleEndian.PutUint32(b[36:], 1024) }},
		{"commandAlignment", func(b []byte) { binary.LittleEndian.PutUint32(b[36:], 9) }},
		{"commandSignatureLength", func(b []byte) { binary.LittleEndian.PutUint32(b[36:], 12) }},
		{"signatureOverlap", func(b []byte) { binary.LittleEndian.PutUint32(b[40:], 1) }},
		{"signatureSize", func(b []byte) { binary.LittleEndian.PutUint32(b[44:], 0xffffffff) }},
		{"trailingCommands", func(b []byte) { binary.LittleEndian.PutUint32(b[16:], 0) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := bytes.Clone(valid)
			tt.mutate(b)
			if _, err := parse(b); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, wide := range []bool{false, true} {
		f := fat(binary.BigEndian, wide, valid, thin(0x1000007, 3, binary.LittleEndian, true, superBlob(codeDirectory(2))))
		stride := 20
		if wide {
			stride = 32
		}
		for _, offset := range []int{4, 8, 12, 16, 20, 8 + stride + 8} {
			b := bytes.Clone(f)
			binary.BigEndian.PutUint32(b[offset:], 0xffffffff)
			if _, err := parse(b); err == nil {
				t.Fatalf("accepted fat offset %d", offset)
			}
		}
		b := bytes.Clone(f)
		binary.BigEndian.PutUint32(b[4:], 0)
		if _, err := parse(b); err == nil {
			t.Fatal("empty fat")
		}
		if _, err := parse(fat(binary.BigEndian, wide, valid, valid)); err == nil {
			t.Fatal("duplicate slice")
		}
	}
	if _, err := Read(context.Background(), nil, 32); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, bytes.NewReader(valid), int64(len(valid))); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), bytes.NewReader(nil), 32); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestMalformedDirectories(t *testing.T) {
	be := binary.BigEndian
	for _, tt := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"magic", func(b []byte) { b[0] = 0 }},
		{"length", func(b []byte) { be.PutUint32(b[4:], 5) }},
		{"unknownVersion", func(b []byte) { be.PutUint32(b[8:], 0x30000) }},
		{"oldVersion", func(b []byte) { be.PutUint32(b[8:], 0) }},
		{"shortHeader", func(b []byte) { be.PutUint32(b[8:], 0x20600) }},
		{"hashSize", func(b []byte) { b[36] = 0 }},
		{"algorithm", func(b []byte) { b[37] = 0 }},
		{"hashOffset", func(b []byte) { be.PutUint32(b[16:], 200) }},
		{"specialSlots", func(b []byte) { be.PutUint32(b[24:], 10) }},
		{"codeSlots", func(b []byte) { be.PutUint32(b[28:], 10) }},
		{"stringsOverlap", func(b []byte) { be.PutUint32(b[16:], 40) }},
		{"identifierOffset", func(b []byte) { be.PutUint32(b[20:], 0) }},
		{"identifierMissing", func(b []byte) { b[52] = 0 }},
		{"identifierUTF8", func(b []byte) { b[52] = 0xff }},
		{"teamOffset", func(b []byte) { be.PutUint32(b[48:], 500) }},
		{"unterminated", func(b []byte) {
			for i := 52; i < len(b); i++ {
				b[i] = 'a'
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := codeDirectory(2)
			tt.mutate(b)
			if _, err := directory(b); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	valid := superBlob(codeDirectory(2), codeDirectory(1))
	for _, offset := range []int{0, 4, 8, 12, 16, 24} {
		b := bytes.Clone(valid)
		be.PutUint32(b[offset:], 0xffffffff)
		if _, err := directories(b); err == nil {
			t.Fatalf("accepted offset %d", offset)
		}
	}
	if _, err := directories(nil); err == nil {
		t.Fatal("empty")
	}
	if _, err := directory(nil); err == nil {
		t.Fatal("empty cd")
	}
	b := bytes.Repeat([]byte{'a'}, maxString+100)
	if _, err := identifier(b, 1, 1); err == nil {
		t.Fatal("oversized string")
	}
}

func TestHostMachO(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native fixture")
	}
	f, err := os.Open("/bin/ls")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	arches, err := Read(t.Context(), f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arches {
		if len(a.CodeDirectories) == 0 {
			t.Fatal(a)
		}
		for _, d := range a.CodeDirectories {
			if d.SigningID != "com.apple.ls" {
				t.Fatal(d)
			}
		}
	}
}

func FuzzRead(f *testing.F) {
	f.Add(thin(0x100000c, 0, binary.LittleEndian, true, superBlob(codeDirectory(2))))
	f.Add(fat(binary.BigEndian, true, thin(7, 3, binary.LittleEndian, false, nil)))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = parse(b) })
}
