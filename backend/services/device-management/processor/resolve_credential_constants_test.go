// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// ceilingOf answers a fixed calculated-metrics ceiling.
type ceilingOf int

func (c ceilingOf) Resolve(context.Context, string) (int, error) { return int(c), nil }

// byTokenSpy is the rig's cached Api with its by-token reads counted, and, when swap is
// set, answered with that device instead.
type byTokenSpy struct {
	*model.CachedApi
	reads int
	swap  *model.Device
}

func (s *byTokenSpy) DevicesByToken(ctx context.Context, tokens []string) ([]*model.Device, error) {
	s.reads++
	if s.swap != nil {
		return []*model.Device{s.swap}, nil
	}
	return s.CachedApi.DevicesByToken(ctx, tokens)
}

// constantRig is the counting rig with a published `scaled = temp * device.k` formula,
// the device's metadata {"k": 3}, and one credential of ctype for it. present seals that
// credential onto an event.
func constantRig(t *testing.T, ctype model.CredentialType) (*resolveRig, func(*esmodel.UnresolvedEvent)) {
	t.Helper()
	rig := newCountingResolveRig(t, false, nil)
	rig.api.CalculatedMetricsCeiling = ceilingOf(5)
	expr := "temp * device.k"
	unit := "Cel"
	_, err := rig.api.CreateMetricDefinition(rig.ctx, &model.MetricDefinitionCreateRequest{
		Token: "scaled-def", DeviceProfileToken: "p", MetricKey: "scaled", DataType: "DOUBLE",
		Unit: &unit, Expression: &expr})
	require.NoError(t, err)
	_, err = rig.api.PublishDeviceProfile(rig.ctx, "p", nil, nil, "t2")
	require.NoError(t, err)
	_, err = rig.api.UpdateDevice(rig.ctx, "dev", &model.DeviceUpdateRequest{
		Metadata: dcgraphql.OptionalStringOf(`{"k": 3}`)})
	require.NoError(t, err)

	secret := "s3cret-0123456789"
	req := &model.DeviceCredentialCreateRequest{Token: "c-1", DeviceToken: "dev",
		CredentialType: string(ctype), CredentialId: "cred-1", Enabled: true}
	if ctype == model.CredentialMqttBasic {
		req.CredentialValue = &secret
	} else {
		req.CredentialId = "tok-1"
	}
	_, err = rig.api.CreateDeviceCredential(rig.ctx, req)
	require.NoError(t, err)
	return rig, func(ev *esmodel.UnresolvedEvent) {
		if ctype == model.CredentialMqttBasic {
			presentSealed(t, rig.ctx, ev, string(ctype), "cred-1", secret)
		} else {
			presentSealedToken(t, rig.ctx, ev, string(ctype), "tok-1")
		}
	}
}

// scaledOf resolves one event and returns its computed `scaled` value, if any.
func scaledOf(t *testing.T, rez *EventResolver, ctx context.Context, ev *esmodel.UnresolvedEvent) (string, bool) {
	t.Helper()
	results, reason, err := rez.ResolveEvent(ctx, ev)
	require.NoError(t, err)
	require.Zero(t, reason)
	require.Len(t, results, 1)
	payload, ok := results[0].Resolved.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	require.True(t, ok, "payload %T", results[0].Resolved.Payload)
	return value(t, payload, 0, "scaled")
}

// A calculated metric that reads a device constant (`device.k`) computes on an event that
// authenticated by credential, the way every MQTT reading does. The credential lookup
// deliberately reads a narrow set of device columns (metadata excluded, so a miss costs the
// same however much a device stores); the resolver must therefore fetch the metadata it
// needs, not compute from a device record that has none.
func TestCalculatedMetricReadsDeviceConstantOnCredentialResolvedEvent(t *testing.T) {
	for _, ctype := range []model.CredentialType{model.CredentialMqttBasic, model.CredentialAccessToken} {
		t.Run(string(ctype), func(t *testing.T) {
			rig, present := constantRig(t, ctype)
			rez := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
			rez.SecretTransit = testSecretTransit(t)
			// Three: a credential-cache miss, a hit, and a hit after a metadata edit.
			for i := 0; i < 3; i++ {
				want := "30"
				if i == 2 {
					// A metadata edit reaches the next event: the by-token entry is evicted.
					_, err := rig.api.UpdateDevice(rig.ctx, "dev", &model.DeviceUpdateRequest{
						Metadata: dcgraphql.OptionalStringOf(`{"k": 4}`)})
					require.NoError(t, err)
					want = "40"
				}
				ev := tempEvent("10")
				present(ev)
				v, found := scaledOf(t, rez, rig.ctx, ev)
				require.True(t, found, "attempt %d: the calculated metric was not computed from the device constant", i)
				assert.Equal(t, want, v, "attempt %d", i)
			}
		})
	}
}

// The by-token re-read is made only for a row that did not read its metadata: an event
// resolved on its device token already holds the full row and reads it once, and a
// credentialed one reads it once more. The re-read must also be the device the credential
// authenticated: a token that names another row now is refused, never computed from that
// row's constants.
func TestDeviceConstantsReReadOnlyANarrowRowAndOnlyItsOwnDevice(t *testing.T) {
	rig, present := constantRig(t, model.CredentialMqttBasic)

	spy := &byTokenSpy{CachedApi: rig.capi}
	rez := NewEventResolver(1, spy, config.AuthModeOptional, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.SecretTransit = testSecretTransit(t)

	// On its token: the resolver's own device read, and no re-read.
	v, found := scaledOf(t, rez, rig.ctx, tempEvent("10"))
	require.True(t, found)
	assert.Equal(t, "30", v)
	assert.Equal(t, 1, spy.reads, "an event resolved on its token re-read a device whose metadata it held")

	// On a credential: the one re-read.
	spy.reads = 0
	ev := tempEvent("10")
	present(ev)
	v, found = scaledOf(t, rez, rig.ctx, ev)
	require.True(t, found)
	assert.Equal(t, "30", v)
	assert.Equal(t, 1, spy.reads, "a credentialed event's narrow row must be re-read once for its constants")

	// The token answers another device: refused.
	full, err := rig.api.DevicesByToken(rig.ctx, []string{"dev"})
	require.NoError(t, err)
	require.Len(t, full, 1)
	stranger := *full[0]
	stranger.ID += 1000
	meta := datatypes.JSON(`{"k": 9}`)
	stranger.Metadata = &meta
	spy.swap = &stranger
	ev = tempEvent("10")
	present(ev)
	results, _, err := rez.ResolveEvent(rig.ctx, ev)
	require.Error(t, err, "a constant read from another device's row was not refused")
	assert.Empty(t, results)
}
