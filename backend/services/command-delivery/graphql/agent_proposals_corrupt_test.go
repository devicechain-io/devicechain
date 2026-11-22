// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/stretchr/testify/require"
)

// A ledger row whose stored JSON does not decode is an error, never an empty list that reads
// as "no targets", "no approvals" or "no reasons".
func TestCorruptProposalListsAreErrors(t *testing.T) {
	r := &AgentProposalResolver{M: model.AgentProposal{Record: actiongate.Record{TargetSet: []byte("{"), Approvals: []byte("{"), Reasons: []byte("{")}}}
	_, err := r.Targets()
	require.Error(t, err)
	_, err = r.Approvals()
	require.Error(t, err)
	_, err = r.Reasons()
	require.Error(t, err)
	ok := &AgentProposalResolver{M: model.AgentProposal{Record: actiongate.Record{TargetSet: []byte(`["d"]`), Approvals: []byte(`[]`), Reasons: []byte(`null`)}}}
	targets, err := ok.Targets()
	require.NoError(t, err)
	require.Equal(t, []string{"d"}, targets)
	reasons, err := ok.Reasons()
	require.NoError(t, err)
	require.Empty(t, reasons)
}
