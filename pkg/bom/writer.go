// Writing bill-of-materials files.
//
// The layout follows what pkgbuild's mkbom writes, as decoded from its
// output (see the block dump in the tests): a null block 0; BomInfo; the
// Paths tree and its leaves; the HLIndex tree with one entry per inode
// (every path, except that a hard-link set contributes only its last
// member in path order), each pointing at an empty 64-byte tree of its
// own; the VIndex record and
// its empty tree; the Size64 tree; then, per path, its record, name and
// index blocks. Only the block *contents* are copied from Apple; the
// placement of blocks in the file is ours, since nothing reads a Bom by
// offset.
package bom

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Builder assembles a bill of materials.
type Builder struct {
	entries []Entry
	seen    map[string]bool
}

// NewBuilder returns an empty builder. Add the root "." first.
func NewBuilder() *Builder {
	return &Builder{seen: map[string]bool{}}
}

// Add records one path. Path is "." for the root or "./a/b" beneath it;
// parents must be added before their children, as a directory walk does.
// Mode must include the type bits. ID and ParentID are assigned here.
func (b *Builder) Add(e Entry) error {
	p := normalizePath(e.Path)
	if b.seen[p] {
		return fmt.Errorf("bom: duplicate path %s", p)
	}
	if p != "." {
		parent := parentOf(p)
		if !b.seen[parent] {
			return fmt.Errorf("bom: parent of %s not added yet", p)
		}
	} else if len(b.entries) != 0 {
		return fmt.Errorf("bom: the root must be added first")
	}
	e.Path = p
	b.seen[p] = true
	b.entries = append(b.entries, e)
	return nil
}

func normalizePath(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "." || p == "./" {
		return "."
	}
	if !strings.HasPrefix(p, "./") {
		p = "./" + strings.TrimPrefix(p, "/")
	}
	return p
}

func parentOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "."
	}
	if i == 1 { // "./x"
		return "."
	}
	return p[:i]
}

func baseOf(p string) string {
	if p == "." {
		return "."
	}
	return p[strings.LastIndexByte(p, '/')+1:]
}

// blockWriter accumulates blocks and hands out their indices.
type blockWriter struct {
	blocks [][]byte
}

func (bw *blockWriter) add(b []byte) uint32 {
	bw.blocks = append(bw.blocks, b)
	return uint32(len(bw.blocks) - 1)
}

// reserve claims an index whose content is set later.
func (bw *blockWriter) reserve() uint32 { return bw.add(nil) }

func (bw *blockWriter) set(i uint32, b []byte) { bw.blocks[i] = b }

// treeBlock encodes a "tree" header pointing at a Paths block.
func treeBlock(child, blockSize, pathCount uint32) []byte {
	b := make([]byte, treeSize)
	copy(b, treeMagic)
	binary.BigEndian.PutUint32(b[4:], 1)
	binary.BigEndian.PutUint32(b[8:], child)
	binary.BigEndian.PutUint32(b[12:], blockSize)
	binary.BigEndian.PutUint32(b[16:], pathCount)
	return b
}

// encodePaths encodes a leaf or branch Paths block of blockSize bytes.
func encodePaths(isLeaf bool, entries []pathsEntry, forward, backward, blockSize uint32) []byte {
	b := make([]byte, blockSize)
	if isLeaf {
		binary.BigEndian.PutUint16(b[0:], 1)
	}
	binary.BigEndian.PutUint16(b[2:], uint16(len(entries)))
	binary.BigEndian.PutUint32(b[4:], forward)
	binary.BigEndian.PutUint32(b[8:], backward)
	for i, e := range entries {
		binary.BigEndian.PutUint32(b[12+i*8:], e.Index0)
		binary.BigEndian.PutUint32(b[16+i*8:], e.Index1)
	}
	return b
}

// pathsPerBlock is how many entries fit in a 4096-byte Paths block.
const (
	pathsBlockSize = 4096
	pathsPerBlock  = (pathsBlockSize - 12) / 8
)

// buildTree writes a tree over the given leaf entries, chunking them into
// leaves of blockSize and adding a branch level when there is more than
// one leaf. It returns the tree block index.
func buildTree(bw *blockWriter, entries []pathsEntry, blockSize uint32, pathCount uint32) uint32 {
	perBlock := int((blockSize - 12) / 8)
	if len(entries) == 0 {
		leaf := bw.add(encodePaths(true, nil, 0, 0, blockSize))
		return bw.add(treeBlock(leaf, blockSize, pathCount))
	}
	// Reserve leaf indices first so forward/backward links can be set.
	var leafIdx []uint32
	for i := 0; i < len(entries); i += perBlock {
		leafIdx = append(leafIdx, bw.reserve())
	}
	for n, start := range leafIdx {
		_ = start
		end := (n + 1) * perBlock
		if end > len(entries) {
			end = len(entries)
		}
		var fwd, back uint32
		if n+1 < len(leafIdx) {
			fwd = leafIdx[n+1]
		}
		if n > 0 {
			back = leafIdx[n-1]
		}
		bw.set(leafIdx[n], encodePaths(true, entries[n*perBlock:end], fwd, back, blockSize))
	}
	root := leafIdx[0]
	if len(leafIdx) > 1 {
		// One branch level: an entry per leaf, keyed by the leaf's last
		// entry's key, as a B+tree does. A second level would only be
		// needed past ~260,000 paths.
		var branch []pathsEntry
		for n, idx := range leafIdx {
			end := (n + 1) * perBlock
			if end > len(entries) {
				end = len(entries)
			}
			branch = append(branch, pathsEntry{Index0: idx, Index1: entries[end-1].Index1})
		}
		root = bw.add(encodePaths(false, branch, 0, 0, blockSize))
	}
	return bw.add(treeBlock(root, blockSize, pathCount))
}

// Build writes the bill of materials to w.
func (b *Builder) Build(w io.Writer) error {
	if len(b.entries) == 0 || b.entries[0].Path != "." {
		return fmt.Errorf("bom: no root entry")
	}
	// Assign ids in insertion order and resolve parents.
	idOf := make(map[string]uint32, len(b.entries))
	for i := range b.entries {
		b.entries[i].ID = uint32(i + 1)
		idOf[b.entries[i].Path] = b.entries[i].ID
	}
	for i := range b.entries {
		if b.entries[i].Path == "." {
			b.entries[i].ParentID = 0
		} else {
			b.entries[i].ParentID = idOf[parentOf(b.entries[i].Path)]
		}
	}

	bw := &blockWriter{}
	bw.add(nil) // block 0 is always the null block

	// BomInfo: version, path count (+1, as mkbom counts), one info entry
	// carrying the total file bytes.
	var totalBytes uint64
	for _, e := range b.entries {
		if e.Type == TypeFile {
			totalBytes += uint64(e.Size)
		}
	}
	info := make([]byte, 12+16)
	binary.BigEndian.PutUint32(info[0:], 1)
	binary.BigEndian.PutUint32(info[4:], uint32(len(b.entries)+1))
	binary.BigEndian.PutUint32(info[8:], 1)
	binary.BigEndian.PutUint32(info[20:], uint32(totalBytes))
	infoIdx := bw.add(info)

	// Per-path blocks: record, file (name), index.
	type placed struct {
		record, file, index uint32
	}
	place := make([]placed, len(b.entries))
	for i, e := range b.entries {
		rec := bw.add(encodeRecord(e))
		name := baseOf(e.Path)
		file := make([]byte, 4+len(name)+1)
		binary.BigEndian.PutUint32(file, e.ParentID)
		copy(file[4:], name)
		fileIdx := bw.add(file)
		idx := make([]byte, 8)
		binary.BigEndian.PutUint32(idx, e.ID)
		binary.BigEndian.PutUint32(idx[4:], rec)
		place[i] = placed{record: rec, file: fileIdx, index: bw.add(idx)}
	}

	// Paths tree: the leaves form a B-tree keyed by the File block, that
	// is by (parent id, name), and lsbom stops reading at the first entry
	// out of that order. Sort accordingly: a directory's children come
	// together, by name, after every entry with a lower parent id.
	order := make([]int, len(b.entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		ex, ey := b.entries[order[x]], b.entries[order[y]]
		if ex.ParentID != ey.ParentID {
			return ex.ParentID < ey.ParentID
		}
		return baseOf(ex.Path) < baseOf(ey.Path)
	})
	leaves := make([]pathsEntry, 0, len(order))
	for _, i := range order {
		leaves = append(leaves, pathsEntry{Index0: place[i].index, Index1: place[i].file})
	}
	pathsTree := buildTree(bw, leaves, pathsBlockSize, uint32(len(b.entries)))

	// HLIndex: one entry per inode, keyed by its record block, each
	// valued with an empty 64-byte tree of its own: the shape mkbom
	// writes. A hard-link set is one inode, and mkbom keeps the last
	// member in path order (see the links probe: a.txt, b.txt, d/c.txt
	// share an inode and only d/c.txt is indexed; sidecars are always
	// indexed).
	lastOfSet := map[uint64]int{}
	for _, i := range order {
		if k := b.entries[i].HardLinkKey; k != 0 && !b.entries[i].Sidecar {
			lastOfSet[k] = i
		}
	}
	hl := make([]pathsEntry, 0, len(order))
	for _, i := range order {
		if k := b.entries[i].HardLinkKey; k != 0 && !b.entries[i].Sidecar && lastOfSet[k] != i {
			continue
		}
		sub := buildTree(bw, nil, 64, 0)
		val := make([]byte, 4)
		binary.BigEndian.PutUint32(val, sub)
		key := make([]byte, 4)
		binary.BigEndian.PutUint32(key, place[i].record)
		hl = append(hl, pathsEntry{Index0: bw.add(val), Index1: bw.add(key)})
	}
	hlTree := buildTree(bw, hl, pathsBlockSize, uint32(len(hl)))

	// VIndex: a record pointing at an empty 128-byte tree.
	vTree := buildTree(bw, nil, 128, 0)
	vindex := make([]byte, 13)
	binary.BigEndian.PutUint32(vindex[0:], 1)
	binary.BigEndian.PutUint32(vindex[4:], vTree)
	vIdx := bw.add(vindex)

	// Size64: sizes over 4 GiB, keyed by the entry's record block, the way
	// HLIndex is keyed and the way pkgbuild writes it. Not by path id:
	// lsbom resolves the key against the record, so a Bom keyed by id
	// reads back with the truncated 32-bit size from the record itself.
	var big []pathsEntry
	for _, i := range order {
		e := b.entries[i]
		if e.Type == TypeFile && e.Size > 0xFFFFFFFF {
			val := make([]byte, 8)
			binary.BigEndian.PutUint64(val, uint64(e.Size))
			key := make([]byte, 4)
			binary.BigEndian.PutUint32(key, place[i].record)
			big = append(big, pathsEntry{Index0: bw.add(val), Index1: bw.add(key)})
		}
	}
	size64Tree := buildTree(bw, big, pathsBlockSize, uint32(len(big)))

	// Variables, in Apple's order.
	vars := []struct {
		name string
		idx  uint32
	}{
		{VarBomInfo, infoIdx}, {VarPaths, pathsTree}, {VarHLIndex, hlTree}, {VarVIndex, vIdx}, {VarSize64, size64Tree},
	}
	var varsBuf bytes.Buffer
	binary.Write(&varsBuf, binary.BigEndian, uint32(len(vars)))
	for _, v := range vars {
		binary.Write(&varsBuf, binary.BigEndian, v.idx)
		varsBuf.WriteByte(byte(len(v.name)))
		varsBuf.WriteString(v.name)
	}

	// Lay the file out: header, block data from offset 512, then the
	// block table (with an empty free list), then the variables.
	var data bytes.Buffer
	data.Write(make([]byte, 512))
	addrs := make([]block, len(bw.blocks))
	for i, blk := range bw.blocks {
		if i == 0 || len(blk) == 0 {
			continue
		}
		addrs[i] = block{Address: uint32(data.Len()), Length: uint32(len(blk))}
		data.Write(blk)
	}
	indexOffset := data.Len()
	binary.Write(&data, binary.BigEndian, uint32(len(bw.blocks)))
	for _, a := range addrs {
		binary.Write(&data, binary.BigEndian, a.Address)
		binary.Write(&data, binary.BigEndian, a.Length)
	}
	binary.Write(&data, binary.BigEndian, uint32(0)) // free list: empty
	indexLength := data.Len() - indexOffset
	varsOffset := data.Len()
	data.Write(varsBuf.Bytes())

	out := data.Bytes()
	copy(out[0:8], Magic)
	binary.BigEndian.PutUint32(out[8:], 1)
	binary.BigEndian.PutUint32(out[12:], uint32(len(bw.blocks)-1))
	binary.BigEndian.PutUint32(out[16:], uint32(indexOffset))
	binary.BigEndian.PutUint32(out[20:], uint32(indexLength))
	binary.BigEndian.PutUint32(out[24:], uint32(varsOffset))
	binary.BigEndian.PutUint32(out[28:], uint32(varsBuf.Len()))
	_, err := w.Write(out)
	return err
}

// encodeRecord writes a path record the way mkbom sizes them: 31 bytes for
// a directory, 35 for a file (four trailing zero bytes), and for a symlink
// the target with its NUL plus eight trailing zero bytes.
func encodeRecord(e Entry) []byte {
	var b bytes.Buffer
	b.WriteByte(byte(e.Type))
	b.WriteByte(1)
	binary.Write(&b, binary.BigEndian, e.Architecture)
	binary.Write(&b, binary.BigEndian, e.Mode)
	binary.Write(&b, binary.BigEndian, e.UID)
	binary.Write(&b, binary.BigEndian, e.GID)
	var mtime uint32
	if !e.ModTime.IsZero() && e.ModTime.Unix() > 0 {
		mtime = uint32(e.ModTime.Unix())
	}
	binary.Write(&b, binary.BigEndian, mtime)
	size := e.Size
	if size > 0xFFFFFFFF {
		size = 0xFFFFFFFF
	}
	binary.Write(&b, binary.BigEndian, uint32(size))
	b.WriteByte(1)
	switch e.Type {
	case TypeDevice:
		binary.Write(&b, binary.BigEndian, e.DevType)
	default:
		binary.Write(&b, binary.BigEndian, e.Checksum)
	}
	if e.Sidecar {
		// 31 bytes: the directory shape, whatever the owner's type.
		binary.Write(&b, binary.BigEndian, uint32(0))
		return b.Bytes()
	}
	switch e.Type {
	case TypeLink:
		binary.Write(&b, binary.BigEndian, uint32(len(e.LinkTarget)+1))
		b.WriteString(e.LinkTarget)
		b.WriteByte(0)
		b.Write(make([]byte, 8))
	case TypeFile:
		binary.Write(&b, binary.BigEndian, uint32(0))
		b.Write(make([]byte, 4))
	default:
		binary.Write(&b, binary.BigEndian, uint32(0))
	}
	return b.Bytes()
}
