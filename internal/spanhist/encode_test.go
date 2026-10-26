package spanhist

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestEncodeRoundTrips(t *testing.T) {
	for _, d := range [][]int64{
		nil,
		{0},
		{-7, 0, 1, 3, 3, 900, 1_000_000},
		{math.MaxInt64 / 2, 12, 12, 12},
	} {
		h := fold(d)
		got, err := Decode(h.Encode())
		if err != nil {
			t.Fatalf("decode %v: %v", d, err)
		}
		if !reflect.DeepEqual(got.bins, h.bins) || got.Count() != h.Count() {
			t.Errorf("round trip of %v: got %+v, want %+v", d, got.bins, h.bins)
		}
	}
}

func TestObserveMatchesFold(t *testing.T) {
	d := []int64{5, 5, 80, 81, 20_000}
	var h Hist
	for _, us := range d {
		h.Observe(us)
	}
	if want := fold(d); !reflect.DeepEqual(h, want) {
		t.Errorf("Observe built %+v, fold built %+v", h, want)
	}
}

// Merging encoded halves equals folding everything at once, in either order.
func TestMergeEncodedEqualsFold(t *testing.T) {
	a := []int64{1, 2, 300, 300, 4000}
	b := []int64{2, 299, 5_000_000}
	whole := fold(append(append([]int64{}, a...), b...)).Encode()
	for _, pair := range [][2][]int64{{a, b}, {b, a}} {
		got, err := MergeEncoded(fold(pair[0]).Encode(), fold(pair[1]).Encode())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, whole) {
			t.Errorf("merge = %v, want %v", got, whole)
		}
	}
}

func TestDecodeRejectsCorruptInput(t *testing.T) {
	good := fold([]int64{10, 2000}).Encode()
	for name, buf := range map[string][]byte{
		"empty":         {},
		"wrong version": append([]byte{9}, good[1:]...),
		"truncated":     good[:len(good)-1],
		"index too big": {encodingV1, Bins, 1, 0, 0},
		"out of order":  {encodingV1, 5, 1, 0, 0, 5, 1, 0, 0},
		"zero count":    {encodingV1, 5, 0, 0, 0},
	} {
		if _, err := Decode(buf); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
	if _, err := MergeEncoded(good, []byte{}); err == nil {
		t.Error("MergeEncoded accepted a corrupt right side")
	}
	if _, err := MergeEncoded(nil, good); err == nil {
		t.Error("MergeEncoded accepted a corrupt left side")
	}
}
