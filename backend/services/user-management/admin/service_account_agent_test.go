// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// serviceAccounts.agentFlagRequiredFor: with the tenant's policy listing command:write, an
// ordinary account holding it is refused, an agent account is not, and a tenant with no policy
// requires nothing (the rule is opt-in, never a default).
func TestAgentFlagRequiredForRefusesAnOrdinaryAccount(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&iam.AgentPolicy{}, &iam.AgentPolicyVersion{}))
	store := iam.NewStore(&rdb.RdbManager{Database: db})
	ctx := context.Background()
	writer := []iam.Role{{Token: "ops", Authorities: []string{"device:read", "command:write"}}}
	reader := []iam.Role{{Token: "viewer", Authorities: []string{"device:read"}}}
	admin := []iam.Role{{Token: "admin", Authorities: []string{"*"}}}

	require.NoError(t, requireAgentFlag(ctx, store, "acme", false, writer), "no policy: nothing is required")

	_, err = store.PublishAgentPolicy(ctx, "acme", "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return `{"serviceAccounts":{"agentFlagRequiredFor":["command:write"]}}`, iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)

	err = requireAgentFlag(ctx, store, "acme", false, writer)
	var ref *integrity.Refusal
	require.ErrorAs(t, err, &ref, "an ordinary account holding command:write must be refused")
	require.ErrorAs(t, requireAgentFlag(ctx, store, "acme", false, admin), &ref, `"*" holds command:write`)
	require.NoError(t, requireAgentFlag(ctx, store, "acme", true, writer), "an agent account is allowed")
	require.NoError(t, requireAgentFlag(ctx, store, "acme", false, reader), "an account without the authority is allowed")
	require.NoError(t, requireAgentFlag(ctx, store, "globex", false, writer), "another tenant's policy does not apply")
}
