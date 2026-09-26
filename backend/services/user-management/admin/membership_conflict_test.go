// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/stretchr/testify/require"
)

// A second membership for the same identity in the same tenant is a uniqueness
// conflict: it keeps its own sentence and carries the CONFLICT code, which is what
// dcctl's `sim create` tolerates on a re-run. Before the code existed its wording
// matched none of the phrases dcctl looked for, so a re-run stopped here.
func TestAddMembershipTwiceIsAConflict(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")
	_, err := s.CreateIdentity(ctx, CreateIdentityInput{Email: "someone@example.com", Password: "hunter2hunter2", Enabled: true})
	require.NoError(t, err)
	_, err = s.AddMembership(ctx, "someone@example.com", "acme", nil)
	require.NoError(t, err)

	_, err = s.AddMembership(ctx, "someone@example.com", "acme", nil)
	require.ErrorIs(t, err, ErrAlreadyMember)
	require.True(t, conflict.Is(err), "a duplicate membership is not a conflict: %v", err)
	require.Equal(t, "identity already has a membership in this tenant", err.Error())
}
