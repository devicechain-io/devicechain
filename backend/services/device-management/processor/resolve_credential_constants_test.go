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
)

// ceilingOf answers a fixed calculated-metrics ceiling.
type ceilingOf int

func (c ceilingOf) Resolve(context.Context, string) (int, error) { return int(c), nil }

// A calculated metric that reads a device constant (`device.k`) computes on an event that
// authenticated by credential, the way every MQTT reading does. The credential lookup
// deliberately reads a narrow set of device columns (metadata excluded, so a miss costs the
// same however much a device stores); the resolver must therefore fetch the metadata it
// needs, not compute from a device record that has none.
func TestCalculatedMetricReadsDeviceConstantOnCredentialResolvedEvent(t *testing.T) {
	for _, ctype := range []model.CredentialType{model.CredentialMqttBasic, model.CredentialAccessToken} {
		t.Run(string(ctype), func(t *testing.T) {
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

			rez := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
			rez.SecretTransit = testSecretTransit(t)
			// Twice: the second event is a credential-cache hit.
			for i := 0; i < 3; i++ {
				want := "30"
				if i == 2 {
					// A metadata edit reaches the next event: the by-token entry is evicted.
					_, err = rig.api.UpdateDevice(rig.ctx, "dev", &model.DeviceUpdateRequest{
						Metadata: dcgraphql.OptionalStringOf(`{"k": 4}`)})
					require.NoError(t, err)
					want = "40"
				}
				ev := tempEvent("10")
				if ctype == model.CredentialMqttBasic {
					presentSealed(t, rig.ctx, ev, string(ctype), "cred-1", secret)
				} else {
					presentSealedToken(t, rig.ctx, ev, string(ctype), "tok-1")
				}
				results, reason, err := rez.ResolveEvent(rig.ctx, ev)
				require.NoError(t, err)
				require.Zero(t, reason)
				require.Len(t, results, 1)
				payload, ok := results[0].Resolved.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
				require.True(t, ok, "attempt %d: payload %T", i, results[0].Resolved.Payload)
				v, found := value(t, payload, 0, "scaled")
				require.True(t, found, "attempt %d: the calculated metric was not computed from the device constant", i)
				assert.Equal(t, want, v, "attempt %d", i)
			}
		})
	}
}
