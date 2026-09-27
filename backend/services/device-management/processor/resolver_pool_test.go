// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
)

// barrierApi holds every event's first lookup until release is closed, and records the most
// lookups it held at once: the number of events the resolver pool was resolving together.
type barrierApi struct {
	instantApi
	release   chan struct{}
	cur, peak *atomic.Int64
}

func (a barrierApi) DevicesByToken(ctx context.Context, tokens []string) ([]*dmodel.Device, error) {
	n := a.cur.Add(1)
	for {
		p := a.peak.Load()
		if n <= p || a.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-a.release
	a.cur.Add(-1)
	return a.instantApi.DevicesByToken(ctx, tokens)
}

// ProfileResolutionByDeviceType is declared here as well as on instantApi so this test does
// not depend on the benchmark file's double: an unscoped profile declaring no metrics.
func (barrierApi) ProfileResolutionByDeviceType(context.Context, uint) (*dmodel.ProfileResolution, error) {
	return dmodel.NewProfileResolution(dmodel.ProfileScope{}, nil), nil
}

// The resolver pool resolves as many events at once as it is sized to, and an unsized pool
// runs the configuration's default of 10. Thirty events wait at the pool; each resolver holds
// its event at the first lookup, so the number held at once is the number of resolvers.
//
// On a tree whose pool was fixed at five, the unsized arm holds 5.
func TestTheResolverPoolResolvesAsManyEventsAtOnceAsItIsSized(t *testing.T) {
	for _, arm := range []struct{ set, want int }{{0, 10}, {3, 3}, {1, 1}} {
		t.Run(fmt.Sprintf("set=%d", arm.set), func(t *testing.T) {
			nmgr, _ := startGateNats(t)
			api := barrierApi{release: make(chan struct{}), cur: &atomic.Int64{}, peak: &atomic.Int64{}}
			released := false
			release := func() {
				if !released {
					released = true
					close(api.release)
				}
			}
			t.Cleanup(release)

			acks := newAckLog()
			reader := &sliceReader{}
			for i := 0; i < 30; i++ {
				body, err := esproto.MarshalUnresolvedEvent(benchMeasurement(i))
				if err != nil {
					t.Fatal(err)
				}
				src := gateSource(nmgr, i, uint64(i+1), acks)
				src.Value = body
				reader.msgs = append(reader.msgs, src)
			}
			iproc := newGateProcessor(t, nmgr, reader, api)
			iproc.resolverCount = arm.set
			if err := iproc.Initialize(context.Background()); err != nil {
				t.Fatalf("initialize: %v", err)
			}
			// Resolvers reports the width the pool runs, including the default it applied
			// when none was set.
			if got := iproc.Resolvers(); got != arm.want {
				t.Errorf("Resolvers() after Initialize = %d, want %d", got, arm.want)
			}
			if err := iproc.Start(context.Background()); err != nil {
				t.Fatalf("start: %v", err)
			}

			// Wait for the count held at once to stop rising.
			deadline := time.Now().Add(5 * time.Second)
			last, since := int64(-1), time.Now()
			for time.Now().Before(deadline) {
				if p := api.peak.Load(); p != last {
					last, since = p, time.Now()
				} else if p > 0 && time.Since(since) > 200*time.Millisecond {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got := api.peak.Load(); got != int64(arm.want) {
				t.Errorf("the pool resolved %d events at once, want %d", got, arm.want)
			}

			release()
			stopWithin(t, iproc, 10*time.Second)
			// Counterweight: the held events were real ones, and every one went through.
			if n := acks.count(); n != 30 {
				t.Errorf("%d of 30 sources were acked after the pool was released", n)
			}
		})
	}
}
