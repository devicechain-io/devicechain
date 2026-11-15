// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPartialChannelAnswersDoNotHoldTheWholeBatch(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1"), queued(2, "c2"), queued(3, "c3")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.DispatchDeferred = prometheus.NewCounter(prometheus.CounterOpts{Name: "test_dispatch_deferred_total"})
	proc.Presence = presence.WithPullChannels(presence.Empty{}, pullSet{
		pull: map[string]bool{"dev-c1": false, "dev-c3": true}, err: errors.New("partial")})
	proc.sweepLocked(context.Background())
	if got := testutil.ToFloat64(proc.DispatchDeferred); got != 1 {
		t.Fatalf("deferred counter = %v, want 1", got)
	}
	if writer.count() != 1 || len(api.markedSent) != 1 || api.markedSent[0] != 1 || len(api.held) != 1 || api.held[0] != 3 {
		t.Fatalf("published %d, sent %v, held %v", writer.count(), api.markedSent, api.held)
	}
}

func TestTheHoldReconcilerNeverReleasesAnUndecidedChannel(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, presence.WithPullChannels(presence.Empty{}, pullSet{err: errors.New("down")}))
	proc.reconcileHolds(context.Background())
	if len(api.releasedHolds) != 0 {
		t.Fatalf("undecided channel released: %v", api.releasedHolds)
	}
}
