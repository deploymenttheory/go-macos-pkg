// Reading bill-of-materials files.
package bom

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// maxBOMSize bounds what Read will load: a BOM describes paths, not
// content, so even a package with a million files is tens of megabytes.
const maxBOMSize = 512 << 20

// block locates one block in the file.
type block struct {
	Address uint32
	Length  uint32
}

// BOM is a parsed bill of materials.
type BOM struct {
	data   []byte
	blocks []block
	vars   map[string]uint32 // variable name -> block index
	order  []string          // variable names in file order

	Version        uint32
	NumberOfBlocks uint32
}

// Read parses a bill of materials from r.
func Read(r io.Reader) (*BOM, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBOMSize+1))
	if err != nil {
		return nil, fmt.Errorf("bom: unable to read: %w", err)
	}
	if len(data) > maxBOMSize {
		return nil, fmt.Errorf("bom: file exceeds %d bytes", maxBOMSize)
	}
	return Parse(data)
}

// ReadFile parses the bill of materials at path.
func ReadFile(path string) (*BOM, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses a bill of materials held in memory.
func Parse(data []byte) (*BOM, error) {
	if len(data) < headerSize || string(data[:8]) != Magic {
		return nil, fmt.Errorf("%w: bad magic", ErrInvalid)
	}
	b := &BOM{
		data:           data,
		Version:        binary.BigEndian.Uint32(data[8:12]),
		NumberOfBlocks: binary.BigEndian.Uint32(data[12:16]),
		vars:           make(map[string]uint32),
	}
	indexOffset := binary.BigEndian.Uint32(data[16:20])
	indexLength := binary.BigEndian.Uint32(data[20:24])
	varsOffset := binary.BigEndian.Uint32(data[24:28])
	varsLength := binary.BigEndian.Uint32(data[28:32])

	// Block table.
	idx, err := b.slice(indexOffset, indexLength)
	if err != nil {
		return nil, fmt.Errorf("%w: block table: %v", ErrInvalid, err)
	}
	if len(idx) < 4 {
		return nil, fmt.Errorf("%w: block table too short", ErrInvalid)
	}
	count := binary.BigEndian.Uint32(idx[0:4])
	if uint64(count)*8+4 > uint64(len(idx)) {
		return nil, fmt.Errorf("%w: block table claims %d entries in %d bytes", ErrInvalid, count, len(idx))
	}
	b.blocks = make([]block, count)
	for i := range b.blocks {
		off := 4 + i*8
		b.blocks[i] = block{
			Address: binary.BigEndian.Uint32(idx[off : off+4]),
			Length:  binary.BigEndian.Uint32(idx[off+4 : off+8]),
		}
	}

	// Variables.
	vars, err := b.slice(varsOffset, varsLength)
	if err != nil {
		return nil, fmt.Errorf("%w: variables: %v", ErrInvalid, err)
	}
	if len(vars) < 4 {
		return nil, fmt.Errorf("%w: variables table too short", ErrInvalid)
	}
	n := binary.BigEndian.Uint32(vars[0:4])
	off := 4
	for i := uint32(0); i < n; i++ {
		if off+5 > len(vars) {
			return nil, fmt.Errorf("%w: variables table truncated", ErrInvalid)
		}
		index := binary.BigEndian.Uint32(vars[off : off+4])
		nameLen := int(vars[off+4])
		off += 5
		if off+nameLen > len(vars) {
			return nil, fmt.Errorf("%w: variable name truncated", ErrInvalid)
		}
		name := string(vars[off : off+nameLen])
		off += nameLen
		b.vars[name] = index
		b.order = append(b.order, name)
	}
	return b, nil
}

func (b *BOM) slice(offset, length uint32) ([]byte, error) {
	end := uint64(offset) + uint64(length)
	if end > uint64(len(b.data)) {
		return nil, fmt.Errorf("range %d+%d exceeds file size %d", offset, length, len(b.data))
	}
	return b.data[offset:end], nil
}

// Block returns the bytes of block i.
func (b *BOM) Block(i uint32) ([]byte, error) {
	if int(i) >= len(b.blocks) {
		return nil, fmt.Errorf("%w: block %d out of range (%d blocks)", ErrInvalid, i, len(b.blocks))
	}
	blk := b.blocks[i]
	return b.slice(blk.Address, blk.Length)
}

// Vars returns the variable names in file order.
func (b *BOM) Vars() []string { return append([]string(nil), b.order...) }

// Var returns the block index of a named variable.
func (b *BOM) Var(name string) (uint32, bool) {
	i, ok := b.vars[name]
	return i, ok
}

// tree is a parsed tree block.
type tree struct {
	Child     uint32
	BlockSize uint32
	PathCount uint32
}

func (b *BOM) readTree(index uint32) (*tree, error) {
	data, err := b.Block(index)
	if err != nil {
		return nil, err
	}
	if len(data) < treeSize || string(data[:4]) != treeMagic {
		return nil, fmt.Errorf("%w: block %d is not a tree", ErrInvalid, index)
	}
	return &tree{
		Child:     binary.BigEndian.Uint32(data[8:12]),
		BlockSize: binary.BigEndian.Uint32(data[12:16]),
		PathCount: binary.BigEndian.Uint32(data[16:20]),
	}, nil
}

// pathsEntry is one (value, key) pair in a Paths block.
type pathsEntry struct {
	Index0 uint32 // PathInfo block (leaf) or child Paths block (branch)
	Index1 uint32 // File block
}

// pathsBlock is a parsed Paths block.
type pathsBlock struct {
	IsLeaf   bool
	Forward  uint32
	Backward uint32
	Entries  []pathsEntry
}

func (b *BOM) readPaths(index uint32) (*pathsBlock, error) {
	data, err := b.Block(index)
	if err != nil {
		return nil, err
	}
	if len(data) < 12 {
		return nil, fmt.Errorf("%w: paths block %d too short", ErrInvalid, index)
	}
	p := &pathsBlock{
		IsLeaf:   binary.BigEndian.Uint16(data[0:2]) == 1,
		Forward:  binary.BigEndian.Uint32(data[4:8]),
		Backward: binary.BigEndian.Uint32(data[8:12]),
	}
	count := int(binary.BigEndian.Uint16(data[2:4]))
	if 12+count*8 > len(data) {
		return nil, fmt.Errorf("%w: paths block %d claims %d entries in %d bytes", ErrInvalid, index, count, len(data))
	}
	p.Entries = make([]pathsEntry, count)
	for i := range p.Entries {
		off := 12 + i*8
		p.Entries[i] = pathsEntry{
			Index0: binary.BigEndian.Uint32(data[off : off+4]),
			Index1: binary.BigEndian.Uint32(data[off+4 : off+8]),
		}
	}
	return p, nil
}

// leaves walks a tree and returns every leaf entry in order: descend to the
// leftmost leaf, then follow the forward chain.
func (b *BOM) leaves(t *tree) ([]pathsEntry, error) {
	if t.Child == 0 {
		return nil, nil
	}
	index := t.Child
	seen := map[uint32]bool{}
	for {
		if seen[index] {
			return nil, fmt.Errorf("%w: paths blocks form a cycle", ErrInvalid)
		}
		seen[index] = true
		p, err := b.readPaths(index)
		if err != nil {
			return nil, err
		}
		if p.IsLeaf {
			break
		}
		if len(p.Entries) == 0 {
			return nil, nil
		}
		index = p.Entries[0].Index0
	}
	var out []pathsEntry
	chain := map[uint32]bool{}
	for index != 0 {
		if chain[index] {
			return nil, fmt.Errorf("%w: leaf chain forms a cycle", ErrInvalid)
		}
		chain[index] = true
		p, err := b.readPaths(index)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Entries...)
		index = p.Forward
	}
	return out, nil
}

// Paths returns every entry of the Paths tree, with paths reconstructed
// from the parent chain, sorted by id (which is the order they were added).
func (b *BOM) Paths() ([]Entry, error) {
	treeIndex, ok := b.vars[VarPaths]
	if !ok {
		return nil, fmt.Errorf("%w: no Paths variable", ErrInvalid)
	}
	t, err := b.readTree(treeIndex)
	if err != nil {
		return nil, err
	}
	leaves, err := b.leaves(t)
	if err != nil {
		return nil, err
	}

	sizes64 := b.size64()

	type raw struct {
		entry Entry
		name  string
	}
	byID := make(map[uint32]*raw, len(leaves))
	var order []uint32
	for _, leaf := range leaves {
		info, err := b.Block(leaf.Index0)
		if err != nil {
			return nil, err
		}
		if len(info) < 8 {
			return nil, fmt.Errorf("%w: path info block too short", ErrInvalid)
		}
		id := binary.BigEndian.Uint32(info[0:4])
		recIndex := binary.BigEndian.Uint32(info[4:8])

		file, err := b.Block(leaf.Index1)
		if err != nil {
			return nil, err
		}
		if len(file) < 4 {
			return nil, fmt.Errorf("%w: file block too short", ErrInvalid)
		}
		parent := binary.BigEndian.Uint32(file[0:4])
		name := cstring(file[4:])

		e := Entry{ID: id, ParentID: parent}
		if err := b.fillRecord(&e, recIndex); err != nil {
			return nil, err
		}
		if s, ok := sizes64[id]; ok {
			e.Size = int64(s)
		}
		byID[id] = &raw{entry: e, name: name}
		order = append(order, id)
	}

	// Paths join upward through parent ids. The root is named "." with
	// parent 0; everything below is a bare name.
	var join func(id uint32, depth int) string
	join = func(id uint32, depth int) string {
		r, ok := byID[id]
		if !ok || depth > 4096 {
			return ""
		}
		if r.entry.ParentID == 0 {
			return r.name
		}
		parent := join(r.entry.ParentID, depth+1)
		if parent == "" {
			return r.name
		}
		return parent + "/" + r.name
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]Entry, 0, len(order))
	for _, id := range order {
		r := byID[id]
		r.entry.Path = join(id, 0)
		out = append(out, r.entry)
	}
	return out, nil
}

// fillRecord decodes a PathRecord block into e. Apple writes shorter
// records for some directories, so each field is read only if present.
func (b *BOM) fillRecord(e *Entry, index uint32) error {
	rec, err := b.Block(index)
	if err != nil {
		return err
	}
	if len(rec) < 1 {
		return fmt.Errorf("%w: empty path record", ErrInvalid)
	}
	e.Type = PathType(rec[0])
	if len(rec) >= 4 {
		e.Architecture = binary.BigEndian.Uint16(rec[2:4])
	}
	if len(rec) >= 6 {
		e.Mode = binary.BigEndian.Uint16(rec[4:6])
	}
	if len(rec) >= 14 {
		e.UID = binary.BigEndian.Uint32(rec[6:10])
		e.GID = binary.BigEndian.Uint32(rec[10:14])
	}
	if len(rec) >= 18 {
		e.ModTime = time.Unix(int64(binary.BigEndian.Uint32(rec[14:18])), 0).UTC()
	}
	if len(rec) >= 22 {
		e.Size = int64(binary.BigEndian.Uint32(rec[18:22]))
	}
	if len(rec) >= 27 {
		v := binary.BigEndian.Uint32(rec[23:27])
		if e.Type == TypeDevice {
			e.DevType = v
		} else {
			e.Checksum = v
		}
	}
	if len(rec) >= pathRecordSize {
		linkLen := binary.BigEndian.Uint32(rec[27:31])
		if e.Type == TypeLink && linkLen > 0 && int(pathRecordSize+linkLen) <= len(rec) {
			e.LinkTarget = cstring(rec[pathRecordSize : pathRecordSize+int(linkLen)])
		}
	}
	return nil
}

// size64 reads the Size64 tree, which records the sizes of files over
// 4 GiB: each leaf's key block holds a path id and its value block a 64-bit
// size. Absent or unreadable, it contributes nothing; the 32-bit size in
// the path record stands.
func (b *BOM) size64() map[uint32]uint64 {
	out := map[uint32]uint64{}
	treeIndex, ok := b.vars[VarSize64]
	if !ok {
		return out
	}
	t, err := b.readTree(treeIndex)
	if err != nil {
		return out
	}
	leaves, err := b.leaves(t)
	if err != nil {
		return out
	}
	for _, leaf := range leaves {
		key, err := b.Block(leaf.Index1)
		if err != nil || len(key) < 4 {
			continue
		}
		val, err := b.Block(leaf.Index0)
		if err != nil || len(val) < 8 {
			continue
		}
		out[binary.BigEndian.Uint32(key[0:4])] = binary.BigEndian.Uint64(val[0:8])
	}
	return out
}

// cstring returns the bytes up to the first NUL as a string.
func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
