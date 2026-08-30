package lzbitmap

// Encoding, translated from libzbitmap's compressor.
//
// Each chunk covers at most MaxChunk bytes of input and is built in one
// pass over them, eight bytes at a time. For every group the encoder
// looks back over what it has already emitted for a run of eight bytes
// that resembles the next eight, and records a bitmap saying which of
// them differ: those become literals, the rest become back-references at
// the chosen period. It scores a candidate as the number of differing
// bytes plus the cost of the period itself (nothing if it repeats the
// last one, one byte up to 255, two beyond), so a slightly worse match
// that reuses the current period can win.
//
// The bitmaps are then counted, and the twelve most common are written
// into the chunk's last 17 bytes where a nibble can name them by index.
// The rest go inline in the second metadata area. Runs of the same
// bitmap number collapse into a repetition count.
//
// A chunk that does not come out smaller than its input is stored
// verbatim behind a plain header instead.

const (
	// possibleBitmaps is how many (bitmap, periodBytes) pairs exist: a
	// byte of bitmap and two bits of period count.
	possibleBitmaps = 1 << 10
	// initialPeriod is where each chunk's period starts.
	initialPeriod = 8
)

// bmprot indexes the use counter for a descriptor.
func bmprot(b bmap) int { return int(b.bitmap)<<2 | int(b.periodBytes) }

type encoder struct {
	src []byte
	pos int // next byte of src to consume, across all chunks

	// Per chunk.
	decmpLen int
	start    int // pos at the start of the chunk
	period   int
	bitmaps  []bmap
	periods  []int
	lit      []byte
	usecnts  [possibleBitmaps]int
	top      [bitmapCount]bmap
}

// Compress encodes src as an LZBITMAP stream.
func Compress(src []byte) ([]byte, error) {
	e := &encoder{src: src}
	out := make([]byte, 0, len(src)/2+64)
	out = append(out, Magic...)
	for {
		chunk := e.chunk()
		out = append(out, chunk...)
		if e.decmpLen == 0 {
			return out, nil
		}
	}
}

// chunk encodes the next chunk, returning its bytes. The final chunk is
// an empty one, which is how a stream ends.
func (e *encoder) chunk() []byte {
	e.start = e.pos
	e.decmpLen = len(e.src) - e.pos
	if e.decmpLen > MaxChunk {
		e.decmpLen = MaxChunk
	}
	if c := e.compressed(); c != nil && len(c) < e.decmpLen+chunkHdrSize {
		e.pos = e.start + e.decmpLen
		return c
	}
	// Not worth compressing: a plain header and the bytes themselves.
	out := make([]byte, chunkHdrSize, chunkHdrSize+e.decmpLen)
	putU24(out[0:], chunkHdrSize+e.decmpLen)
	putU24(out[3:], e.decmpLen)
	out = append(out, e.src[e.start:e.start+e.decmpLen]...)
	e.pos = e.start + e.decmpLen
	return out
}

// compressed builds the compressed form of the current chunk, or nil if
// the chunk is too short for one.
func (e *encoder) compressed() []byte {
	e.bitmaps = e.bitmaps[:0]
	e.periods = e.periods[:0]
	e.lit = e.lit[:0]
	e.usecnts = [possibleBitmaps]int{}
	e.top = [bitmapCount]bmap{}
	e.pos = e.start

	if e.start == 0 {
		// The first eight bytes of a stream have nothing to refer back
		// to, so they are literals under a bitmap of all ones.
		if e.decmpLen < 8 {
			return nil
		}
		e.bitmaps = append(e.bitmaps, bmap{bitmap: 0xff})
		e.periods = append(e.periods, 0)
		e.usecnts[bmprot(bmap{bitmap: 0xff})] = 1
		e.lit = append(e.lit, e.src[e.pos:e.pos+8]...)
		e.pos += 8
	}
	e.period = initialPeriod

	for e.pos < e.start+e.decmpLen {
		e.eightBytes()
	}
	return e.assemble()
}

// eightBytes chooses a pattern for the next group and emits its literals.
func (e *encoder) eightBytes() {
	n := e.start + e.decmpLen - e.pos
	if n > 8 {
		n = 8
	}
	best := e.findPattern(n)
	var bitmap byte
	for i := 0; i < n; i++ {
		if e.src[best+i] != e.src[e.pos+i] {
			bitmap |= 1 << i
		}
	}
	e.appendBitmap(bitmap, e.pos-best)
	for i := 0; i < 8 && e.pos < e.start+e.decmpLen; i++ {
		if bitmap&(1<<i) != 0 {
			e.lit = append(e.lit, e.src[e.pos])
		}
		e.pos++
	}
}

// load8 reads eight bytes as a little-endian word, zero beyond the input.
func (e *encoder) load8(at int) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		if at+i < len(e.src) {
			v |= uint64(e.src[at+i]) << (i * 8)
		}
	}
	return v
}

// differing counts how many of the eight byte lanes differ.
func differing(a, b uint64) int {
	x := a ^ b
	n := 0
	for i := 0; i < 8; i++ {
		if x&(0xff<<(i*8)) != 0 {
			n++
		}
	}
	return n
}

// findPattern picks the position to encode the next group against. The
// cost of a candidate is the bytes that differ plus the bytes needed to
// store its period, so repeating the current period is free.
func (e *encoder) findPattern(n int) int {
	needle := uint64(0)
	for i := 0; i < n; i++ {
		needle |= uint64(e.src[e.pos+i]) << (i * 8)
	}

	best := e.pos - e.period
	cost := differing(e.load8(best), needle)
	if cost <= 1 {
		return best
	}

	// One-byte periods: anything within the last 255 bytes.
	back := 0xff
	if e.pos < back {
		back = e.pos
	}
	split := e.pos - back
	for at := split; at <= e.pos-8; at++ {
		if d := differing(e.load8(at), needle) + 1; d < cost {
			best, cost = at, d
			if cost == 1 {
				return best
			}
		}
	}
	if cost == 2 {
		return best
	}

	// Two-byte periods reach 65535 back, which is too much ground to
	// cover exhaustively, so only positions starting with the same byte
	// are considered.
	back = 0xffff
	if e.pos < back {
		back = e.pos
	}
	for at := e.pos - back; at < split; {
		next := -1
		for i := at; i < split; i++ {
			if e.src[i] == e.src[e.pos] {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		if d := differing(e.load8(next), needle) + 2; d < cost {
			best, cost = next, d
			if cost == 2 {
				return best
			}
		}
		at = next + 1
	}
	return best
}

// appendBitmap records a descriptor and the period it introduces.
func (e *encoder) appendBitmap(bitmap byte, period int) {
	b := bmap{bitmap: bitmap}
	switch {
	case period == e.period:
		b.periodBytes = 0
	case period <= 0xff:
		b.periodBytes = 1
	default:
		b.periodBytes = 2
	}
	e.bitmaps = append(e.bitmaps, b)
	e.periods = append(e.periods, period)
	e.usecnts[bmprot(b)]++
	e.period = period
}

// chooseTop picks the twelve most used descriptors, which a nibble can
// then name by index, and marks them so they stay out of the inline
// area. Ties keep the order they were first seen in.
func (e *encoder) chooseTop() {
	type slot struct {
		b     bmap
		count int
		set   bool
	}
	var tops [bitmapCount]slot
	for _, b := range e.bitmaps {
		count := e.usecnts[bmprot(b)]
		i := 0
		for ; i < bitmapCount; i++ {
			if tops[i].set && tops[i].b == b {
				break // already there
			}
			if tops[i].count < count {
				copy(tops[i+1:], tops[i:bitmapCount-1])
				tops[i] = slot{b: b, count: count, set: true}
				break
			}
		}
	}
	for i, t := range tops {
		if t.count == 0 {
			e.top[i] = bmap{}
			continue
		}
		e.top[i] = t.b
		e.usecnts[bmprot(t.b)] = 0
	}
}

// numberFor is the nibble naming a descriptor: an index into the trailing
// bitmaps for a common one, or its period byte count for an inline one.
func (e *encoder) numberFor(i int) byte {
	b := e.bitmaps[i]
	if e.usecnts[bmprot(b)] == 0 {
		for j := 0; j < bitmapCount; j++ {
			if e.top[j] == b {
				return byte(j + bitmapBase)
			}
		}
	}
	return b.periodBytes
}

// assemble lays the chunk out: header, literals, the three metadata
// areas, then the trailing bitmaps.
func (e *encoder) assemble() []byte {
	e.chooseTop()

	// The periods, whenever one changes.
	var meta1 []byte
	for i, b := range e.bitmaps {
		if b.periodBytes == 0 {
			continue
		}
		meta1 = append(meta1, byte(e.periods[i]))
		if b.periodBytes == 2 {
			meta1 = append(meta1, byte(e.periods[i]>>8))
		}
	}

	// The descriptors that are not common enough to be named by index.
	var meta2 []byte
	for _, b := range e.bitmaps {
		if e.usecnts[bmprot(b)] != 0 {
			meta2 = append(meta2, b.bitmap)
		}
	}

	// The bitmap numbers, a nibble each, with runs collapsed.
	var meta3 []byte
	half := false
	put := func(v byte) {
		if !half {
			meta3 = append(meta3, v)
			half = true
		} else {
			meta3[len(meta3)-1] |= v << 4
			half = false
		}
	}
	for i := 0; i < len(e.bitmaps); {
		num := e.numberFor(i)
		repeat := 1
		for j := i + 1; j < len(e.bitmaps) && e.numberFor(j) == num; j++ {
			repeat++
		}
		put(num)
		if repeat <= 3 {
			for j := 1; j < repeat; j++ {
				put(num)
			}
		} else {
			// 0xf opens a count, which is nibbles summed onto a base of
			// four and must not end on 0xf.
			put(0xf)
			left := repeat - repeatBase
			last := byte(0xf)
			for left > 0 {
				last = byte(left)
				if left > 0xf {
					last = 0xf
				}
				put(last)
				left -= int(last)
			}
			if last == 0xf {
				put(0)
			}
		}
		i += repeat
	}

	trailing := make([]byte, bitmapByteCount)
	at, bit := 0, 0
	putBit := func(v byte) {
		trailing[at] |= v << bit
		bit++
		if bit == 8 {
			bit = 0
			at++
		}
	}
	for _, b := range e.top {
		for i := 0; i < 8; i++ {
			putBit(b.bitmap >> i & 1)
		}
		for i := 0; i < 2; i++ {
			putBit(b.periodBytes >> i & 1)
		}
	}

	off1 := cmpChunkHdrSize + len(e.lit)
	off2 := off1 + len(meta1)
	off3 := off2 + len(meta2)
	total := off3 + len(meta3) + bitmapByteCount

	out := make([]byte, cmpChunkHdrSize, total)
	putU24(out[0:], total)
	putU24(out[3:], e.decmpLen)
	putU24(out[6:], off1)
	putU24(out[9:], off2)
	putU24(out[12:], off3)
	out = append(out, e.lit...)
	out = append(out, meta1...)
	out = append(out, meta2...)
	out = append(out, meta3...)
	out = append(out, trailing...)
	return out
}
