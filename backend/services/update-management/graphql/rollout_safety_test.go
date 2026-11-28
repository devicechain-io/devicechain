// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

func TestStagedRolloutSafetyIsOptInOnTheWire(t *testing.T) {
	p, _ := rolloutPlane(t)
	p.api.RolloutSafety = true
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
	p.api.RolloutSafety = true
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

func TestFailedCohortNeedsExplicitPromotionOverrideOnTheWire(t *testing.T) {
	p, _ := rolloutPlane(t)
	p.api.RolloutSafety = true
	writer := p.ctx("acme", auth.ArtifactWrite)
	require.Empty(t, p.exec(writer, `mutation { createUpdateRollout(request: {token: "failed", artifactToken: "fw-1", groupToken: "fleet", maxInFlight: 1,
 cohorts: [{name: "canary", deviceTokens: ["dev-1"]}, {name: "all", percentage: 100}]}) { token } }`, nil).Errors)
	require.Empty(t, p.exec(writer, `mutation { startUpdateRollout(token: "failed") { token } }`, nil).Errors)
	require.NoError(t, p.api.RDB.DB(writer).Model(&model.UpdateAttempt{}).Where("device_token = ?", "dev-1").Update("state", string(ota.StateFailed)).Error)
	v, err := p.api.RolloutByToken(writer, "failed")
	require.NoError(t, err)
	_, err = p.api.StepRollout(writer, v.Rollout.ID)
	require.NoError(t, err)
	resp := p.exec(p.ctx("acme", auth.ArtifactRead), `{ updateRollout(token: "failed") { state pauseReason } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateRollout":{"state":"PAUSED","pauseReason":"COHORT_FAILED"}}`, string(resp.Data))
	require.NotEmpty(t, p.exec(writer, `mutation { resumeUpdateRollout(token: "failed") { token } }`, nil).Errors)
	require.NotEmpty(t, p.exec(writer, `mutation { promoteUpdateRollout(token: "failed", expectedCohort: 0) { token } }`, nil).Errors)
	q := `mutation { promoteUpdateRollout(token: "failed", expectedCohort: 0, overrideFailure: true) { currentCohort } }`
	for range 2 {
		resp = p.exec(writer, q, nil)
		require.Empty(t, resp.Errors)
		require.JSONEq(t, `{"promoteUpdateRollout":{"currentCohort":1}}`, string(resp.Data))
	}
	require.NotEmpty(t, p.exec(p.ctx("globex", auth.ArtifactWrite), q, nil).Errors)
}

func TestRolloutSafetySchemaEntriesStayWithRolloutControls(t *testing.T) {
	mutations := SchemaContent[strings.Index(SchemaContent, "type Mutation {"):]
	require.Greater(t, strings.Index(mutations, "    promoteUpdateRollout("), strings.Index(mutations, "    resumeUpdateRollout("))
	require.Less(t, strings.Index(mutations, "    resumeHaltedUpdateRollout("), strings.Index(mutations, "    abortUpdateRollout("))
	rollout := SchemaContent[strings.Index(SchemaContent, "type UpdateRollout {"):strings.Index(SchemaContent, "type UpdateRolloutTarget {")]
	require.Greater(t, strings.Index(rollout, "    cohorts:"), strings.Index(rollout, "    pauseReason:"))
	require.Less(t, strings.Index(rollout, "    haltSamples:"), strings.Index(rollout, "    targetCount:"))
}

func TestRolloutSafetyRequiresFixedOptInOnTheWire(t *testing.T) {
	p, _ := rolloutPlane(t)
	writer := p.ctx("acme", auth.ArtifactWrite)
	for _, policy := range []string{
		`cohorts: [{name: "all", percentage: 100}]`,
		`failureRate: {percentage: 25, windowSeconds: 600, minimumSamples: 1}`,
	} {
		resp := p.exec(writer, `mutation { createUpdateRollout(request: {token: "disabled", artifactToken: "fw-1", groupToken: "fleet", maxInFlight: 1, `+policy+`}) { token } }`, nil)
		require.Equal(t, model.CodeRolloutSafetyDisabled, code(resp))
	}
}

// The console sends promoteUpdateRollout through a document with variables. A
// variable declared non-null with a default is refused by validation, so the
// exact document the console sends is run here, with and without the override.
const consolePromoteDocument = `
  mutation PromoteUpdateRollout($token: String!, $expectedCohort: Int!, $overrideFailure: Boolean = false) {
    promoteUpdateRollout(token: $token, expectedCohort: $expectedCohort, overrideFailure: $overrideFailure) {
      token currentCohort
    }
  }
`

func TestConsolePromoteDocumentValidatesAndRuns(t *testing.T) {
	p, _ := rolloutPlane(t)
	p.api.RolloutSafety = true
	writer := p.ctx("acme", auth.ArtifactWrite)
	require.Empty(t, p.exec(writer, `mutation { createUpdateRollout(request: {token: "console", artifactToken: "fw-1", groupToken: "fleet", maxInFlight: 1,
 cohorts: [{name: "canary", deviceTokens: ["dev-1"]}, {name: "all", percentage: 100}]}) { token } }`, nil).Errors)
	require.Empty(t, p.exec(writer, `mutation { startUpdateRollout(token: "console") { token } }`, nil).Errors)
	require.NoError(t, p.api.RDB.DB(writer).Model(&model.UpdateAttempt{}).Where("device_token = ?", "dev-1").Update("state", string(ota.StateFailed)).Error)

	// Validation must accept the document (an error here would not mention the failed cohort).
	resp := p.exec(writer, consolePromoteDocument, map[string]any{"token": "console", "expectedCohort": 0})
	require.NotEmpty(t, resp.Errors)
	require.Contains(t, resp.Errors[0].Message, "overrideFailure")
	resp = p.exec(writer, consolePromoteDocument, map[string]any{"token": "console", "expectedCohort": 0, "overrideFailure": true})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"promoteUpdateRollout":{"token":"console","currentCohort":1}}`, string(resp.Data))
}
