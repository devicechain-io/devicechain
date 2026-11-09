// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
)

// requireAgentFlag applies the tenant agent policy's serviceAccounts.agentFlagRequiredFor: an
// account that is NOT flagged as an agent may not hold any of the listed authorities. It runs when
// an account is created and whenever its roles are replaced, so the rule cannot be stepped around
// by creating the account first and granting the roles afterwards.
//
// A tenant with no published policy requires nothing (the rule is opt-in, never a default: it
// would refuse accounts that are valid today). A published policy that cannot be read is an error,
// not "requires nothing": the rule a tenant wrote must not lapse because its row is damaged.
func requireAgentFlag(ctx context.Context, tx *iam.Store, tenant string, agent bool, roles []iam.Role) error {
	if agent {
		return nil
	}
	_, v, err := tx.PublishedAgentPolicy(ctx, tenant)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	// A document of a newer release (mid-upgrade) has the frozen key extracted, so this pod
	// enforces the same rule the new pods do; a damaged one of this release's format is an error,
	// so the rule never lapses.
	required, err := agentpolicy.AgentFlagRequirement([]byte(v.Document))
	if err != nil {
		return fmt.Errorf("the published agent policy of %s cannot be read: %w", tenant, err)
	}
	if len(required) == 0 {
		return nil
	}
	held := map[string]bool{}
	for i := range roles {
		for _, a := range roles[i].Authorities {
			held[a] = true
		}
	}
	for _, r := range required {
		if held[r] || held[string(auth.AuthorityAll)] {
			return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf(
				"this tenant's agent policy requires a service account holding %q to be flagged as an agent", r))
		}
	}
	return nil
}
