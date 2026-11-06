package pack

import "errors"

// ApplyDelta applies a git delta to base.
func ApplyDelta(base, delta []byte) ([]byte, error) {
	baseSize, n := varint(delta)
	if n == 0 {
		return nil, errors.New("delta: truncated header")
	}
	delta = delta[n:]
	resSize, n := varint(delta)
	if n == 0 {
		return nil, errors.New("delta: truncated header")
	}
	delta = delta[n:]
	if baseSize != uint64(len(base)) {
		return nil, errors.New("delta: base size mismatch")
	}
	if resSize > 1<<31 {
		return nil, errors.New("delta: result too large")
	}
	out := make([]byte, 0, resSize)
	for len(delta) > 0 {
		op := delta[0]
		delta = delta[1:]
		if op&0x80 != 0 {
			var off, size uint64
			for i := 0; i < 4; i++ {
				if op&(1<<i) != 0 {
					if len(delta) == 0 {
						return nil, errors.New("delta: truncated copy")
					}
					off |= uint64(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<i) != 0 {
					if len(delta) == 0 {
						return nil, errors.New("delta: truncated copy")
					}
					size |= uint64(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if off+size > uint64(len(base)) {
				return nil, errors.New("delta: copy out of range")
			}
			out = append(out, base[off:off+size]...)
		} else if op != 0 {
			n := int(op)
			if n > len(delta) {
				return nil, errors.New("delta: truncated insert")
			}
			out = append(out, delta[:n]...)
			delta = delta[n:]
		} else {
			return nil, errors.New("delta: invalid opcode 0")
		}
	}
	if uint64(len(out)) != resSize {
		return nil, errors.New("delta: result size mismatch")
	}
	return out, nil
}

// varint reads a little-endian base-128 size; returns 0 consumed on truncation.
func varint(b []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i, c := range b {
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, i + 1
		}
		shift += 7
		if shift > 63 {
			return 0, 0
		}
	}
	return 0, 0
}
