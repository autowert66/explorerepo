// Package pack parses git packfiles from a stream and resolves deltas.
package pack

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"errors"
	"fmt"
	"hash"
	"io"

	"explorerepo/internal/object"
)

// Raw is a fully resolved object.
type Raw struct {
	OID  object.OID
	Type object.Type
	Data []byte
}

type Options struct {
	// Base looks up a REF_DELTA base that is not in the pack (e.g. in the local store).
	Base func(object.OID) (object.Type, []byte, bool)
	// MaxObjectSize rejects objects whose declared size exceeds it (0 = 1 GiB).
	MaxObjectSize int64
}

const (
	typOfsDelta = 6
	typRefDelta = 7
)

// reader is a byte-at-a-time friendly buffered reader that tracks the stream
// offset and lazily SHA-1s consumed bytes (so the trailer can be excluded).
type reader struct {
	src    io.Reader
	buf    []byte
	pos    int
	end    int
	hashed int
	base   int64
	h      hash.Hash
}

func newReader(src io.Reader) *reader {
	return &reader{src: src, buf: make([]byte, 64*1024), h: sha1.New()}
}

func (r *reader) flushHash() {
	r.h.Write(r.buf[r.hashed:r.pos])
	r.hashed = r.pos
}

func (r *reader) fill() error {
	r.flushHash()
	r.base += int64(r.end)
	r.pos, r.end, r.hashed = 0, 0, 0
	for {
		n, err := r.src.Read(r.buf)
		if n > 0 {
			r.end = n
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (r *reader) ReadByte() (byte, error) {
	if r.pos == r.end {
		if err := r.fill(); err != nil {
			return 0, err
		}
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.pos == r.end {
		if err := r.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, r.buf[r.pos:r.end])
	r.pos += n
	return n, nil
}

func (r *reader) offset() int64 { return r.base + int64(r.pos) }

type stored struct {
	typ  object.Type
	data []byte
}

type pendingRef struct {
	off   int64
	delta []byte
}

// Parse reads a packfile from src, calling sink for every resolved object.
func Parse(src io.Reader, opt Options, sink func(Raw) error) error {
	if opt.MaxObjectSize == 0 {
		opt.MaxObjectSize = 1 << 30
	}
	r := newReader(src)
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return fmt.Errorf("pack: reading header: %w", noEOF(err))
	}
	if string(hdr[:4]) != "PACK" {
		return errors.New("pack: bad signature")
	}
	ver := be32(hdr[4:])
	if ver != 2 && ver != 3 {
		return fmt.Errorf("pack: unsupported version %d", ver)
	}
	count := be32(hdr[8:])

	byOffset := map[int64]stored{}
	byOID := map[object.OID]int64{}
	pending := map[object.OID][]pendingRef{} // keyed by base oid
	var zr io.ReadCloser

	emit := func(off int64, t object.Type, data []byte) error {
		oid := object.Hash(t, data)
		byOffset[off] = stored{t, data}
		byOID[oid] = off
		if err := sink(Raw{oid, t, data}); err != nil {
			return err
		}
		// resolve any REF_DELTAs waiting for this object
		work := []object.OID{oid}
		for len(work) > 0 {
			base := work[0]
			work = work[1:]
			waiting := pending[base]
			if len(waiting) == 0 {
				continue
			}
			delete(pending, base)
			bo := byOID[base]
			bs := byOffset[bo]
			for _, p := range waiting {
				res, err := ApplyDelta(bs.data, p.delta)
				if err != nil {
					return err
				}
				o := object.Hash(bs.typ, res)
				if _, dup := byOID[o]; dup {
					continue
				}
				byOID[o] = p.off
				byOffset[p.off] = stored{bs.typ, res}
				if err := sink(Raw{o, bs.typ, res}); err != nil {
					return err
				}
				work = append(work, o)
			}
		}
		return nil
	}

	for i := uint32(0); i < count; i++ {
		off := r.offset()
		b, err := r.ReadByte()
		if err != nil {
			return fmt.Errorf("pack: object %d: %w", i, noEOF(err))
		}
		typ := int(b>>4) & 7
		size := uint64(b & 0x0f)
		shift := uint(4)
		for b&0x80 != 0 {
			if b, err = r.ReadByte(); err != nil {
				return fmt.Errorf("pack: object %d: %w", i, noEOF(err))
			}
			size |= uint64(b&0x7f) << shift
			shift += 7
			if shift > 63 {
				return errors.New("pack: object size overflow")
			}
		}
		if size > uint64(opt.MaxObjectSize) {
			return fmt.Errorf("pack: object of %d bytes exceeds limit", size)
		}
		var baseOff int64
		var baseOID object.OID
		switch typ {
		case typOfsDelta:
			c, err := r.ReadByte()
			if err != nil {
				return noEOF(err)
			}
			v := int64(c & 0x7f)
			for c&0x80 != 0 {
				if c, err = r.ReadByte(); err != nil {
					return noEOF(err)
				}
				v = ((v + 1) << 7) | int64(c&0x7f)
				if v < 0 {
					return errors.New("pack: bad ofs-delta offset")
				}
			}
			baseOff = off - v
			if v <= 0 || baseOff < 0 {
				return errors.New("pack: bad ofs-delta offset")
			}
		case typRefDelta:
			if _, err := io.ReadFull(r, baseOID[:]); err != nil {
				return noEOF(err)
			}
		case 1, 2, 3, 4:
		default:
			return fmt.Errorf("pack: unknown object type %d", typ)
		}

		// inflate exactly one zlib stream
		if zr == nil {
			zr, err = zlib.NewReader(r)
		} else {
			err = zr.(zlib.Resetter).Reset(r, nil)
		}
		if err != nil {
			return fmt.Errorf("pack: object %d: %w", i, err)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(zr, data); err != nil {
			return fmt.Errorf("pack: object %d: inflate: %w", i, noEOF(err))
		}
		var one [1]byte
		if n, err := zr.Read(one[:]); n != 0 || err != io.EOF {
			if err == nil || err == io.EOF {
				err = errors.New("object longer than declared size")
			}
			return fmt.Errorf("pack: object %d: %w", i, err)
		}

		switch typ {
		case 1, 2, 3, 4:
			if err := emit(off, object.Type(typ), data); err != nil {
				return err
			}
		case typOfsDelta:
			base, ok := byOffset[baseOff]
			if !ok {
				return errors.New("pack: ofs-delta base not found")
			}
			res, err := ApplyDelta(base.data, data)
			if err != nil {
				return fmt.Errorf("pack: %w", err)
			}
			if err := emit(off, base.typ, res); err != nil {
				return err
			}
		case typRefDelta:
			if bo, ok := byOID[baseOID]; ok {
				base := byOffset[bo]
				res, err := ApplyDelta(base.data, data)
				if err != nil {
					return fmt.Errorf("pack: %w", err)
				}
				if err := emit(off, base.typ, res); err != nil {
					return err
				}
			} else if opt.Base != nil {
				if t, bd, ok := opt.Base(baseOID); ok {
					res, err := ApplyDelta(bd, data)
					if err != nil {
						return fmt.Errorf("pack: %w", err)
					}
					if err := emit(off, t, res); err != nil {
						return err
					}
					continue
				}
				pending[baseOID] = append(pending[baseOID], pendingRef{off, data})
			} else {
				pending[baseOID] = append(pending[baseOID], pendingRef{off, data})
			}
		}
	}

	// unresolved REF_DELTAs may still have bases in the local store
	for base, list := range pending {
		if opt.Base == nil {
			return fmt.Errorf("pack: unresolved delta base %s", base)
		}
		t, bd, ok := opt.Base(base)
		if !ok {
			return fmt.Errorf("pack: unresolved delta base %s", base)
		}
		for _, p := range list {
			res, err := ApplyDelta(bd, p.delta)
			if err != nil {
				return fmt.Errorf("pack: %w", err)
			}
			if err := sink(Raw{object.Hash(t, res), t, res}); err != nil {
				return err
			}
		}
	}

	// trailer
	r.flushHash()
	want := r.h.Sum(nil)
	var trailer [20]byte
	if _, err := io.ReadFull(r, trailer[:]); err != nil {
		return fmt.Errorf("pack: reading trailer: %w", noEOF(err))
	}
	if !bytes.Equal(trailer[:], want) {
		return errors.New("pack: checksum mismatch")
	}
	return nil
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func noEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}
