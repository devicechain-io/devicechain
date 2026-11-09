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

// An agent account can be created only where agent actuation is on: a pod of an earlier release
// would mint its roles' writes, so the account must not exist before the rollout is finished and
// the operator has turned the switch on.
func TestAgentAccountRefusedWhileActuationIsOff(t *testing.T) {
	e, _ := saEnv(t)
	_, _, err := e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "bot", RoleTokens: []string{"operator"}, Agent: true})
	var ref *integrity.Refusal
	require.ErrorAs(t, err, &ref, "an agent account must be refused with the instance switch off")

	e.s.SetAgentActuation(true)
	acct, _, err := e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "bot", RoleTokens: []string{"operator"}, Agent: true})
	require.NoError(t, err, "control: with the switch on it is created")
	require.True(t, acct.Agent)

	e.s.SetAgentActuation(false)
	_, _, err = e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "ci", RoleTokens: []string{"operator"}})
	require.NoError(t, err, "an ordinary account is unaffected by the switch")
}

// A policy written by a NEWER release (an unknown document version) still has its service-account
// rule enforced on this pod (the key is frozen across versions); a damaged one in this release's
// version is an error, so the rule never lapses.
func TestAgentFlagRuleAndDocumentVersions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&iam.AgentPolicy{}, &iam.AgentPolicyVersion{}))
	store := iam.NewStore(&rdb.RdbManager{Database: db})
	ctx := context.Background()
	writer := []iam.Role{{Token: "ops", Authorities: []string{"command:write"}}}
	publish := func(doc string) {
		_, err := store.PublishAgentPolicy(ctx, "acme", "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
			return doc, iam.AgentPolicyOriginPublish, false, nil
		})
		require.NoError(t, err)
	}
	publish(`{"version":"v9","serviceAccounts":{"agentFlagRequiredFor":["command:write"]},"futureKey":1}`)
	var ref *integrity.Refusal
	require.ErrorAs(t, requireAgentFlag(ctx, store, "acme", false, writer), &ref, "the newer release's rule is enforced here too")
	require.NoError(t, requireAgentFlag(ctx, store, "acme", true, writer), "an agent account is still created")
	publish(`{"version":"v9","futureKey":1}`)
	require.NoError(t, requireAgentFlag(ctx, store, "acme", false, writer), "a newer document with no rule does not stop creation")
	publish(`{"version":"v1alpha1","serviceAccounts":{"agentFlagRequiredFor":["command:write"]},"typo":1}`)
	require.Error(t, requireAgentFlag(ctx, store, "acme", false, writer), "a damaged policy of a known version must not lapse")
}

// Service-account client ids own the dcsa_ prefix: an OAuth client may not take one.
func TestOAuthClientIdMayNotUseTheServiceAccountPrefix(t *testing.T) {
	require.Error(t, validateClientId("dcsa_lookalike"))
	require.Error(t, validateClientId("DCSA_lookalike"))
	require.NoError(t, validateClientId("dcs-a_desktop"))
}
