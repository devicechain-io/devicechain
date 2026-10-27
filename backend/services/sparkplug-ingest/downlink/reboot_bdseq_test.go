// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// A reboot is proved by two birth sequence numbers that are both known and DIFFERENT. A birth, or a
// baseline, without one proves nothing, so it must not confirm; the same number is a rebirth.
func TestARebootNeedsTwoKnownDifferentBdSeqs(t *testing.T) {
	at := time.Now()
	for _, tc := range []struct {
		name          string
		before, after bool
		old, next     uint64
		want          bool
	}{
		{"missing-after", true, false, 7, 0, false},
		{"missing-before", false, true, 0, 7, false},
		{"both-missing", false, false, 0, 0, false},
		{"missing-before-after-zero", false, true, 0, 0, false},
		{"same", true, true, 7, 7, false},
		{"different", true, true, 7, 8, true},
		{"different-from-zero", true, true, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := entry{a: Awaiting{Kind: KindReboot, PublishedAt: at, BdSeq: tc.old, HasBdSeq: tc.before}}
			out, ok := e.observe(host.Report{Kind: host.ReportNodeBirth, ReceivedAt: at.Add(time.Second),
				BdSeq: tc.next, HasBdSeq: tc.after}, 0)
			if tc.want {
				require.True(t, ok)
				assert.True(t, out.success)
				return
			}
			assert.False(t, ok && out != nil && out.success, "an unproven reboot was confirmed")
		})
	}
}

// Through the dispatcher: an NBIRTH that carries no bdSeq neither confirms the reboot nor ends it;
// the command stays awaiting, and the window ends it UNANSWERED (TIMEOUT) - never SUCCESSFUL.
func TestARebootNotProvedByABirthWithoutABdSeqEndsUnanswered(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.src.bdSeq = 7
	r.live("acme", "node-3", "boot", CommandReboot, ``, map[string]string{
		cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandImpact: "DISRUPTIVE"})
	require.Len(t, r.src.published(), 1)

	clk.advance(5 * time.Second)
	r.report(host.Report{Kind: host.ReportNodeBirth, Target: nodeT, Via: "NBIRTH", ReceivedAt: clk.now()})
	r.noResponse(t, "boot")

	clk.advance(601 * time.Second)
	r.d.sweep()
	res := r.responseTo(t, "boot")
	assert.False(t, res.env.Success)
	assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach)
}
