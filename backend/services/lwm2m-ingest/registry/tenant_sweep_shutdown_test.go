// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A sweep releases the table lock before ending sessions. The term must join those
// callbacks before releasing its lease, even when cancellation arrives mid-callback.
func TestStartTenantSweepWaitJoinsSessionEnd(t *testing.T) {
	parked := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newTestRegistry(okResolver(), &fakeEmitter{}, &testClock{t: time.Unix(1_700_000_000, 0)}, func(o *Options) {
		o.TenantDeleted = func(tenant string) bool { return tenant == testBinding.Tenant }
		o.OnSessionEnd = func(string, uint64) {
			close(parked)
			<-release
		}
	})
	defer r.Stop()
	defer close(release)
	result, _, _, _ := r.Register(ctx, "dev-1", testBinding, 86400)
	require.Equal(t, RegisterOK, result)
	require.True(t, r.StartTenantSweep(ctx, time.Millisecond))
	select {
	case <-parked:
	case <-time.After(2 * time.Second):
		t.Fatal("tenant sweep did not park in OnSessionEnd")
	}

	cancel()
	r.Stop()
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("Wait returned immediately while tenant sweep OnSessionEnd was still blocked")
	case <-time.After(200 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after tenant sweep OnSessionEnd was released")
	}
}

// Shutdown must wake the loop without waiting for the production sweep interval.
func TestRunTenantSweepReturnsPromptly(t *testing.T) {
	for _, shutdown := range []string{"cancel", "stop"} {
		t.Run(shutdown, func(t *testing.T) {
			r := newTestRegistry(okResolver(), &fakeEmitter{}, &testClock{t: time.Unix(1_700_000_000, 0)}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer r.Stop()
			done := make(chan struct{})
			go func() { r.RunTenantSweep(ctx, time.Hour); close(done) }()
			if shutdown == "cancel" {
				cancel()
			} else {
				r.Stop()
				r.Stop() // Stop remains safe to repeat.
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("RunTenantSweep did not return promptly after %s", shutdown)
			}
		})
	}
}

func TestStartTenantSweepRefusesStoppedRegistry(t *testing.T) {
	r := newTestRegistry(okResolver(), &fakeEmitter{}, &testClock{t: time.Unix(1_700_000_000, 0)}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Stop()
	require.False(t, r.StartTenantSweep(ctx, time.Hour))
	r.Wait()
}
