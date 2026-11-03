// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
)

// 🔴 THE BROKER'S OWN ANSWER, NOT A UNIT TEST'S. These run the real grants on a real
// permission-enforcing broker (permtest) and show the three halves of "services only verify":
//
//   - a service on a broker nobody has reconciled stops, by name, and asks the broker for nothing
//     it may not have (no permission violation);
//   - a service that DID try to create or reshape a stream would be refused by the broker, which is
//     what makes the verify-only behaviour a property of the grant and not of the service's good
//     manners (the negative control);
//   - the administrator, over its own login, is not.

func deployedOver(t *testing.T, b *permtest.Broker, instance, area string) *messaging.NatsManager {
	t.Helper()
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: area, Deployed: true, Readiness: core.NewReadinessGate()}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.Readiness.MarkReadyWithoutAuthSurface()
	ms.InstanceConfiguration.Infrastructure.Nats = b.NatsConfiguration(area)
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil {
			c.Close()
		}
	})
	return nmgr
}

func TestAServiceOnAnUnreconciledBrokerStopsByNameAndAsksForNothingItMayNotHave(t *testing.T) {
	instance := fmt.Sprintf("verify%d", time.Now().UnixNano())
	b := permtest.Start(t, instance, permtest.WithoutReconcile())
	svc := deployedOver(t, b, instance, "event-sources")

	_, err := svc.NewWriter(streams.InboundEvents)
	var drift *messaging.BrokerDriftError
	require.True(t, errors.As(err, &drift), "got %v", err)
	require.Equal(t, messaging.StreamName(instance, streams.InboundEvents), drift.Name)
	require.Contains(t, err.Error(), "dcctl broker reconcile")
	b.RequireNoViolations(t) // it looked (STREAM.INFO) and did not try to create

	// Once the administrator has reconciled, the same call succeeds, over the same login.
	b.Reconcile(t, "event-sources")
	svc2 := deployedOver(t, b, instance, "event-sources")
	_, err = svc2.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	b.RequireNoViolations(t)
}

// The negative control: the broker itself refuses every shaping verb an area login tries, so a
// service could not create a stream or reconfigure one even if it were written to.
func TestTheBrokerRefusesAnAreaThatTriesToShapeAStream(t *testing.T) {
	instance := fmt.Sprintf("shape%d", time.Now().UnixNano())
	b := permtest.Start(t, instance)
	nc := b.Connect(t, "device-state")
	js, err := nc.JetStream()
	require.NoError(t, err)

	name := messaging.StreamName(instance, streams.ResolvedEvents)
	info, err := js.StreamInfo(name)
	require.NoError(t, err, "an area may look at a stream it reads")

	// A stream it only reads, reshaped to republish into a subject someone trusts.
	cfg := info.Config
	cfg.RePublish = &nats.RePublish{Source: ">", Destination: "forged.>"}
	_, err = js.UpdateStream(&cfg)
	require.Error(t, err, "device-state reshaped resolved-events")

	// A new stream capturing another login's inbox.
	_, err = js.AddStream(&nats.StreamConfig{Name: "capture", Subjects: []string{"_INBOX_device-management.>"}})
	require.Error(t, err, "device-state created a stream capturing another area's inbox")

	// Deleting one.
	require.Error(t, js.DeleteStream(name), "device-state deleted a stream")

	require.NotEmpty(t, b.Violations(), "the broker did not log the refusals, so it was not what refused them")
	got, err := js.StreamInfo(name)
	require.NoError(t, err)
	require.Nil(t, got.Config.RePublish, "the stream was reshaped")
}

// The administrator over its own login can do what the areas cannot, and nothing outside the
// catalog.
func TestTheAdministratorShapesTheCatalogAndNotAnythingElse(t *testing.T) {
	instance := fmt.Sprintf("admin%d", time.Now().UnixNano())
	b := permtest.Start(t, instance)

	ms := &core.Microservice{InstanceId: instance}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = b.AdminNatsConfiguration()
	admin := messaging.NewBrokerAdmin(ms)
	require.NoError(t, admin.ExecuteInitialize(context.Background()))
	t.Cleanup(admin.Close)
	js, err := admin.Conn().JetStream()
	require.NoError(t, err)

	// Not a stream of the catalog: refused, even for the administrator.
	_, err = js.AddStream(&nats.StreamConfig{Name: "somewhere-else", Subjects: []string{"somewhere.else"}})
	require.Error(t, err)
	// ...and it cannot put a message into any stream.
	_, err = js.Publish(messaging.ConcreteSubjectFor(instance, "acme", streams.ResolvedEvents, "dev1"), []byte("{}"))
	require.Error(t, err, "the administrator wrote into a stream")
	require.NotEmpty(t, b.Violations())
}

// 🔴 A STREAM LOST WITH EVERY REPLICA IS REPORTED, NOT RECREATED. This is the in-process half of the
// HA rig's `streamloss` case, on the real grants: the administrator (the login the rig drives through
// the broker's CLI) can delete the capture stream, its only reader then stops by name and asks for
// nothing it may not have, the stream is STILL missing afterwards, and the administrator brings it
// back. The negative control is the check itself, run against the restored state, where it must fail.
func TestAStreamLostWithEveryReplicaIsReportedByNameNotRecreatedAndTheAdministratorRestoresIt(t *testing.T) {
	instance := fmt.Sprintf("lost%d", time.Now().UnixNano())
	b := permtest.Start(t, instance)
	stream := messaging.StreamName(instance, streams.DeviceEventsCapture)

	admin, err := nats.Connect(fmt.Sprintf("nats://%s:%d", b.Host, b.Port),
		nats.UserInfo(natsauth.AdminUser, permtest.Password(natsauth.AdminUser)),
		nats.CustomInboxPrefix(messaging.InboxPrefix(config.NatsAdminPrincipal)))
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	adminJS, err := admin.JetStream()
	require.NoError(t, err)

	present := func() bool { _, err := adminJS.StreamInfo(stream); return err == nil }
	// stayedLost is the rig's three-part check: the reader stopped with the named error, it did so
	// asking for nothing it may not have, and the stream is still missing.
	stayedLost := func() bool {
		svc := deployedOver(t, b, instance, "event-sources")
		_, err := svc.NewReader(streams.DeviceEventsCapture)
		var drift *messaging.BrokerDriftError
		return errors.As(err, &drift) && drift.Name == stream &&
			strings.Contains(err.Error(), "dcctl broker reconcile") && !present()
	}

	require.True(t, present(), "precondition: the stream exists before the loss")
	require.False(t, stayedLost(), "negative control: the check passed with the stream present")

	require.NoError(t, adminJS.DeleteStream(stream), "the administrator may delete the capture stream")
	require.False(t, present())
	require.True(t, stayedLost(), "the reader did not report the loss by name, or something recreated the stream")
	b.RequireNoViolations(t) // the reader looked and did not try to create

	b.Reconcile(t, "event-sources")
	require.True(t, present(), "the administrator did not restore the stream")
	require.False(t, stayedLost(), "negative control: the check still passes once the stream is back")
}

// 🔴 THE REBALANCE RUNS AS THE ADMINISTRATOR, AND READS EVERY GROUP BEFORE IT MOVES IT. `dcctl
// broker rebalance` (and the step at the end of `dcctl upgrade`) reads a consumer's leader with a
// CONSUMER.INFO request before it asks that consumer to step down. The administrator held the
// step-down verb for a stream's consumers but not the read, so on a real cluster (GKE round r26)
// every consumer move failed: the broker silently drops a publish the login may not make, so the
// request surfaced as "reading the group before the move: context deadline exceeded" next to the
// "Permissions Violation for Publish to $JS.API.CONSUMER.INFO" line. This runs the executor's own
// read over the real grants, for a consumer on every stream the rebalance is allowed to move.
func TestTheAdministratorCanReadTheConsumersItMovesInARebalance(t *testing.T) {
	instance := fmt.Sprintf("rebal%d", time.Now().UnixNano())
	b := permtest.Start(t, instance)

	adminConn, err := nats.Connect(fmt.Sprintf("nats://%s:%d", b.Host, b.Port),
		nats.UserInfo(natsauth.AdminUser, permtest.Password(natsauth.AdminUser)),
		nats.CustomInboxPrefix(messaging.InboxPrefix(config.NatsAdminPrincipal)))
	require.NoError(t, err)
	t.Cleanup(adminConn.Close)
	adminJS, err := adminConn.JetStream()
	require.NoError(t, err)

	movable := map[string]bool{}
	for _, n := range messaging.RebalanceableStreams(instance) {
		movable[n] = true
	}
	checked := 0
	for _, st := range streams.All {
		name := messaging.StreamName(instance, st.Suffix)
		if !movable[name] || st.Shape == streams.ShapeAdvisory {
			continue
		}
		for _, r := range streams.ReadersOf(st.Suffix) {
			if r.Mode != streams.ReadDurable {
				continue
			}
			durable := messaging.DurableName(instance, r.Area, st.Suffix)
			// The consumer exists the way a deployed one does: its reader area created it.
			areaJS, err := b.Connect(t, r.Area).JetStream()
			require.NoError(t, err)
			_, err = areaJS.AddConsumer(name, &nats.ConsumerConfig{Durable: durable, AckPolicy: nats.AckExplicitPolicy})
			require.NoError(t, err, "%s creating %s", r.Area, durable)

			// A move the group does not need (the plan's source is not its leader) is skipped, but only
			// after the group has been READ; a read the broker refuses is an error here.
			res := replication.ApplyMoves(context.Background(), adminConn, adminJS,
				[]replication.Move{{Stream: name, Consumer: durable, From: "not-the-leader", To: "nowhere"}},
				replication.ApplyOptions{PerMove: 3 * time.Second})
			require.Len(t, res, 1)
			require.NoError(t, res[0].Err, "reading %s/%s as the administrator", name, durable)
			require.True(t, res[0].Skipped)
			checked++
		}
	}
	require.NotZero(t, checked, "no rebalanceable stream has a durable reader, so nothing was exercised")
	b.RequireNoViolations(t)
}
