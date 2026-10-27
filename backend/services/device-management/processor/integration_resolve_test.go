// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtype"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/transport"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// An inbound integration's event is trusted only after the platform has opened the sealed token
// for THIS tenant and THIS integration, authenticated the integration, found it enabled and found
// the device's type in its scope. Each refusal below is paired with the control that shows the
// same event resolving when the one thing is right.

// rigEUI is the DevEUI the rig's integration resolves to the device "dev".
const rigEUI = "70B3D57ED0000001"

type integrationRig struct {
	*resolveRig
	token string // the integration's issued bearer token
}

// setAliases REPLACES a device's alias set, as an operator's device edit does.
func (r *integrationRig) setAliases(device string, euis ...string) {
	r.t.Helper()
	in := make([]model.DeviceAliasInput, 0, len(euis))
	for _, e := range euis {
		in = append(in, model.DeviceAliasInput{Scheme: "lorawan.deveui", Value: e})
	}
	_, err := r.api.UpdateDevice(r.ctx, device, &model.DeviceUpdateRequest{Aliases: model.OptionalDeviceAliasListOf(in)})
	require.NoError(r.t, err)
}

func newIntegrationRig(t *testing.T) *integrationRig {
	t.Helper()
	tables := append(append([]any{}, resolveRigTables...),
		&model.Integration{}, &model.IntegrationDeviceType{}, &model.DeviceAlias{}, &model.IntegrationUnregisteredDevice{})
	rig := buildResolveRig(t, putest.NewSQLiteDB(t, tables...), false, 0, nil)
	keys := dmtest.EnrollmentKeys()
	rig.api.EnrollmentKeys = keys
	checker, err := NewIntegrationChecker(keys)
	require.NoError(t, err)
	rig.api.IntegrationChecker = checker

	// "dev" is of type "dt" (in scope); "other" is of a second type (out of scope).
	otherType := &model.DeviceType{}
	otherType.Token = "dt-other"
	mustCreate(t, rig.db.WithContext(rig.ctx), otherType, "second device type")
	other := &model.Device{DeviceTypeId: otherType.ID}
	other.Token = "other"
	mustCreate(t, rig.db.WithContext(rig.ctx), other, "out-of-scope device")

	created, err := rig.api.CreateIntegration(rig.ctx, &model.IntegrationCreateRequest{
		Token: "chirp-1", Type: string(model.IntegrationChirpStackV4), Enabled: true, DeviceTypeTokens: []string{"dt"}})
	require.NoError(t, err)
	r := &integrationRig{resolveRig: rig, token: created.IssuedToken}
	// "dev" holds the alias the events below are resolved from; "dev-b" is a second in-scope
	// device that holds none.
	found, err := rig.api.DevicesByToken(rig.ctx, []string{"dev"})
	require.NoError(t, err)
	devB := &model.Device{DeviceTypeId: found[0].DeviceTypeId}
	devB.Token = "dev-b"
	mustCreate(t, rig.db.WithContext(rig.ctx), devB, "second in-scope device")
	r.setAliases("dev", rigEUI)
	return r
}

func (r *integrationRig) resolver(mode string) *EventResolver {
	rez := NewEventResolver(1, r.capi, mode, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.SecretTransit = testSecretTransit(r.t)
	return rez
}

// event is an integration's measurement for device, with the token sealed the way the ingest route
// seals it.
func (r *integrationRig) event(device, integration, token string) *esmodel.UnresolvedEvent {
	e := tempEvent("21")
	e.Device = device
	e.Integration = integration
	e.IntegrationIdentifier = rigEUI
	e.Source = transport.Qualify(transport.Integration, integration)
	sealed, err := testSecretTransit(r.t).SealToken("acme", credential.IntegrationSealKind(integration), token)
	require.NoError(r.t, err)
	e.SealedIntegrationToken = sealed
	return e
}

func (r *integrationRig) refused(rez *EventResolver, e *esmodel.UnresolvedEvent, want resolvedevent.FailureReason, why string) {
	r.t.Helper()
	results, reason, err := rez.ResolveEvent(r.ctx, e)
	require.Error(r.t, err, why)
	require.Empty(r.t, results, why)
	require.Equal(r.t, uint(want), reason, why)
}

func TestAnIntegrationEventResolvesOnlyAfterEveryCheck(t *testing.T) {
	rig := newIntegrationRig(t)

	// The control, in every device auth mode: the integration's own authentication decides, so a
	// deployment with device authentication off or optional resolves it the same.
	for _, mode := range []string{config.AuthModeDisabled, config.AuthModeOptional, config.AuthModeRequired} {
		results, reason, err := rig.resolver(mode).ResolveEvent(rig.ctx, rig.event("dev", "chirp-1", rig.token))
		require.NoError(t, err, mode)
		require.Zero(t, reason, mode)
		require.Len(t, results, 1, mode)
	}

	unauthenticated := resolvedevent.FailureReason_Unauthenticated
	for _, mode := range []string{config.AuthModeDisabled, config.AuthModeOptional, config.AuthModeRequired} {
		rez := rig.resolver(mode)

		rig.refused(rez, rig.event("dev", "chirp-1", "not-the-token"), unauthenticated, mode+": wrong token")
		rig.refused(rez, rig.event("dev", "no-such", rig.token), unauthenticated, mode+": unknown integration")

		// Sealed for another integration, another tenant, or as a device's ACCESS_TOKEN: none opens.
		e := rig.event("dev", "chirp-1", rig.token)
		e.SealedIntegrationToken, _ = testSecretTransit(t).SealToken("acme", credential.IntegrationSealKind("chirp-2"), rig.token)
		rig.refused(rez, e, unauthenticated, mode+": sealed for another integration")
		e = rig.event("dev", "chirp-1", rig.token)
		e.SealedIntegrationToken, _ = testSecretTransit(t).SealToken("other-tenant", credential.IntegrationSealKind("chirp-1"), rig.token)
		rig.refused(rez, e, unauthenticated, mode+": sealed for another tenant")
		e = rig.event("dev", "chirp-1", rig.token)
		e.SealedIntegrationToken, _ = testSecretTransit(t).SealToken("acme", "ACCESS_TOKEN", rig.token)
		rig.refused(rez, e, unauthenticated, mode+": sealed as a device access token")
		e = rig.event("dev", "chirp-1", rig.token)
		e.SealedIntegrationToken = []byte(rig.token)
		rig.refused(rez, e, unauthenticated, mode+": the token itself in the seal")

		// The marker without its token, and the token without its marker.
		e = rig.event("dev", "chirp-1", rig.token)
		e.SealedIntegrationToken = nil
		rig.refused(rez, e, unauthenticated, mode+": no sealed token")
		e = rig.event("dev", "chirp-1", rig.token)
		e.Integration = ""
		rig.refused(rez, e, unauthenticated, mode+": a sealed token with no integration is not a plain event")

		// A device outside the integration's scope.
		rig.refused(rez, rig.event("other", "chirp-1", rig.token), unauthenticated, mode+": device out of scope")
		// A device that does not exist.
		rig.refused(rez, rig.event("ghost", "chirp-1", rig.token), resolvedevent.FailureReason_DeviceNotFound, mode+": unknown device")
	}
}

func TestAnIntegrationEventCarriesNoOtherAuthority(t *testing.T) {
	rig := newIntegrationRig(t)
	rez := rig.resolver(config.AuthModeDisabled)
	access := "ACCESS_TOKEN"
	for name, mutate := range map[string]func(*esmodel.UnresolvedEvent){
		"a broker assertion":     func(e *esmodel.UnresolvedEvent) { e.BrokerAsserted = true },
		"a transport mark":       func(e *esmodel.UnresolvedEvent) { e.AuthenticatedTransport = true },
		"a gateway":              func(e *esmodel.UnresolvedEvent) { e.Gateway = "gw" },
		"a device credential":    func(e *esmodel.UnresolvedEvent) { e.CredentialType = &access },
		"a sealed device secret": func(e *esmodel.UnresolvedEvent) { e.SealedCredentialSecret = []byte{1} },
		"a state change":         func(e *esmodel.UnresolvedEvent) { e.EventType = eventtype.StateChange },
		"a location":             func(e *esmodel.UnresolvedEvent) { e.EventType = eventtype.Location },
	} {
		e := rig.event("dev", "chirp-1", rig.token)
		mutate(e)
		rig.refused(rez, e, resolvedevent.FailureReason_Unauthenticated, name)
	}
}

// The source stamp is what command delivery classifies a device's transport by, so an integration
// event that is stamped as anything else is refused: a mis-stamping producer cannot make an
// integration device commandable.
func TestAnIntegrationEventMustBeStampedAsTheIntegration(t *testing.T) {
	rig := newIntegrationRig(t)
	rez := rig.resolver(config.AuthModeRequired)
	for name, source := range map[string]string{
		"empty":                 "",
		"a plain broker source": "mqtt1",
		"another transport":     "sparkplug:plant-a",
		"another integration":   transport.Qualify(transport.Integration, "chirp-2"),
		"bare transport name":   string(transport.Integration),
	} {
		e := rig.event("dev", "chirp-1", rig.token)
		e.Source = source
		rig.refused(rez, e, resolvedevent.FailureReason_Unauthenticated, name)
	}
}

// A revocation takes effect at the very next event, whatever any cache in front of the route
// still holds: the resolver is the authority and reads the integration as it is now.
func TestARevokedIntegrationIsRefusedAtTheNextEvent(t *testing.T) {
	rig := newIntegrationRig(t)
	rez := rig.resolver(config.AuthModeRequired)
	_, _, err := rez.ResolveEvent(rig.ctx, rig.event("dev", "chirp-1", rig.token))
	require.NoError(t, err, "control: enabled and in scope")

	// Disabled.
	_, err = rig.api.UpdateIntegration(rig.ctx, "chirp-1", &model.IntegrationUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	rig.refused(rez, rig.event("dev", "chirp-1", rig.token), resolvedevent.FailureReason_Unauthenticated, "disabled")
	_, err = rig.api.UpdateIntegration(rig.ctx, "chirp-1", &model.IntegrationUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	_, _, err = rez.ResolveEvent(rig.ctx, rig.event("dev", "chirp-1", rig.token))
	require.NoError(t, err, "control: re-enabled")

	// Rotated: the old token stops working at once.
	rotated, err := rig.api.RotateIntegrationToken(rig.ctx, "chirp-1")
	require.NoError(t, err)
	rig.refused(rez, rig.event("dev", "chirp-1", rig.token), resolvedevent.FailureReason_Unauthenticated, "rotated")
	_, _, err = rez.ResolveEvent(rig.ctx, rig.event("dev", "chirp-1", rotated.IssuedToken))
	require.NoError(t, err, "control: the new token")

	// A device moved out of scope.
	devices, err := rig.api.DevicesByToken(rig.ctx, []string{"dev"})
	require.NoError(t, err)
	_, err = rig.api.UpdateDevice(rig.ctx, "dev", &model.DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("dt-other")})
	require.NoError(t, err)
	_ = devices
	rig.refused(rez, rig.event("dev", "chirp-1", rotated.IssuedToken), resolvedevent.FailureReason_Unauthenticated, "moved out of scope")
}

// THE ROUTE'S CACHE CAN OUTLIVE AN ALIAS. event-sources remembers a resolved device for minutes,
// so an uplink (or an event already on the stream) can name a device that no longer holds the
// identifier it was resolved from. The resolver reads the alias row as it is now: such an event
// is refused with its own reason, counted, and never stored on the old device.
func TestAnIntegrationEventIsRefusedWhenItsDeviceNoLongerHoldsTheAlias(t *testing.T) {
	rig := newIntegrationRig(t)
	rez := rig.resolver(config.AuthModeRequired)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "alias_mismatch_test"})
	rez.integrationAliasMismatch = counter
	mismatch := resolvedevent.FailureReason_IntegrationAliasMismatch

	// Control: the alias is on "dev", and an event resolved from it lands there.
	results, reason, err := rez.ResolveEvent(rig.ctx, rig.event("dev", "chirp-1", rig.token))
	require.NoError(t, err)
	require.Zero(t, reason)
	require.Len(t, results, 1)
	require.Equal(t, "dev", results[0].Device.Token)
	require.Zero(t, testutil.ToFloat64(counter))

	// The operator MOVES the alias from "dev" to "dev-b". An event still naming "dev" (the route's
	// cached answer) is refused; the same identifier, once the route re-resolves, lands on "dev-b".
	rig.setAliases("dev")
	rig.setAliases("dev-b", rigEUI)
	stale := rig.event("dev", "chirp-1", rig.token)
	rig.refused(rez, stale, mismatch, "moved to another device")
	_, _, err = rez.ResolveEvent(rig.ctx, stale)
	require.ErrorIs(t, err, ErrIntegrationAliasMismatch, "the loop dead-letters on this error on the first delivery")
	require.EqualValues(t, 2, testutil.ToFloat64(counter))
	results, reason, err = rez.ResolveEvent(rig.ctx, rig.event("dev-b", "chirp-1", rig.token))
	require.NoError(t, err)
	require.Zero(t, reason)
	require.Equal(t, "dev-b", results[0].Device.Token)

	// The operator REMOVES the alias: nothing holds it, so both are refused.
	rig.setAliases("dev-b")
	rig.refused(rez, rig.event("dev-b", "chirp-1", rig.token), mismatch, "removed")
	rig.refused(rez, rig.event("dev", "chirp-1", rig.token), mismatch, "removed, old device")

	// An alias the device holds under another value is not the one the event came from; a
	// different spelling of the same value is.
	rig.setAliases("dev", "70B3D57ED0000002")
	rig.refused(rez, rig.event("dev", "chirp-1", rig.token), mismatch, "holds a different DevEUI")
	rig.setAliases("dev", rigEUI)
	lower := rig.event("dev", "chirp-1", rig.token)
	lower.IntegrationIdentifier = "70b3d57ed0000001"
	_, _, err = rez.ResolveEvent(rig.ctx, lower)
	require.NoError(t, err, "the comparison is on the canonical value")

	// An event that does not say which identifier it came from cannot be checked, so it is refused.
	none := rig.event("dev", "chirp-1", rig.token)
	none.IntegrationIdentifier = ""
	rig.refused(rez, none, mismatch, "no identifier")
}

// THROUGH THE FRONT DOOR: the wire (the identifier survives the stream's protobuf hop), the
// Process loop (a mismatch is dead-lettered on its FIRST delivery, with its own reason and
// payload, and counted), and the control that the same event lands while the alias is held.
func TestAnAliasMismatchIsDeadLetteredOnTheFirstDeliveryAndCounted(t *testing.T) {
	rig := newIntegrationRig(t)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_integration_alias_mismatch_total"})
	rig.rez = rig.resolver(config.AuthModeRequired)
	rig.rez.integrationAliasMismatch = counter

	resolved, failed, _ := processOne(rig.resolveRig, rig.event("dev", "chirp-1", rig.token), 1)
	require.Equal(t, 1, resolved, "control: the alias is held, the event is stored")
	require.Empty(t, failed)
	require.Zero(t, testutil.ToFloat64(counter))

	rig.setAliases("dev")
	rig.setAliases("dev-b", rigEUI)
	resolved, failed, acked := processOne(rig.resolveRig, rig.event("dev", "chirp-1", rig.token), 1)
	require.Zero(t, resolved, "an event for an alias the device no longer holds was stored")
	require.Len(t, failed, 1, "dead-lettered on the first delivery, not retried")
	require.Equal(t, 1, acked)
	require.Equal(t, uint(resolvedevent.FailureReason_IntegrationAliasMismatch), failed[0].reason)
	require.ErrorIs(t, failed[0].err, ErrIntegrationAliasMismatch)
	require.Equal(t, "dev", failed[0].event.Device)
	require.NotNil(t, failed[0].event.Payload, "the refused event keeps its payload so it can be replayed")
	archived := failed[0].event.WithoutPresentedCredential()
	require.Nil(t, archived.SealedIntegrationToken, "the archived dead letter carries no bearer token")
	require.Equal(t, rigEUI, archived.IntegrationIdentifier, "and keeps the identifier it was resolved from")
	require.EqualValues(t, 1, testutil.ToFloat64(counter))

	// After the route re-resolves, the same identifier lands on the new holder.
	resolved, failed, _ = processOne(rig.resolveRig, rig.event("dev-b", "chirp-1", rig.token), 1)
	require.Equal(t, 1, resolved)
	require.Empty(t, failed)
}
