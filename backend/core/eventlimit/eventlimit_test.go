// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventlimit

import (
	"reflect"
	"testing"
)

// The number is published (the device-facing docs and the HTTP 400 text both say 256), so it
// is pinned as a literal: a change here must be a decision, not a drift.
func TestTheLimitIsTheDocumentedNumber(t *testing.T) {
	if MaxReadingsPerEvent != 256 {
		t.Fatalf("MaxReadingsPerEvent = %d; the published limit is 256 readings per event", MaxReadingsPerEvent)
	}
}

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		n    int
		want []int
	}{
		{0, nil},
		{1, []int{1}},
		{256, []int{256}},
		{257, []int{256, 1}},
		{512, []int{256, 256}},
		{600, []int{256, 256, 88}},
	} {
		in := make([]int, c.n)
		for i := range in {
			in[i] = i
		}
		pieces := Split(in)
		var sizes []int
		var joined []int
		for _, p := range pieces {
			sizes = append(sizes, len(p))
			joined = append(joined, p...)
		}
		if !reflect.DeepEqual(sizes, c.want) {
			t.Errorf("Split(%d) sizes = %v, want %v", c.n, sizes, c.want)
		}
		if c.n > 0 && !reflect.DeepEqual(joined, in) {
			t.Errorf("Split(%d) pieces do not concatenate back to the input in order", c.n)
		}
	}
	if Split[int](nil) != nil {
		t.Error("a nil slice yields no pieces")
	}
}

// An append to one piece must not overwrite the next: the pieces share s's backing array,
// and without the capacity limit the first piece's spare capacity IS the second piece.
func TestSplitPiecesAreCapacityLimited(t *testing.T) {
	in := make([]int, 300)
	for i := range in {
		in[i] = i
	}
	pieces := Split(in)
	_ = append(pieces[0], -1)
	if pieces[1][0] != 256 {
		t.Fatalf("appending to the first piece overwrote the second: pieces[1][0] = %d, want 256", pieces[1][0])
	}
}
