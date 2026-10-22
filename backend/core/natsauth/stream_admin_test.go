// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
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
