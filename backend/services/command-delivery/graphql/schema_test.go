// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// TestSchemaParses validates that the GraphQL schema parses against the
// resolver — every schema field must have a matching resolver method.
func TestSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(SchemaContent, &SchemaResolver{})
}

// The served proposal shapes are shared with other owners; all additions are Preview.
func TestAgentProposalServedSDL(t *testing.T) {
	const preview = "(Preview: may change in a minor release.)"
	type element struct {
		Name, Description string
		Args              []struct{ Description string }
	}
	type definition struct {
		Description                     string
		Fields, InputFields, EnumValues []element
	}
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{}, gql.UseStringDescriptions())
	read := func(name string) definition {
		res := schema.Exec(context.Background(), `query($n:String!){__type(name:$n){description fields{name description args{description}} inputFields{name description} enumValues{name description}}}`, "", map[string]any{"n": name})
		require.Empty(t, res.Errors)
		var data struct {
			Type definition `json:"__type"`
		}
		require.NoError(t, json.Unmarshal(res.Data, &data))
		return data.Type
	}
	for name, want := range map[string][]string{
		"AgentProposalKind":           {"COMMAND_SEND"},
		"AgentProposalState":          {"PROPOSED", "EXECUTED", "REJECTED", "EXPIRED", "REFUSED", "CANCELLED", "KILLED"},
		"AgentProposalVerdict":        {"REFUSE", "APPROVE"},
		"AgentProposalStatus":         {"DRY_RUN", "PENDING_APPROVAL", "REFUSED", "EXISTING"},
		"AgentCommandProposalInput":   {"deviceToken", "name", "payload", "requestId", "rationale", "expiresAt", "deliveryMode", "maxAttempts", "dryRun"},
		"AgentCommandProposalPreview": {"deviceToken", "incarnation", "command", "payload", "payloadPresent", "impact", "definition", "cutoff", "dispatchAfter", "windowToken"},
		"AgentCommandProposalOutcome": {"status", "verdict", "requiredApprovals", "reasons", "digest", "expiresAt", "targetCount", "preview", "proposal"},
		"AgentProposal":               {"token", "kind", "state", "payload", "targets", "digest", "impact", "targetCount", "proposerPrincipal", "proposerClient", "clientAttested", "rationale", "policyVersion", "requiredApprovals", "approvals", "reasons", "createdAt", "expiresAt", "decidedAt", "decidedBy", "note", "deviceToken", "command", "commandCutoff", "resultRef"},
		"AgentProposalApproval":       {"principal", "approvedAt"},
		"AgentProposalSearchCriteria": {"pageNumber", "pageSize", "state", "kind"},
		"AgentProposalSearchResults":  {"results", "pagination"},
	} {
		t.Run(name, func(t *testing.T) {
			d := read(name)
			require.Contains(t, d.Description, preview)
			var names []string
			for _, field := range append(append(d.Fields, d.InputFields...), d.EnumValues...) {
				names = append(names, field.Name)
				require.Contains(t, field.Description, preview, "%s.%s", name, field.Name)
			}
			sort.Strings(names)
			sort.Strings(want)
			require.Equal(t, want, names)
		})
	}
	operations := map[string]bool{"proposeCommand": true, "approveAgentProposal": true, "rejectAgentProposal": true, "agentProposal": true, "agentProposals": true}
	seen := 0
	for _, root := range []string{"Query", "Mutation"} {
		for _, field := range read(root).Fields {
			if !operations[field.Name] {
				continue
			}
			seen++
			require.Contains(t, field.Description, preview)
			for _, arg := range field.Args {
				require.Contains(t, arg.Description, preview)
			}
		}
	}
	require.Equal(t, 5, seen)
}
