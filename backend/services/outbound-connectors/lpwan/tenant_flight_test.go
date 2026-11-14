// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

type flightAck struct {
	fakeAck
	progress func() error
}

func (a *flightAck) InProgress() error { return a.progress() }

func TestSingleTenantDownlinkUsesSpareCapacity(t *testing.T) {
	r := newRig(t, nil)
	require.True(t, r.a.inFlight.TryAcquire("acme"))
	ack := &flightAck{progress: func() error {
		t.Fatal("a lone tenant must not be throttled")
		return nil
	}}
	r.a.handle(context.Background(), messaging.NewConsumedMessage(subject, envelope(t, goodParams), 1, routed(), ack))
	require.True(t, ack.acked)
	require.Len(t, r.send.calls, 1)
	require.Zero(t, r.m.get("throttled"))
}

func TestThrottledDownlinkKeepsDeliveryOrAnswersUnsent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "resumes", true: "final_progress_failure"}[fail], func(t *testing.T) {
			r := newRig(t, nil)
			require.True(t, r.a.inFlight.TryAcquire("acme"))
			require.True(t, r.a.inFlight.TryAcquire("other"))
			beats := 0
			ack := &flightAck{progress: func() error {
				beats++
				require.Zero(t, r.claim.calls, "waiting must not claim the command")
				require.Empty(t, r.send.calls)
				if fail {
					return errors.New("broker unavailable")
				}
				r.a.inFlight.Release("other")
				return nil
			}}
			msg := messaging.NewConsumedMessage(subject, envelope(t, goodParams), messaging.MaxDeliver, routed(), ack)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r.a.handle(ctx, msg)
			require.Positive(t, beats, "must renew AckWait on the current delivery")
			require.True(t, ack.acked)
			require.Equal(t, 1, r.m.get("throttled"))
			require.Len(t, r.resp.envs, 1)
			if fail {
				require.Zero(t, r.claim.calls)
				require.Empty(t, r.send.calls)
				require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[0].Reach)
				require.False(t, r.resp.envs[0].Success)
			} else {
				require.Equal(t, 1, r.claim.calls)
				require.Len(t, r.send.calls, 1)
				require.True(t, r.resp.envs[0].Success)
			}
		})
	}
}
