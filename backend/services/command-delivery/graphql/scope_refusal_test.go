// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"regexp"
	"sort"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// scopedOptIns are the operations a member limited to part of the tenant may call
// (ADR-081). Their behaviour is asserted on a real PostgreSQL in scope_integration_test.go.
var scopedOptIns = []string{"commands", "commandsById", "commandsByToken", "createCommand", "deviceCommandHold"}

// scopedRefusals is every other operation, with a well-formed call to each.
var scopedRefusals = map[string]string{
	"commandBatchesById":     `query { commandBatchesById(ids: ["1"]) { token } }`,
	"commandBatchesByToken":  `query { commandBatchesByToken(tokens: ["b"]) { token } }`,
	"commandBatches":         `query { commandBatches(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`,
	"drainableCommands":      `query { drainableCommands(deviceToken: "device-1") { token } }`,
	"createCommandBatch":     `mutation { createCommandBatch(request: {token: "b", name: "reboot", deviceTokens: ["device-1"], allowPartial: true}) { batch { token } } }`,
	"retryCommandBatch":      `mutation { retryCommandBatch(request: {token: "r", of: "b", deviceTokens: ["device-1"], includeUnknown: false, allowPartial: true}) { batch { token } } }`,
	"cancelCommand":          `mutation { cancelCommand(token: "c") { token } }`,
	"cancelDeviceCommands":   `mutation { cancelDeviceCommands(deviceToken: "device-1") { cancelled } }`,
	"cancelCommandBatch":     `mutation { cancelCommandBatch(token: "b") { cancelled } }`,
	"markCommandSent":        `mutation { markCommandSent(token: "c") }`,
	"confirmCommandDispatch": `mutation { confirmCommandDispatch(token: "c", dispatchNonce: "n") }`,
	"parkCommand":            `mutation { parkCommand(token: "c", dispatchNonce: "n") }`,
	"releaseHeldCommands":    `mutation { releaseHeldCommands(deviceToken: "device-1") }`,
}

// rootFields reads the Query and Mutation field names straight from the served SDL, so an
// operation added later is in this test's sight the moment it exists.
func rootFields(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, block := range regexp.MustCompile(`(?s)type (?:Query|Mutation) \{(.*?)\n\}`).FindAllStringSubmatch(SchemaContent, -1) {
		for _, m := range regexp.MustCompile(`(?m)^    ([a-zA-Z]+)\(`).FindAllStringSubmatch(block[1], -1) {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Every operation is classified: opted in for scoped members, or refused to them. A new
// operation fails here until someone decides which, which is the decision ADR-081 makes
// every resolver's author take — the default (Authorize) already refuses, so this is the
// list being kept honest, not the refusal being kept.
func TestEveryOperationIsClassifiedForScopedMembers(t *testing.T) {
	classified := append([]string{}, scopedOptIns...)
	for name := range scopedRefusals {
		classified = append(classified, name)
	}
	sort.Strings(classified)
	require.Equal(t, rootFields(t), classified)
}

// 🔴 Every operation outside the opt-ins refuses a scoped member with the scope refusal —
// holding every authority it needs, including `*` — before it reads anything. Batches
// fan one actuation over many devices, cancel reaches commands by token without asking
// whose, and the rest are the machine-only lifecycle.
func TestScopedMembersAreRefusedEverythingElse(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	scoped := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.AuthorityAll)}, ScopeAnchors: []string{"customer:acme"}})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for name, query := range scopedRefusals {
		res := schema.Exec(scoped, query, "", nil)
		require.NotEmpty(t, res.Errors, "%s answered a scoped member", name)
		require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrScopeUnsupported),
			"%s: want the scope refusal, got %v", name, res.Errors[0])
	}
}
