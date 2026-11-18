// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/replayinput"
	"github.com/devicechain-io/dc-event-processing/internal/review"
	cfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type previewShardRouter struct{}

// Ready reports a placement is always adoptable: this router places every tenant statically.
func (previewShardRouter) Ready() error { return nil }

func (previewShardRouter) ShardGroup(tenant string) (int, error) { return 1, nil }

// legacyBrokerSource hides the concrete manager, preserving the parent's exact
// unqualified opener for the equality control.
type legacyBrokerSource struct{ *messaging.NatsManager }

func TestFreshShardedPreviewAndCompare(t *testing.T) {
	for _, sharded := range []bool{true, false} {
		name := "unsharded"
		if sharded {
			name = "g1"
		}
		t.Run(name, func(t *testing.T) {
			const instance = "tenantreplay"
			b := permtest.Start(t, instance, permtest.WithOpenUser())
			js, err := b.ConnectOpen(t).JetStream()
			require.NoError(t, err)
			physical := streams.ResolvedEvents
			ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			ms.InstanceConfiguration.Infrastructure.Nats = b.NatsConfiguration("event-processing")
			ms.InstanceConfiguration.Infrastructure.Nats.Auth = cfg.NatsAuthConfiguration{User: permtest.OpenUser, Password: permtest.Password(permtest.OpenUser)}
			if sharded {
				physical = messaging.GroupSuffix(streams.ResolvedEvents, 1)
				_, err = js.AddStream(&nats.StreamConfig{Name: messaging.StreamName(instance, physical), Subjects: []string{instance + ".*." + physical, instance + ".*." + physical + ".>"}, Storage: nats.FileStorage, AllowDirect: true})
				require.NoError(t, err)
				ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &cfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}}
			}
			manager := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
			ctx := core.WithTenant(context.Background(), "acme")
			require.NoError(t, manager.Initialize(ctx))
			t.Cleanup(func() { require.NoError(t, manager.Stop(context.Background())) })
			if sharded {
				require.NoError(t, manager.EnableStreamSharding(previewShardRouter{}, ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups))
			}
			require.NoError(t, manager.Start(ctx))
			publish := func(tenant, suffix, dev, version, value string, at time.Time) {
				m := msg(t, 0, tenant, dev, version, "temperature", value, at)
				_, err := js.Publish(messaging.ScopedSubject(instance, tenant, suffix), m.Value)
				require.NoError(t, err)
			}
			// The same group holds foreign tenants and versions; routing is not admission.
			publish("other", streams.ResolvedEvents, "intruder", "p@1", "99", base)
			if sharded {
				publish("other", physical, "intruder", "p@1", "99", base)
			}
			publish("acme", physical, "d1", "p@1", "90", base)
			publish("acme", physical, "d1", "p@1", "70", base.Add(time.Minute))
			publish("acme", physical, "wrong-version", "q@1", "99", base)
			before, err := js.StreamInfo(messaging.StreamName(instance, physical))
			require.NoError(t, err)
			previewRun := func(source ReplayOpener) Result {
				out, err := Run(ctx, source, streams.ResolvedEvents, thresholdReg(t), "acme", "p@1", window(), 0, 0, 0, nil, nil)
				require.NoError(t, err)
				return out
			}
			out := previewRun(manager)
			require.Equal(t, 2, out.Stats.EventsScanned)
			require.Len(t, out.Firings, 2)
			require.Equal(t, "d1", out.Firings[0].Series)
			p := review.Params{Tenant: "acme", ProfileToken: "p", Base: &review.RuleEnvelope{Token: "hot", Enabled: true, Definition: json.RawMessage(`{"name":"hot","type":"threshold","when":{"metric":"temperature","op":"gt","threshold":80}}`)}, Devices: []string{"d1", "intruder"}, Windows: []review.Window{{Role: review.Incident, Start: base.Add(-time.Minute), End: base.Add(2 * time.Minute)}, {Role: review.Normal, Start: base.Add(5 * time.Minute), End: base.Add(8 * time.Minute)}}, Now: base.Add(2 * time.Hour), Source: manager, Suffix: streams.ResolvedEvents}
			compared, err := review.Run(ctx, p)
			require.NoError(t, err)
			require.Len(t, compared.Windows, 2)
			require.Equal(t, physical, compared.Windows[0].Bounds.Stream)
			require.Len(t, compared.Windows[0].BaseEdges, 2)
			for _, dev := range compared.Windows[0].Devices {
				if dev.Device == "intruder" {
					require.Empty(t, dev.Readings)
				}
			}
			for _, w := range compared.Windows {
				p.StreamBounds = append(p.StreamBounds, w.Bounds)
			}
			again, err := review.Run(ctx, p)
			require.NoError(t, err)
			require.Equal(t, compared.InputDigest, again.InputDigest)
			if sharded {
				// Saved pre-enable base bounds MUST refuse before a g1 sequence is used.
				p.StreamBounds[0].Stream = streams.ResolvedEvents
				_, err = review.Run(ctx, p)
				require.ErrorContains(t, err, "recorded stream bounds are invalid")
				source, suffix, err := replayinput.TenantReview(ctx, manager, streams.ResolvedEvents)
				require.NoError(t, err)
				require.Equal(t, physical, suffix)
				reader, head, err := source.NewReplayReader(suffix, 2)
				require.NoError(t, err)
				require.Equal(t, before.State.LastSeq, head)
				m, err := reader.Read(ctx)
				require.NoError(t, err)
				require.EqualValues(t, 2, m.StreamSeq)
				require.Equal(t, messaging.ScopedSubject(instance, "acme", physical), m.Subject)
				require.NoError(t, reader.Close())
			} else {
				require.Equal(t, previewRun(legacyBrokerSource{manager}), out)
				p.Source = legacyBrokerSource{manager}
				legacy, err := review.Run(ctx, p)
				require.NoError(t, err)
				require.Equal(t, legacy, compared)
				require.Equal(t, legacy.InputDigest, compared.InputDigest)
			}
			after, err := js.StreamInfo(messaging.StreamName(instance, physical))
			require.NoError(t, err)
			require.Equal(t, before.State, after.State, "preview/compare must not publish or mutate history")
		})
	}
}
