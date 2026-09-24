// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// The ceiling is half the acknowledgement window, floored to whole seconds. Synthetic
// windows tell a derived ceiling from a copied literal (the 20s row), an inclusive bound
// from an exclusive one (the at-ceiling rows), and a floor from rounding up (the 61s row).
func TestCheckpointIntervalCeilingFollowsAckWait(t *testing.T) {
	cases := []struct {
		ackWait time.Duration
		secs    int
		ok      bool
	}{
		{20 * time.Second, 10, true},
		{20 * time.Second, 11, false},
		{61 * time.Second, 30, true},
		{61 * time.Second, 31, false},
		// A value whose Duration conversion overflows int64 must still be refused.
		{60 * time.Second, 1 << 40, false},
	}
	for _, tc := range cases {
		err := validateCheckpointInterval(tc.secs, tc.ackWait)
		if tc.ok && err != nil {
			t.Errorf("ackWait %s, interval %d: refused, want accepted: %v", tc.ackWait, tc.secs, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ackWait %s, interval %d: accepted, want refused — its held messages would outlive "+
				"the acknowledgement window and be redelivered", tc.ackWait, tc.secs)
		}
	}
}

// Validate is wired to the LIVE AckWait: the ceiling itself passes and one more second
// is refused with a message that says what the limit is.
func TestValidateRejectsAnIntervalPastTheAckWindow(t *testing.T) {
	ceiling := checkpointIntervalCeiling(messaging.AckWait)
	if ceiling <= 0 {
		t.Fatalf("the precondition: a positive ceiling, got %d from AckWait %s", ceiling, messaging.AckWait)
	}

	at := EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: ceiling}
	if err := at.Validate(); err != nil {
		t.Fatalf("an interval AT the ceiling (%d) was refused: %v", ceiling, err)
	}

	past := EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: ceiling + 1}
	err := past.Validate()
	if err == nil {
		t.Fatalf("an interval of %d, past the ceiling of %d, was accepted", ceiling+1, ceiling)
	}
	if !strings.Contains(err.Error(), "at most") {
		t.Errorf("the refusal does not state the limit: %v", err)
	}
}
