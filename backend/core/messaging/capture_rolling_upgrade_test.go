// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// A ROLLING UPGRADE KEEPS A POD OF THE PREVIOUS BUILD RUNNING BESIDE THE NEW ONE.
//
// event-sources rolls one pod at a time with a surge pod (maxSurge 1, maxUnavailable 0), so
// for the whole roll an old pod still reads the capture stream with the old build's reader:
// it binds the ordinary durable name with the two-subject filter list, and whenever its
// durable goes away it re-binds by creating it and then moving the filter onto its list IN
// PLACE (its reconcileFilterSubject). These tests emulate that pod against the new reader on a
// real broker.

// oldBuildCaptureConfig is the durable config the previous build's capture reader asks for.
func oldBuildCaptureConfig(durable string) *nats.ConsumerConfig {
	return &nats.ConsumerConfig{
		Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		AckWait: time.Second, MaxDeliver: MaxDeliver, MaxAckPending: readerMaxAckPending,
		FilterSubjects: DeviceEventsSubjects("test"),
	}
}

// oldBuildBind is the previous build's bind of its capture durable: create it, then move the
// filter onto the list in place when the existing durable's differs.
func oldBuildBind(js nats.JetStreamContext, durable string) error {
	cfg := oldBuildCaptureConfig(durable)
	if _, err := js.AddConsumer(captureStreamOn("test"), cfg); err != nil {
		return err
	}
	info, err := js.ConsumerInfo(captureStreamOn("test"), durable)
	if err != nil {
		return err
	}
	have := slices.Clone(info.Config.FilterSubjects)
	slices.Sort(have)
	want := slices.Clone(cfg.FilterSubjects)
	slices.Sort(want)
	if info.Config.FilterSubject == "" && slices.Equal(have, want) {
		return nil
	}
	_, err = js.UpdateConsumer(captureStreamOn("test"), cfg)
	return err
}

// oldPod runs the previous build's self-heal until stopped: whenever the durable it read with
// is deleted or replaced (its fetch would fail on a deleted consumer), it re-binds.
type oldPod struct {
	js       nats.JetStreamContext
	durable  string
	mu       sync.Mutex
	rebinds  int
	refusals int
}

func (p *oldPod) run(stop <-chan struct{}, created time.Time) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Millisecond):
		}
		info, err := p.js.ConsumerInfo(captureStreamOn("test"), p.durable)
		if err == nil && info.Created.Equal(created) {
			continue // still the durable it bound
		}
		if err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
			continue
		}
		bindErr := oldBuildBind(p.js, p.durable)
		p.mu.Lock()
		p.rebinds++
		if bindErr != nil {
			p.refusals++
		}
		p.mu.Unlock()
		if info, err := p.js.ConsumerInfo(captureStreamOn("test"), p.durable); err == nil {
			created = info.Created
		}
	}
}

// GUARD: after a roll in which an old pod re-binds beside the new reader, the durable the new
// reader reads is still unfiltered, and the old pod could not put a filtered capture durable
// back. Under one shared durable name this fails: the old pod's in-place move re-filters the
// durable the new reader is bound to, and the reader, which reconciles only when it binds,
// never notices.
func TestARollingUpgradeBesideAnOldPodLeavesTheCaptureUnfiltered(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-roll")
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	legacy := DurableName("test", area, captureSuffix)
	_, err = js.AddStream(&nats.StreamConfig{
		Name: captureStreamOn("test"), Subjects: DeviceEventsSubjects("test"), Retention: nats.WorkQueuePolicy,
	})
	require.NoError(t, err)
	require.NoError(t, oldBuildBind(js, legacy), "the old build's durable could not be staged")
	staged, err := js.ConsumerInfo(captureStreamOn("test"), legacy)
	require.NoError(t, err)
	require.Len(t, staged.Config.FilterSubjects, 2, "the staged durable is not the old build's; the test would prove nothing")
	_, err = js.Publish(DeviceEventsSubject("test", "acme", "dev-1"), []byte("before"))
	require.NoError(t, err)

	// The old pod is running when the new one starts, and keeps running through the roll.
	old := &oldPod{js: js, durable: legacy}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); old.run(stop, staged.Created) }()
	t.Cleanup(func() { close(stop); <-done })

	nmgr, readers := recorderRig(t, srv, area, recordNothing, captureSuffix)
	reader := readers[0].(*natsReader)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, "before", string(msg.Value), "the message stored before the roll was not delivered")
	require.NoError(t, msg.Ack())

	// Let the old pod notice and re-bind, as its failing fetch would make it do.
	waitWithin(t, 5*time.Second, "the old pod to re-bind at least once", func() bool {
		old.mu.Lock()
		defer old.mu.Unlock()
		return old.rebinds >= 1
	})
	time.Sleep(500 * time.Millisecond)

	after, err := nmgr.js.ConsumerInfo(captureStreamOn("test"), reader.durable)
	require.NoError(t, err)
	old.mu.Lock()
	rebinds, refusals := old.rebinds, old.refusals
	old.mu.Unlock()
	t.Logf("after the roll: durable=%s filterSubject=%q filterSubjects=%v oldRebinds=%d oldRefusals=%d",
		reader.durable, after.Config.FilterSubject, after.Config.FilterSubjects, rebinds, refusals)
	require.Empty(t, after.Config.FilterSubject, "the old pod put a filter back on the new reader's durable")
	require.Empty(t, after.Config.FilterSubjects, "the old pod put the filter list back on the new reader's durable")
	if reader.durable != legacy {
		_, err := js.ConsumerInfo(captureStreamOn("test"), legacy)
		require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the old pod recreated its filtered durable beside the new one")
		require.Equal(t, rebinds, refusals, "an old pod's re-bind was accepted")
	}

	// And the new reader still delivers.
	_, err = js.Publish(DeviceEventsSubject("test", "acme", "dev-1"), []byte("after"))
	require.NoError(t, err)
	msg, err = reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, "after", string(msg.Value))
	require.NoError(t, msg.Ack())
}

// THE ROLLBACK, new build → previous build. Holds what the release note has to say: while the
// new build's durable exists, the previous build cannot bind at all (a work queue refuses its
// overlapping filtered durable), so going back is refused loudly rather than half-working; once
// that durable is deleted (the documented step), the previous build creates its own and is
// handed every message the stream holds, including those stored while no reader ran. Nothing
// is orphaned: a work queue's messages belong to the stream, not to a durable, and the new
// build's durable cannot coexist with the old one.
func TestARollbackToThePreviousBuildNeedsTheCaptureDurableDeleted(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-rollback")
	nmgr, readers := recorderRig(t, srv, area, recordNothing, captureSuffix)
	current := readers[0].(*natsReader).durable
	require.Equal(t, CaptureDurableName("test", area, captureSuffix), current)

	_, err := nmgr.js.Publish(DeviceEventsSubject("test", "acme", "dev-1"), []byte("read"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, err := readers[0].ReadMessage(ctx)
	require.NoError(t, err)
	require.NoError(t, msg.Ack())
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	require.NoError(t, nmgr.Stop(stopCtx), "the new build did not stop")

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	// Stored after the new build stopped and before the old one reads.
	for i := 0; i < 3; i++ {
		_, err = js.Publish(DeviceEventsSubject("test", "acme", "dev-1"), []byte("waiting"))
		require.NoError(t, err)
	}

	legacy := DurableName("test", area, captureSuffix)
	err = oldBuildBind(js, legacy)
	require.Error(t, err, "the previous build bound beside the new build's durable")
	t.Logf("previous build's bind with the new durable present: %v", err)
	_, err = js.ConsumerInfo(captureStreamOn("test"), legacy)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound)

	// The documented step, then the previous build again.
	require.NoError(t, js.DeleteConsumer(captureStreamOn("test"), current))
	require.NoError(t, oldBuildBind(js, legacy), "the previous build could not bind once the new durable was deleted")
	sub, err := js.PullSubscribe("", legacy, nats.Bind(captureStreamOn("test"), legacy))
	require.NoError(t, err)
	got, err := sub.Fetch(10, nats.MaxWait(3*time.Second))
	require.NoError(t, err)
	require.Len(t, got, 3, "the previous build was not handed what was stored while no reader ran")
	for _, m := range got {
		require.Equal(t, "waiting", string(m.Data))
		require.NoError(t, m.AckSync())
	}
}
