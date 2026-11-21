// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

func TestStagedRolloutSafetyIsOptInOnTheWire(t *testing.T) {
	p, _ := rolloutPlane(t)
	resp := p.exec(p.ctx("acme", auth.ArtifactWrite), `mutation {
	  createUpdateRollout(request: {token: "staged", artifactToken: "fw-1", groupToken: "fleet", maxInFlight: 1,
	    cohorts: [{name: "canary", deviceTokens: ["dev-1"], manualPromotion: true}, {name: "fleet", percentage: 100}],
	    failureRate: {percentage: 25, windowSeconds: 600, minimumSamples: 2}}) {
	    token currentCohort cohorts { name manualPromotion } failureRate { percentage windowSeconds minimumSamples }
	    haltSequence haltFailures haltSamples
	  }
	}`, nil)
	require.Empty(t, resp.Errors)
	require.Contains(t, string(resp.Data), `"currentCohort":0`)
	require.Contains(t, string(resp.Data), `"minimumSamples":2`)
}

func TestManualPromotionUsesTheFrozenCohortOnTheWire(t *testing.T) {
	p, _ := rolloutPlane(t)
	writer := p.ctx("acme", auth.ArtifactWrite)
	require.Empty(t, p.exec(writer, `mutation { createUpdateRollout(request: {token: "staged", artifactToken: "fw-1", groupToken: "fleet", maxInFlight: 1,
	 cohorts: [{name: "canary", deviceTokens: ["dev-1"], manualPromotion: true}, {name: "all", percentage: 100}]}) { token } }`, nil).Errors)
	require.Empty(t, p.exec(writer, `mutation { startUpdateRollout(token: "staged") { token } }`, nil).Errors)
	q := `mutation { promoteUpdateRollout(token: "staged", expectedCohort: 0) { currentCohort } }`
	require.NotEmpty(t, p.exec(writer, q, nil).Errors)
	require.NoError(t, p.api.RDB.DB(writer).Model(&model.UpdateAttempt{}).Where("device_token = ?", "dev-1").Update("state", string(ota.StateUpdated)).Error)
	for range 2 {
		resp := p.exec(writer, q, nil)
		require.Empty(t, resp.Errors)
		require.JSONEq(t, `{"promoteUpdateRollout":{"currentCohort":1}}`, string(resp.Data))
	}
	require.NotEmpty(t, p.exec(p.ctx("globex", auth.ArtifactWrite), q, nil).Errors)
}
