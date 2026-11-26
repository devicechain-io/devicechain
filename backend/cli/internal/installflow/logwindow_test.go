// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow_test

import (
	"fmt"
	"io"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

func TestLogWindowAddressesLinesByAbsoluteNumber(t *testing.T) {
	f := newFixture(t, installflow.Options{LogLines: 10})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	lw, err := f.hub.LogWriter("", "tool", installflow.LogInfo)
	must(t, err)
	text := func(w installflow.LogWindow) string {
		s := ""
		for _, l := range w.Lines {
			s += l.Text.String() + ","
		}
		return s
	}

	if w := f.hub.LogWindow(0, 4, true); len(w.Lines) != 0 || w.First != 0 || w.End != 0 {
		t.Fatalf("empty ring: %+v", w)
	}
	for i := range 25 {
		_, _ = io.WriteString(lw, fmt.Sprintf("l%d\n", i))
	}
	// 25 lines logged, 10 kept: 15..24.
	cases := []struct {
		name     string
		from     uint64
		n        int
		tail     bool
		wantFrom uint64
		want     string
	}{
		{"tail", 0, 3, true, 22, "l22,l23,l24,"},
		{"aged out moves forward", 2, 3, false, 15, "l15,l16,l17,"},
		{"middle", 18, 2, false, 18, "l18,l19,"},
		{"past the newest moves back", 40, 3, false, 22, "l22,l23,l24,"},
		{"window larger than the ring", 0, 50, false, 15, "l15,l16,l17,l18,l19,l20,l21,l22,l23,l24,"},
		{"zero rows", 18, 0, false, 18, ""},
	}
	for _, c := range cases {
		w := f.hub.LogWindow(c.from, c.n, c.tail)
		if w.First != 15 || w.End != 25 {
			t.Errorf("%s: first %d end %d, want 15 25", c.name, w.First, w.End)
		}
		if w.From != c.wantFrom || text(w) != c.want {
			t.Errorf("%s: from %d %q, want %d %q", c.name, w.From, text(w), c.wantFrom, c.want)
		}
		if len(w.Lines) > c.n {
			t.Errorf("%s: %d lines for a window of %d", c.name, len(w.Lines), c.n)
		}
	}
}
