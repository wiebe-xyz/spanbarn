package spanhist

import (
	"encoding/binary"
	"errors"
)

// encodingV1 is the first byte of every encoded histogram. A later layout
// gets a new version byte, so stored rows stay readable.
const encodingV1 byte = 1

// ErrCorrupt reports an encoded histogram that cannot be read.
var ErrCorrupt = errors.New("spanhist: corrupt encoding")

// Observe folds one duration in microseconds into h.
func (h *Hist) Observe(us int64) {
	h.Add(Bin{Index: Index(us), Count: 1, Min: us, Max: us})
}

// Encode returns h as bytes: the version byte, then per non-empty bin its
// index (one byte), count (uvarint), min (varint) and max-min (uvarint).
// An empty histogram encodes to the version byte alone.
func (h Hist) Encode() []byte {
	out := make([]byte, 0, 1+len(h.bins)*8)
	out = append(out, encodingV1)
	for _, b := range h.bins {
		out = append(out, byte(b.Index))
		out = binary.AppendUvarint(out, uint64(b.Count))
		out = binary.AppendVarint(out, b.Min)
		out = binary.AppendUvarint(out, uint64(b.Max-b.Min))
	}
	return out
}

// Decode reads a histogram written by Encode. Bins must appear in ascending
// index order, as Encode writes them.
func Decode(buf []byte) (Hist, error) {
	var h Hist
	if len(buf) == 0 || buf[0] != encodingV1 {
		return h, ErrCorrupt
	}
	buf = buf[1:]
	last := -1
	for len(buf) > 0 {
		idx := int(buf[0])
		if idx >= Bins || idx <= last {
			return Hist{}, ErrCorrupt
		}
		buf = buf[1:]
		count, n := binary.Uvarint(buf)
		if n <= 0 || count == 0 {
			return Hist{}, ErrCorrupt
		}
		buf = buf[n:]
		lo, n := binary.Varint(buf)
		if n <= 0 {
			return Hist{}, ErrCorrupt
		}
		buf = buf[n:]
		width, n := binary.Uvarint(buf)
		if n <= 0 {
			return Hist{}, ErrCorrupt
		}
		buf = buf[n:]
		h.bins = append(h.bins, Bin{Index: idx, Count: int64(count), Min: lo, Max: lo + int64(width)})
		h.total += int64(count)
		last = idx
	}
	return h, nil
}

// MergeEncoded decodes a and b, merges them and encodes the result. It backs
// the SQL function that folds a histogram into a stored row on conflict.
func MergeEncoded(a, b []byte) ([]byte, error) {
	ha, err := Decode(a)
	if err != nil {
		return nil, err
	}
	hb, err := Decode(b)
	if err != nil {
		return nil, err
	}
	ha.Merge(hb)
	return ha.Encode(), nil
}
