// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 🔴 A REVEAL SHOWS ITS VALUE ONCE. The second Show — a re-rendered screen, a key pressed
// twice, a second presenter — gets ErrRevealConsumed and its callback is never called.
func TestARevealShowsItsValueOnce(t *testing.T) {
	r := NewReveal([]byte("the-seed-value"))
	var got []string
	if err := r.Show(func(v []byte) { got = append(got, string(v)) }); err != nil {
		t.Fatal(err)
	}
	if !r.Consumed() {
		t.Fatal("a shown reveal is not consumed")
	}
	if err := r.Show(func(v []byte) { got = append(got, string(v)) }); !errors.Is(err, ErrRevealConsumed) {
		t.Fatalf("second Show: %v", err)
	}
	if len(got) != 1 || got[0] != "the-seed-value" {
		t.Fatalf("shown %q", got)
	}
}

// The value handed to Show is cleared when the callback returns, so a callback that kept
// the slice keeps nothing.
func TestARevealClearsTheValueAfterShowingIt(t *testing.T) {
	r := NewReveal([]byte("the-seed-value"))
	var kept []byte
	_ = r.Show(func(v []byte) { kept = v })
	if strings.Contains(string(kept), "seed") {
		t.Fatalf("the slice still holds %q after Show returned", kept)
	}
}

// A discarded reveal shows nothing, ever; and nil reveals are spent.
func TestADiscardedRevealShowsNothing(t *testing.T) {
	r := NewReveal([]byte("the-seed-value"))
	r.Discard()
	if err := r.Show(func([]byte) { t.Fatal("a discarded reveal showed its value") }); !errors.Is(err, ErrRevealConsumed) {
		t.Fatalf("Show after Discard: %v", err)
	}
	r.Discard()
	var nilReveal *Reveal
	if !nilReveal.Consumed() || nilReveal.Show(func([]byte) { t.Fatal("nil showed") }) == nil {
		t.Fatal("a nil reveal is not spent")
	}
	nilReveal.Discard()
}

// Every printable form of a reveal is a placeholder, and encoding one is an error.
func TestARevealNeverPrintsOrEncodesItsValue(t *testing.T) {
	r := NewReveal([]byte("the-seed-value"))
	offer := RevealOffer{Subject: RevealSuperuserSeed, Reveal: r}
	for _, s := range []string{fmt.Sprint(r), fmt.Sprintf("%v %+v %#v %s %q %x", r, r, r, r, r, r),
		fmt.Sprintf("%+v %#v", offer, offer)} {
		if strings.Contains(s, "seed-value") {
			t.Fatalf("printed form %q holds the value", s)
		}
	}
	if _, err := json.Marshal(r); err == nil {
		t.Fatal("a reveal marshalled")
	}
	if _, err := json.Marshal(offer); err == nil {
		t.Fatal("an offer carrying a reveal marshalled")
	}
	if r.Consumed() {
		t.Fatal("printing a reveal consumed it")
	}
}
