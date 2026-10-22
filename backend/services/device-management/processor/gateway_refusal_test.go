// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// A gateway's publish for a child, at the resolver. The fixture is the shared resolve rig (the
// real cached Api over sqlite, evictions wired as main.go wires them) with a gateway "gw", a
// child "c" assigned to it, and the rig's own "dev": an EXISTING, ENABLED device of the same
// tenant that is NOT assigned to the gateway, so only the assignment check can refuse it.

func gatewayRig(t *testing.T, mode string) *resolveRig {
	t.Helper()
	rig := newCountingResolveRig(t, false, nil)
	rig.rez.AuthMode = mode
	for _, token := range []string{"gw", "c"} {
		_, err := rig.api.CreateDevice(rig.ctx, &model.DeviceCreateRequest{Token: token, DeviceTypeToken: "dt"})
		require.NoError(t, err)
	}
	_, err := rig.api.AssignDeviceGateway(rig.ctx, "c", "gw")
	require.NoError(t, err)
	return rig
}

// viaGateway is a measurement the capture source would have produced from a publish on the
// gateway's namespace: the child as its device, the gateway marked, and NOT BrokerAsserted (the
// broker authenticated the gateway, not the child; see event-sources' brokerAssertion.mark).
func viaGateway(gateway, child string) *esmodel.UnresolvedEvent {
	e := tempEvent("21.5")
	e.Device = child
	e.Gateway = gateway
	altId := "alt-" + child
	e.AltId = &altId
	now := time.Now().UTC()
	e.OccurredTime, e.ProcessedTime = now, now
	return e
}

var allAuthModes = []string{config.AuthModeDisabled, config.AuthModeOptional, config.AuthModeRequired}

// 1. The positive: a gateway publishes for its assigned child, in every auth mode. The event
// resolves ATTRIBUTED TO THE CHILD. This is the control for every refusal below.
func TestAGatewayEventForItsAssignedChildResolvesToTheChild(t *testing.T) {
	for _, mode := range allAuthModes {
		rig := gatewayRig(t, mode)
		resolved := rig.resolve(viaGateway("gw", "c"))
		require.Equal(t, "c", resolved.SourceDeviceToken, "mode %s: attributed to the child, not the gateway", mode)
	}
}

// 2. THE R4 CONTROL. The gateway names an existing, enabled device of the same tenant that is
// not assigned to it: refused as NotGatewayChild in every auth mode, never resolved.
func TestAGatewayEventForAnUnassignedDeviceIsRefusedInEveryAuthMode(t *testing.T) {
	for _, mode := range allAuthModes {
		rig := gatewayRig(t, mode)
		results, reason, err := rig.rez.ResolveEvent(rig.ctx, viaGateway("gw", "dev"))
		require.Error(t, err, "mode %s: a gateway spoke for a device that is not its own", mode)
		require.Empty(t, results)
		require.Equal(t, uint(resolvedevent.FailureReason_NotGatewayChild), reason, mode)
		require.True(t, errors.Is(err, ErrNotGatewayChild), "%s: %v", mode, err)
		require.Contains(t, err.Error(), `"gw"`)
		require.Contains(t, err.Error(), `"dev"`)
	}
}

// ...and the refusal follows the assignment: clearing it refuses the child's next event with no
// reconnect, assigning it admits again, a sibling is unaffected, and a MOVE to another gateway
// refuses the old one. Each step reads through the cached Api, so a missed eviction is a red.
func TestAGatewayRefusalFollowsTheAssignmentThroughTheCaches(t *testing.T) {
	rig := gatewayRig(t, config.AuthModeRequired)
	_, err := rig.api.CreateDevice(rig.ctx, &model.DeviceCreateRequest{Token: "c-sibling", DeviceTypeToken: "dt"})
	require.NoError(t, err)
	_, err = rig.api.AssignDeviceGateway(rig.ctx, "c-sibling", "gw")
	require.NoError(t, err)
	_, err = rig.api.CreateDevice(rig.ctx, &model.DeviceCreateRequest{Token: "gw2", DeviceTypeToken: "dt"})
	require.NoError(t, err)

	rig.resolve(viaGateway("gw", "c")) // warms the child's and the gateway's cache entries
	rig.resolve(viaGateway("gw", "c-sibling"))

	_, err = rig.api.ClearDeviceGateway(rig.ctx, "c")
	require.NoError(t, err)
	_, reason, err := rig.rez.ResolveEvent(rig.ctx, viaGateway("gw", "c"))
	require.ErrorIs(t, err, ErrNotGatewayChild, "the cleared child's next event was not refused: a stale cache entry")
	require.Equal(t, uint(resolvedevent.FailureReason_NotGatewayChild), reason)
	rig.resolve(viaGateway("gw", "c-sibling")) // the gateway's other children keep flowing

	_, err = rig.api.AssignDeviceGateway(rig.ctx, "c", "gw2")
	require.NoError(t, err)
	_, _, err = rig.rez.ResolveEvent(rig.ctx, viaGateway("gw", "c"))
	require.ErrorIs(t, err, ErrNotGatewayChild, "after a move the old gateway may no longer speak for the child")
	rig.resolve(viaGateway("gw2", "c"))
}

// 3. An unknown child token is refused on the first attempt as NotGatewayChild (not retried as
// DeviceNotFound, never auto-registered), and a device that does not exist cannot be named into
// being by a gateway.
func TestAGatewayEventForAnUnknownDeviceIsRefusedNotRetried(t *testing.T) {
	rig := gatewayRig(t, config.AuthModeRequired)
	_, reason, err := rig.rez.ResolveEvent(rig.ctx, viaGateway("gw", "no-such-device"))
	require.ErrorIs(t, err, ErrNotGatewayChild)
	require.Equal(t, uint(resolvedevent.FailureReason_NotGatewayChild), reason)

	// The control: the same unknown token WITHOUT a gateway is the retry-then-dead-letter
	// DeviceNotFound it has always been, so the terminal answer above is the gateway shape's.
	rig.rez.AuthMode = config.AuthModeDisabled
	plain := tempEvent("1")
	plain.Device = "no-such-device"
	_, reason, err = rig.rez.ResolveEvent(rig.ctx, plain)
	require.Error(t, err)
	require.Equal(t, uint(resolvedevent.FailureReason_DeviceNotFound), reason)
	require.False(t, errors.Is(err, ErrNotGatewayChild))
}

// A disabled gateway speaks for no one; a disabled child is refused by the existing path; an
// event naming a gateway that does not exist, or naming no child, is refused.
func TestAGatewayEventNeedsAnEnabledGatewayAndAChild(t *testing.T) {
	rig := gatewayRig(t, config.AuthModeRequired)

	nameless := viaGateway("gw", "c")
	nameless.Device = ""
	_, _, err := rig.rez.ResolveEvent(rig.ctx, nameless)
	require.ErrorIs(t, err, ErrNotGatewayChild, "a gateway event naming no child was resolved")

	_, _, err = rig.rez.ResolveEvent(rig.ctx, viaGateway("ghost-gateway", "c"))
	require.ErrorIs(t, err, ErrNotGatewayChild)

	_, err = rig.api.UpdateDevice(rig.ctx, "c", &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	_, reason, err := rig.rez.ResolveEvent(rig.ctx, viaGateway("gw", "c"))
	require.ErrorIs(t, err, model.ErrDeviceDisabled, "a disabled child is refused as any disabled device is")
	require.Equal(t, uint(resolvedevent.FailureReason_DeviceDisabled), reason)
	_, err = rig.api.UpdateDevice(rig.ctx, "c", &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)

	_, err = rig.api.UpdateDevice(rig.ctx, "gw", &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	_, _, err = rig.rez.ResolveEvent(rig.ctx, viaGateway("gw", "c"))
	require.ErrorIs(t, err, ErrNotGatewayChild, "a disabled gateway still spoke for its child")
	_, err = rig.api.UpdateDevice(rig.ctx, "gw", &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	rig.resolve(viaGateway("gw", "c"))
}

// 9. Upgrade direction. A device-by-token cache entry written by a replica that predates the
// gateway column has no GatewayId, which reads as "no gateway": the gateway's publish is
// REFUSED, never accepted. (Negative control: the same event resolves once the entry is the
// current format.)
func TestAnOldFormatCacheEntryReadsAsNoGatewayAndIsRefused(t *testing.T) {
	rig := gatewayRig(t, config.AuthModeRequired)
	children, err := rig.api.DevicesByToken(rig.ctx, []string{"c"})
	require.NoError(t, err)
	require.Len(t, children, 1)
	raw, err := json.Marshal(children[0])
	require.NoError(t, err)
	var asMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &asMap))
	require.Contains(t, asMap, "GatewayId", "the cached shape has no gateway field; the test would prove nothing")
	delete(asMap, "GatewayId")
	old, err := json.Marshal(asMap)
	require.NoError(t, err)

	require.NoError(t, rig.caches.DeviceByToken.Set(rig.ctx, "acme|c", json.RawMessage(old)))
	_, _, err = rig.rez.ResolveEvent(rig.ctx, viaGateway("gw", "c"))
	require.ErrorIs(t, err, ErrNotGatewayChild, "an entry with no gateway was read as a child")

	require.NoError(t, rig.caches.DeviceByToken.Set(rig.ctx, "acme|c", json.RawMessage(raw)))
	rig.resolve(viaGateway("gw", "c"))
}

// recordedNotices is the gateway's side of the best-effort notice.
type recordedNotices struct {
	mu   sync.Mutex
	sent []struct {
		tenant, gateway string
		notice          messaging.GatewayNotice
	}
}

func (r *recordedNotices) Notice(tenant, gateway string, n messaging.GatewayNotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, struct {
		tenant, gateway string
		notice          messaging.GatewayNotice
	}{tenant, gateway, n})
	return nil
}

// failedRecord is what the dead-letter path was handed.
type failedRecord struct {
	tenant string
	reason uint
	event  esmodel.UnresolvedEvent
	err    error
}

// processOne drives ONE delivery through the real Process loop, the path that settles, counts,
// logs and notifies, and returns what reached the resolved and failed callbacks.
func processOne(rig *resolveRig, event *esmodel.UnresolvedEvent, delivery int) (resolved int, failed []failedRecord, acked int) {
	body, err := esproto.MarshalUnresolvedEvent(event)
	if err != nil {
		panic(err)
	}
	ack := &atomicAck{}
	in := make(chan messaging.Message, 1)
	in <- messaging.NewConsumedMessage("inst.acme.inbound-events", body, delivery, nil, ack)
	close(in)
	rig.rez.Unresolved = in
	var mu sync.Mutex
	rig.rez.Invalid = func(error, messaging.Message) {}
	rig.rez.Resolved = func(m messaging.Message, _ string, _ []EventResolutionResults) {
		mu.Lock()
		resolved++
		mu.Unlock()
		_ = m.Ack()
	}
	rig.rez.Failed = func(m messaging.Message, tenant string, reason uint, ev esmodel.UnresolvedEvent, e error) {
		mu.Lock()
		failed = append(failed, failedRecord{tenant, reason, ev, e})
		mu.Unlock()
		_ = m.Ack()
	}
	rig.rez.Process(context.Background())
	return resolved, failed, int(ack.n.Load())
}

// 2 (continued). THE LOUD REFUSAL. A non-child publish is not acknowledged-and-forgotten: on its
// FIRST delivery it is recorded on failed-events with its payload and the reason, counted,
// and announced to the gateway — and the credential a (misbehaving) body carried is redacted from
// the record.
func TestARefusedGatewayEventIsRecordedCountedAndAnnounced(t *testing.T) {
	rig := gatewayRig(t, config.AuthModeRequired)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_not_gateway_child_total"})
	notices := &recordedNotices{}
	rig.rez.gateway = newGatewayRefusals()
	rig.rez.gateway.counter = counter
	rig.rez.gateway.noticer = notices

	event := viaGateway("gw", "dev")
	username := "must-not-be-archived"
	credType := "MQTT_BASIC"
	event.CredentialType, event.CredentialId = &credType, &username

	resolved, failed, acked := processOne(rig, event, 1)

	require.Zero(t, resolved, "a refused event reached the resolved stream")
	require.Len(t, failed, 1, "a refused gateway publish must be recorded on the first delivery, not retried or dropped")
	require.Equal(t, 1, acked, "the failed-events path owns the ack and settled it")
	rec := failed[0]
	require.Equal(t, "acme", rec.tenant)
	require.Equal(t, uint(resolvedevent.FailureReason_NotGatewayChild), rec.reason)
	require.ErrorIs(t, rec.err, ErrNotGatewayChild)
	require.Equal(t, "dev", rec.event.Device, "the record names the device the gateway claimed")
	require.Equal(t, "gw", rec.event.Gateway, "and the gateway that claimed it")
	require.NotNil(t, rec.event.Payload, "the refused event keeps its payload so it can be recovered")

	require.Equal(t, 1.0, testutil.ToFloat64(counter), "the refusal is counted once")

	require.Len(t, notices.sent, 1, "the gateway is told")
	sent := notices.sent[0]
	require.Equal(t, "acme", sent.tenant)
	require.Equal(t, "gw", sent.gateway)
	require.Equal(t, messaging.GatewayNotice{AltId: "alt-dev", Device: "dev", Reason: "not_gateway_child"}, sent.notice)

	// The record the failed-events path ARCHIVES is credential-free: OnUnresolvedEvent strips
	// the presented credential before it marshals the event.
	archived := rec.event.WithoutPresentedCredential()
	body, err := esproto.MarshalUnresolvedEvent(&archived)
	require.NoError(t, err)
	require.NotContains(t, string(body), username)
	// ...and the control for that assertion: the unredacted event does carry it.
	unredacted, err := esproto.MarshalUnresolvedEvent(&rec.event)
	require.NoError(t, err)
	require.Contains(t, string(unredacted), username)

	// The control: an ACCEPTED gateway event counts nothing, notifies no one and is not recorded.
	resolved, failed, _ = processOne(rig, viaGateway("gw", "c"), 1)
	require.Equal(t, 1, resolved)
	require.Empty(t, failed)
	require.Equal(t, 1.0, testutil.ToFloat64(counter))
	require.Len(t, notices.sent, 1)
}

// The WARN names tenant, gateway and child, once per pair per window: the counter and the
// notice are per event, the log is not (a mis-assigned site would otherwise log every event).
func TestTheGatewayRefusalWarnIsRateLimitedPerPair(t *testing.T) {
	g := newGatewayRefusals()
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return clock }
	ev := &esmodel.UnresolvedEvent{Gateway: "gw", Device: "dev"}

	require.True(t, g.shouldWarn("acme|gw|dev"))
	require.False(t, g.shouldWarn("acme|gw|dev"), "a second refusal of the same pair inside the window must not log")
	require.True(t, g.shouldWarn("acme|gw|other"), "another child is its own line")
	require.True(t, g.shouldWarn("other|gw|dev"), "another tenant is its own line")
	clock = clock.Add(2 * gatewayWarnEvery)
	require.True(t, g.shouldWarn("acme|gw|dev"), "the window closes")

	// refused is nil-safe and counts without a noticer.
	var nilRefusals *gatewayRefusals
	nilRefusals.refused("acme", ev, errors.New("x"))
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_rl_total"})
	g.counter = counter
	g.refused("acme", ev, errors.New("x"))
	g.refused("acme", ev, errors.New("x"))
	require.Equal(t, 2.0, testutil.ToFloat64(counter), "every refusal is counted, logged or not")

	// The bound: a flood of distinct offenders cannot grow the limiter without limit.
	for i := 0; i < 3*gatewayWarnKeys; i++ {
		g.shouldWarn("acme|gw|c-" + time.Duration(i).String())
	}
	require.LessOrEqual(t, len(g.last), gatewayWarnKeys)
}

// THE ROLLING-UPGRADE DIRECTION ON THE WIRE. A device-management replica that predates the
// gateway field reads a gateway event with field 19 unknown, so it sees a plain event for the
// child: what it does with that is decided by BrokerAsserted alone. The capture source leaves a
// gateway event UNMARKED (event-sources' brokerAssertion.mark), so such a replica asks for a
// credential under required and refuses; marked, it would resolve the child's event with no
// assignment check, and any device could speak for any device of its tenant during a rollout.
//
// Negative control: mark the fixture BrokerAsserted (what the source sent before it was unmarked)
// and the old replica's resolve below succeeds (red). The source's half, that mark leaves a gateway
// event unmarked, is pinned in event-sources by TestAGatewaySubjectAssertsTheGatewayAndNamesTheChild.
func TestAGatewayEventIsRefusedByAResolverThatPredatesTheGatewayField(t *testing.T) {
	rig := gatewayRig(t, config.AuthModeRequired)

	// What the current resolver accepts...
	event := viaGateway("gw", "c")
	rig.resolve(event)

	// ...and the same event as the older replica decodes it: the field it does not know is gone.
	encoded, err := esproto.MarshalUnresolvedEvent(event)
	require.NoError(t, err)
	old, err := esproto.UnmarshalUnresolvedEvent(encoded)
	require.NoError(t, err)
	require.Equal(t, "gw", old.Gateway, "the fixture did not carry the gateway over the wire")
	old.Gateway = ""
	_, reason, err := rig.rez.ResolveEvent(rig.ctx, old)
	require.Error(t, err, "a resolver that ignores the gateway field accepted a gateway's event as the child's own")
	require.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason)
}
