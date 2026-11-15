// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

type heldChildBuildReader struct {
	messaging.MessageReader
	messaging.TermBoundReader
	entered chan struct{}
	release chan struct{}
}

func (r *heldChildBuildReader) BindTerm() error {
	close(r.entered)
	<-r.release
	return r.TermBoundReader.BindTerm()
}

type buildReadinessFacts struct {
	DeviceManagementFacts
	calls atomic.Int32
}

func (s *buildReadinessFacts) Tenants(context.Context) ([]string, error) {
	s.calls.Add(1)
	return []string{"other"}, nil
}

func (*buildReadinessFacts) ActiveProfiles(context.Context, string) ([]dmmodel.ActiveProfileRules, error) {
	return nil, nil
}

func (*buildReadinessFacts) Roster(context.Context, string) ([]dmmodel.DeviceRosterEntry, error) {
	return []dmmodel.DeviceRosterEntry{{DeviceToken: "lost", ProfileToken: "p", ExpectedSince: testBase}}, nil
}

// g1 is blocked before constructing its armer. No reconciliation, and hence no
// child signal, may happen before every worker has built.
func TestGroupReconcileWaitsForChildBuild(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := membershipFixture(t, r)
	require.True(t, p0.handleRuleFact(membershipRuleMessage(t, "other"), false))
	source := &buildReadinessFacts{}
	p0.DeviceManagement = source
	p0.cfg.TickInterval = 10 * time.Millisecond
	p1.cfg.TickInterval = 10 * time.Millisecond
	p1.cfg.CheckpointInterval = 20 * time.Millisecond
	p1.cfg.CheckpointEvents = 1
	held := &heldChildBuildReader{
		MessageReader:   p1.ResolvedEventsReader,
		TermBoundReader: p1.ResolvedEventsReader.(messaging.TermBoundReader),
		entered:         make(chan struct{}), release: make(chan struct{}),
	}
	p1.ResolvedEventsReader = held
	r.gate.Enter(func() bool { return true })
	done := make(chan error, 1)
	go func() { done <- p0.buildWorkers(p0.pctx()) }()
	released := false
	joined := false
	t.Cleanup(func() {
		if !released {
			close(held.release)
		}
		if !joined {
			<-done
		}
		p0.pcancel()
		for _, p := range p0.termWorkers() {
			p.readerWG.Wait()
		}
		p0.unbindTermReaders()
		p1.unbindTermReaders()
	})
	select {
	case <-held.entered:
	case err := <-done:
		joined = true
		t.Fatalf("build ended before child blocked: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("child build did not reach hold")
	}
	// Worker loops now start only after every worker has built, so no g0 tick (and
	// so no reconciliation) can happen while g1 is still building; the termReady
	// gate in runFactReconcile is the second line of defence. Observe both.
	require.Never(t, func() bool { return p0.factReconciling.Load() || source.calls.Load() != 0 }, 300*time.Millisecond, time.Millisecond,
		"reconciliation started or touched device-management while the child armer was still unbuilt")
	close(held.release)
	released = true
	err := <-done
	joined = true
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, ok, err := p0.RosterStore.Load(context.Background(), "other", "lost")
		return err == nil && ok && row.DeviceToken == "lost" && !p0.factReconciling.Load() && len(p1.armUpdates) == 0
	}, 5*time.Second, time.Millisecond, "ready workers never received the roster repair")
	r.publish(t, 1, 1, 1)
	require.Eventually(t, func() bool {
		row, ok, err := p1.Store.Load(context.Background(), p1.partitionID())
		return err == nil && ok && bytes.Contains(row.Payload, []byte(`"lost"`))
	}, 5*time.Second, 10*time.Millisecond, "child never checkpointed repaired membership")
	// Joining leaves the loop-owned state safe to inspect. A repaired row alone
	// would miss the old nil-armer path silently skipping its notification.
	p0.pcancel()
	for _, p := range p0.termWorkers() {
		p.readerWG.Wait()
	}
	require.NotNil(t, p1.armer)
	require.Len(t, p1.engine.ExpectedKeys(), 1, "roster repair never reached the child armer")
}
