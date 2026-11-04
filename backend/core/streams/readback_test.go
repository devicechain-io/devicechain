// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package streams

import "testing"

// A writer that reads its own writes back (settles envelope rounds) must be a declared writer of
// the stream, and the set is pinned as a literal: the read is a grant (core/natsauth), so adding a
// name sends its author to the field's comment.
func TestReadBackWritersAreDeclaredWritersAndPinned(t *testing.T) {
	type pair struct{ suffix, area string }
	var got []pair
	for _, s := range All {
		for _, a := range s.ReadBackWriters {
			got = append(got, pair{s.Suffix, a})
			if !Writes(s.Suffix, a) {
				t.Errorf("stream %q lists %q as a read-back writer but it is not a declared writer", s.Suffix, a)
			}
		}
	}
	want := []pair{{InboundEvents, "event-sources"}, {ResolvedEvents, "device-management"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("read-back writers = %v, want %v", got, want)
	}
}
