// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// The operations a member limited to part of the tenant may call (ADR-081): reading update
// assignments, asserted on PostgreSQL in scope_integration_test.go, and the ungated,
// non-sensitive service identity.
var scopedOptIns = []string{"updateAssignment", "updateAssignments", "updateManagementInfo"}

// Every other operation, with a well-formed call to each.
var scopedRefusals = map[string]string{
	"artifact":               `{ artifact(token: "fw") { token } }`,
	"artifacts":              `{ artifacts(criteria: {pageNumber: 1, pageSize: 5}) { results { token } } }`,
	"artifactSigner":         `{ artifactSigner(token: "s") { token } }`,
	"artifactSigners":        `{ artifactSigners(criteria: {pageNumber: 1, pageSize: 5}) { results { token } } }`,
	"createArtifact":         `mutation { createArtifact(request: {token: "fw", kind: FIRMWARE, name: "n", version: "1", component: "c", sizeBytes: "1", digest: "sha256:00", compatibleProfiles: ["gateway"]}) { token } }`,
	"retireArtifact":         `mutation { retireArtifact(token: "fw") { token } }`,
	"assignArtifact":         `mutation { assignArtifact(deviceToken: "d", artifactToken: "fw") { token } }`,
	"registerArtifactSigner": `mutation { registerArtifactSigner(name: "n", publicKeyPem: "p") { token } }`,
	"revokeArtifactSigner":   `mutation { revokeArtifactSigner(token: "s") { token } }`,
	"addArtifactSignature":   `mutation { addArtifactSignature(artifactToken: "fw", signerToken: "s", signature: "x") { token } }`,
	"cancelUpdate":           `mutation { cancelUpdate(assignmentToken: "a") { token } }`,
	"abandonUnknownUpdate":   `mutation { abandonUnknownUpdate(assignmentToken: "a", reason: "r") { token } }`,
	// A rollout spans a group of devices, most of them outside any one member's scope.
	"updateRollout":        `{ updateRollout(token: "r") { token } }`,
	"updateRollouts":       `{ updateRollouts(criteria: {pageNumber: 1, pageSize: 5}) { results { token } } }`,
	"updateRolloutTargets": `{ updateRolloutTargets(token: "r", criteria: {pageNumber: 1, pageSize: 5}) { results { deviceToken } } }`,
	"createUpdateRollout":  `mutation { createUpdateRollout(request: {token: "r", artifactToken: "fw", groupToken: "g", maxInFlight: 1}) { token } }`,
	"startUpdateRollout":   `mutation { startUpdateRollout(token: "r") { token } }`,
	"pauseUpdateRollout":   `mutation { pauseUpdateRollout(token: "r") { token } }`,
	"resumeUpdateRollout":  `mutation { resumeUpdateRollout(token: "r") { token } }`,
	"abortUpdateRollout":   `mutation { abortUpdateRollout(token: "r") { token } }`,
}

func rootFields() []string {
	var out []string
	for _, block := range regexp.MustCompile(`(?s)type (?:Query|Mutation) \{(.*?)\n\}`).FindAllStringSubmatch(SchemaContent, -1) {
		for _, m := range regexp.MustCompile(`(?m)^    ([a-zA-Z]+)(?:\(|: [A-Z\[])`).FindAllStringSubmatch(block[1], -1) {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Every operation is classified, so a new one is decided rather than inherited.
func TestEveryOperationIsClassifiedForScopedMembers(t *testing.T) {
	classified := append([]string{}, scopedOptIns...)
	for name := range scopedRefusals {
		classified = append(classified, name)
	}
	sort.Strings(classified)
	require.Equal(t, rootFields(), classified)
}

// 🔴 Every other operation refuses a scoped member with the scope refusal, whatever it holds:
// the artifact catalogue, the signers and every write.
func TestScopedMembersAreRefusedEverythingElse(t *testing.T) {
	p := newPlane(t)
	ctx := context.WithValue(context.Background(), gqlcore.ContextApiKey, p.api)
	ctx = core.WithTenant(ctx, "acme")
	ctx = auth.WithClaims(ctx, &auth.Claims{Tenant: "acme", TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.AuthorityAll)}, ScopeAnchors: []string{"customer:acme"}})
	for name, query := range scopedRefusals {
		resp := p.exec(ctx, query, nil)
		require.NotEmpty(t, resp.Errors, "%s answered a scoped member", name)
		require.True(t, errors.Is(resp.Errors[0].ResolverError, auth.ErrScopeUnsupported),
			"%s: want the scope refusal, got %v", name, resp.Errors[0])
	}
}
