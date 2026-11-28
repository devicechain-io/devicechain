// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installtui"
)

// The escrow receipt says an artifact opens only when this run showed it does.
func TestTheEscrowReceiptSaysCheckedOnlyOfWhatWasOpened(t *testing.T) {
	for outcome, want := range map[string]installtui.EscrowOutcome{
		"written":  installtui.EscrowWrittenAndChecked,
		"verified": installtui.EscrowOpened,
		"present, not opened (no passphrase was given to open it)": installtui.EscrowFoundNotOpened,
		"": installtui.EscrowFoundNotOpened,
	} {
		st := &bootstrap.State{EscrowOutcome: outcome}
		if got := receiptEscrowOutcome(st, nil); got != want {
			t.Errorf("%q: receipt outcome %v, want %v", outcome, got, want)
		}
		if got := receiptEscrowOutcome(st, errors.New("failed")); got != installtui.EscrowFoundNotOpened {
			t.Errorf("%q after a failed run: receipt outcome %v", outcome, got)
		}
	}
}
