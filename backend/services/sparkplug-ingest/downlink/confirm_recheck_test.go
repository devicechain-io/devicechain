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

// 🔴 UNCHANGED IS DECIDED AT THE END OF THE WINDOW, FROM A FRESH READ. The "already at the value"
// mark is read when the command is published, before its entry joins the mirror, so a contrary
// report can land between the two and be seen by no one. At the end of the window the value must
// still be the requested one, on the same table: a node that reported something else since, or died
// or re-birthed (its table gone), is not "unchanged". The control confirms.
func TestUnchangedIsRecheckedAtTheEndOfTheWindow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(f *fakeSource)
		unchanged bool
	}{
		{"CONTROL still at the value on the same table", func(f *fakeSource) {}, true},
		{"a contrary value was reported after the mark was read", func(f *fakeSource) { f.lastVal = host.WireValue{Kind: 'i', N: 9} }, false},
		{"the node died or re-birthed: its table is gone", func(f *fakeSource) { f.tableGone = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			clk := r.useClock()
			r.src.atValue, r.src.lastAt, r.src.lastVal = true, clk.now().Add(-time.Hour), i8(t, 5)
			r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
			tc.mutate(r.src)
			clk.advance(121 * time.Second)
			r.d.sweep()
			res := r.responseTo(t, "c")
			assert.Equal(t, tc.unchanged, res.env.Success)
			if !tc.unchanged {
				assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach)
			}
		})
	}
}

// An indeterminate rebirth is awaited like any other, and the node's birth confirms it.
func TestAnIndeterminateRebirthIsConfirmedByTheNodesBirth(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.src.publishErr = &host.PublishIndeterminateError{Err: errBoom}
	r.live("acme", "node-3", "rb", CommandRebirth, ``, nil)
	assert.Empty(t, r.responses.of("rb"))
	clk.advance(time.Second)
	r.report(nodeBirth(clk.now(), 5))
	assert.True(t, r.responseTo(t, "rb").env.Success)
}

// A response that cannot be published is retried only while the command can still be answered: past
// its own expiry command-delivery has ended the row itself, so the entry is dropped (and its record
// deleted) instead of being retried for as long as the bucket would keep it.
func TestAResponseIsNotRetriedPastTheCommandsExpiry(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	exp := clk.now().Add(10 * time.Minute)
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), map[string]string{
		cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandExpires: exp.Format(time.RFC3339)})
	r.responses.mu.Lock()
	r.responses.err = errBoom
	r.responses.mu.Unlock()
	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	require.Eventually(t, func() bool {
		r.d.mu.Lock()
		defer r.d.mu.Unlock()
		e := r.d.entries[AwaitingKey("acme", "c")]
		return e != nil && !e.resolving && !e.retryAt.IsZero()
	}, confirmWait, 5*time.Millisecond)

	clk.advance(9 * time.Minute)
	r.d.sweep()
	r.d.mu.Lock()
	assert.Len(t, r.d.entries, 1, "an answerable command was abandoned")
	r.d.mu.Unlock()

	clk.advance(2 * time.Minute) // past the command's expiry
	r.d.sweep()
	r.d.mu.Lock()
	assert.Empty(t, r.d.entries, "a response is still being retried for a command that has expired")
	r.d.mu.Unlock()
	assert.Zero(t, r.store.len(), "the abandoned command's record was left behind")
}
