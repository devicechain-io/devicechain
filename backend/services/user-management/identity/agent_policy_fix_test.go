// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// serviceAccounts.agentFlagRequiredFor is applied at EVERY mint, against the roles as they are now:
// an ordinary account whose role later gained a listed authority stops receiving tokens.
func TestAgentFlagRequirementEnforcedAtMint(t *testing.T) {
	e := newCCEnv(t)
	ctx := context.Background()
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	rec := e.grant(t, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, "control: the account mints")

	_, err := e.store.PublishAgentPolicy(ctx, "acme", "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return `{"serviceAccounts":{"agentFlagRequiredFor":["command:write"]}}`, iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)
	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, "an account not holding the listed authority still mints")

	// The role is edited after the fact to grant command:write.
	sys := e.m.db.Database.WithContext(core.WithSystemContext(ctx))
	var role iam.Role
	require.NoError(t, sys.Where("token = ?", "ci-role").First(&role).Error)
	role.Authorities = []string{"device:read", "command:write"}
	require.NoError(t, sys.Save(&role).Error)

	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_grant", decode(t, rec).Error)
}

// A scoped member, a disabled identity, a disabled membership and a person with no second factor
// are not eligible approvers: a
// two-person verdict with only such people available is a refusal.
func TestIneligibleApproversAreNotCounted(t *testing.T) {
	e := newAgentEnv(t, string(auth.CommandWrite))
	ctx := context.Background()
	e.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers"]},
		"tools":{"commands":{"commands":["wipe"]}}}`)
	role := &iam.Role{Scope: iam.ScopeTenant, Token: "approvers", Authorities: []string{string(auth.CommandWrite)}}
	require.NoError(t, e.store.CreateRole(ctx, role))
	add := func(email string, idEnabled, memEnabled bool, scope []string, factor bool) {
		require.NoError(t, e.store.CreateIdentity(ctx, &iam.Identity{Email: email, Enabled: true, PasswordHash: "x",
			Memberships: []iam.Membership{{TenantId: agentTenant, Enabled: memEnabled, Scope: scope, TenantRoles: []iam.Role{*role}}}}))
		if factor {
			e.enrollFactor(t, email)
		}
		if !idEnabled {
			require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.Identity{}).
				Where("email = ?", email).Update("enabled", false).Error)
		}
	}
	add("ok@example.invalid", true, true, nil, true)
	add("scoped@example.invalid", true, true, []string{"customer:c1"}, true)
	add("gone@example.invalid", false, true, nil, true)
	add("off@example.invalid", true, false, nil, true)
	add("nofactor@example.invalid", true, true, nil, false)

	agent := &auth.Claims{Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, Scope: "act:commands",
		Authorities: []string{string(auth.CommandPropose)}, TokenType: auth.TokenTypeAccess}
	req := agentpolicy.Request{Kind: agentpolicy.KindCommandSend, Impact: agentpolicy.ImpactDestructive, Command: "wipe", TargetCount: 1}
	d, err := e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict, "only one eligible approver: two-person must refuse")
	require.Contains(t, d.Reasons, agentpolicy.ReasonTooFewApprovers)

	add("second@example.invalid", true, true, nil, true)
	d, err = e.m.AgentActuationDecision(ctx, agent, req)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict, "control: two eligible approvers")
	require.Equal(t, 2, d.RequiredApprovals)
}

// Each suspension and restoration is its own journal row naming the operator.
func TestSuspensionTransitionsAreJournaled(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	_, err := e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, true, "op@example.invalid")
	require.NoError(t, err)
	_, err = e.m.SetTenantAgentActuationSuspended(ctx, agentTenant, false, "op2@example.invalid")
	require.NoError(t, err)
	var evs []rdb.AuditEvent
	require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(ctx)).
		Where("operation IN ?", []string{rdb.AuditOpAgentActuationSuspended, rdb.AuditOpAgentActuationRestored}).
		Order("id").Find(&evs).Error)
	require.Len(t, evs, 2)
	require.Equal(t, rdb.AuditOpAgentActuationSuspended, evs[0].Operation)
	require.Equal(t, "op@example.invalid", evs[0].Actor)
	require.Equal(t, rdb.AuditOpAgentActuationRestored, evs[1].Operation)
	require.Equal(t, "op2@example.invalid", evs[1].Actor)
}

// The kill switch never depends on reading the policy it stops: over a document this pod cannot
// read (written by a newer release, or damaged), turning actuation off still publishes a disabled
// policy without a second factor; turning it on over one is refused.
func TestKillSwitchWorksOverAnUnreadablePolicy(t *testing.T) {
	for name, c := range map[string]struct {
		doc  string
		want []string
	}{
		// future:write is an authority only the newer release knows: it is carried VERBATIM, so
		// the rule still holds on the pods that can read it once the rollout finishes.
		"newer version": {`{"version":"v9","actuation":"enabled","clients":["agent-desktop"],"futureKey":1,"serviceAccounts":{"agentFlagRequiredFor":["command:write","future:write"]}}`,
			[]string{"command:write", "future:write"}},
		"damaged": {`{"version":"v1alpha1","actuation":"enabled","typo":1,"serviceAccounts":{"agentFlagRequiredFor":["command:write"]}}`,
			[]string{"command:write"}},
	} {
		doc := c.doc
		t.Run(name, func(t *testing.T) {
			e := newAgentEnv(t)
			ctx := context.Background()
			_, err := e.store.PublishAgentPolicy(ctx, agentTenant, "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
				return doc, iam.AgentPolicyOriginPublish, false, nil
			})
			require.NoError(t, err)
			noFactor := adminClaims(nil, auth.AmrPasswordOnly())
			view, err := e.m.SetTenantAgentActuation(ctx, noFactor, false)
			require.NoError(t, err)
			d, err := agentpolicy.ParseStored([]byte(view.Published.Document))
			require.NoError(t, err)
			require.False(t, d.ActuationOn())
			require.ElementsMatch(t, c.want, d.AgentFlagRequiredFor(),
				"the kill switch must carry the service-account rule forward verbatim, never drop an entry")

			_, err = e.store.PublishAgentPolicy(ctx, agentTenant, "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
				return doc, iam.AgentPolicyOriginPublish, false, nil
			})
			require.NoError(t, err)
			now := time.Now()
			_, err = e.m.SetTenantAgentActuation(ctx, adminClaims(&now, auth.AmrPasswordOTP()), true)
			require.Error(t, err, "enabling over an unreadable policy must be refused")
		})
	}
}

// The mint path fails closed on a damaged policy of a known version (the tenant's service-account
// rule must not lapse), and does not stop on a newer release's document mid-upgrade.
func TestMintFailsClosedOnADamagedPolicy(t *testing.T) {
	e := newCCEnv(t)
	ctx := context.Background()
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	publish := func(doc string) {
		_, err := e.store.PublishAgentPolicy(ctx, "acme", "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
			return doc, iam.AgentPolicyOriginPublish, false, nil
		})
		require.NoError(t, err)
	}
	publish(`{"version":"v1alpha1","serviceAccounts":{"agentFlagRequiredFor":["command:write"]},"typo":1}`)
	rec := e.grant(t, id, secret)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	publish(`{"version":"v9","futureKey":1}`)
	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A newer release's rule is enforced on this pod too: the role grows command:write, and the
	// rule written in an unknown format still refuses the ordinary account's mint.
	sys := e.m.db.Database.WithContext(core.WithSystemContext(ctx))
	var role iam.Role
	require.NoError(t, sys.Where("token = ?", "ci-role").First(&role).Error)
	role.Authorities = []string{"device:read", "command:write"}
	require.NoError(t, sys.Save(&role).Error)
	publish(`{"version":"v9","serviceAccounts":{"agentFlagRequiredFor":["command:write"]},"futureKey":1}`)
	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_grant", decode(t, rec).Error)
}

// A document no pod can read at all (not even JSON): actuation is already off everywhere, and the
// kill switch publishes nothing rather than a policy that would lift the service-account rule.
func TestKillSwitchPublishesNothingOverAnUnparseableDocument(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	_, err := e.store.PublishAgentPolicy(ctx, agentTenant, "test", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return `{"version":"v9","serviceAccounts":{"agentFlagRequiredFor":"not-a-list"}}`, iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)
	view, err := e.m.SetTenantAgentActuation(ctx, adminClaims(nil, auth.AmrPasswordOnly()), false)
	require.NoError(t, err)
	require.Equal(t, 1, view.Published.Version, "nothing must be published")
}

// 🔴 Nothing but the kill switch changes a policy this release cannot read: a publish and a rollback
// over one are refused, with a second factor or without, because a loosening cannot be judged
// against a predecessor nobody here can read. The kill switch still works.
func TestPublishAndRollbackRefusedOverAnUnreadablePolicy(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	now := time.Now()
	fresh := adminClaims(&now, auth.AmrPasswordOTP())
	_, err := e.m.SaveAgentPolicyDraft(ctx, fresh, `{"actuation":"disabled"}`)
	require.NoError(t, err)
	_, err = e.m.PublishAgentPolicyDraft(ctx, fresh)
	require.NoError(t, err, "control: v1 is readable")

	_, err = e.store.PublishAgentPolicy(ctx, agentTenant, "newer-pod", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return `{"version":"v9","actuation":"enabled","serviceAccounts":{"agentFlagRequiredFor":["future:write"]}}`, iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)

	_, err = e.m.SaveAgentPolicyDraft(ctx, fresh, `{"actuation":"disabled"}`)
	require.NoError(t, err, "a draft changes nothing in force and may still be saved")
	_, err = e.m.PublishAgentPolicyDraft(ctx, fresh)
	require.Equal(t, AgentPolicyUnreadable, policyErrCode(err), "publish over an unreadable policy")
	_, err = e.m.RollbackAgentPolicy(ctx, fresh, 1)
	require.Equal(t, AgentPolicyUnreadable, policyErrCode(err), "rollback over an unreadable policy")

	view, err := e.m.SetTenantAgentActuation(ctx, adminClaims(nil, auth.AmrPasswordOnly()), false)
	require.NoError(t, err, "the kill switch still works, without a second factor")
	d, err := agentpolicy.ParseStored([]byte(view.Published.Document))
	require.NoError(t, err)
	require.Equal(t, []string{"future:write"}, d.AgentFlagRequiredFor())
}

// 🔴 Every writer stores Canonical() of a parsed document: the version is always written and the
// frozen key is always spelt the one way, which is what a later release relies on to read it.
func TestEveryWriterStoresTheCanonicalForm(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	now := time.Now()
	fresh := adminClaims(&now, auth.AmrPasswordOTP())
	// Deliberately non-canonical input: whitespace, key order, no version.
	_, err := e.m.SaveAgentPolicyDraft(ctx, fresh, ` { "serviceAccounts" : {"agentFlagRequiredFor":["command:write"]}, "actuation":"disabled" } `)
	require.NoError(t, err)
	head, err := e.store.AgentPolicyHead(ctx, agentTenant)
	require.NoError(t, err)
	stored := []string{*head.Draft}
	_, err = e.m.PublishAgentPolicyDraft(ctx, fresh)
	require.NoError(t, err)
	_, err = e.m.SetTenantAgentActuation(ctx, fresh, false)
	require.NoError(t, err)
	_, err = e.m.RollbackAgentPolicy(ctx, fresh, 1)
	require.NoError(t, err)
	vs, err := e.m.AgentPolicyVersions(ctx, agentTenant, 10)
	require.NoError(t, err)
	require.Len(t, vs, 3)
	for _, v := range vs {
		stored = append(stored, v.Document)
	}
	for _, s := range stored {
		d, err := agentpolicy.ParseStored([]byte(s))
		require.NoError(t, err)
		c, err := d.Canonical()
		require.NoError(t, err)
		require.Equal(t, string(c), s, "a stored document must be exactly the canonical form")
		require.Contains(t, s, `"version":"v1alpha1"`)
	}
}

// nearLimitPolicy builds a VALID, readable document whose submitted form is just under the size
// limit and whose stored (canonical) form, which always adds the version, is over it.
func nearLimitPolicy(t *testing.T, actuation string) string {
	t.Helper()
	// Tight settings beside the clients, so a minimal fallback would read as a LOOSENING of them:
	// the kill switch must not be judged on that.
	prefix := `{"actuation":"` + actuation + `","approvers":{"roles":["r"],"stepUpMaxAge":"5m"},"expiry":{"default":"10m"},"clients":[`
	var clients []string
	size := len(prefix) + 2
	for i := 0; ; i++ {
		c := fmt.Sprintf(`"c%03d-%s"`, i, strings.Repeat("x", 250))
		if size+len(c)+1 > agentpolicy.MaxDocumentBytes-8 {
			break
		}
		clients = append(clients, c)
		size += len(c) + 1
	}
	// One last entry fills the remaining room, so the submitted form sits a few bytes under the
	// limit and the version the canonical form adds tips it over.
	if pad := agentpolicy.MaxDocumentBytes - 4 - size - 3; pad > 0 {
		clients = append(clients, `"`+strings.Repeat("y", min(pad, 256))+`"`)
	}
	raw := prefix + strings.Join(clients, ",") + "]}"
	require.LessOrEqual(t, len(raw), agentpolicy.MaxDocumentBytes)
	d, err := agentpolicy.ParseStored([]byte(raw))
	require.NoError(t, err, "the near-limit document must be readable as stored")
	b, err := d.Canonical()
	require.NoError(t, err)
	require.Greater(t, len(b), agentpolicy.MaxDocumentBytes, "its canonical form must be over the limit")
	return raw
}

// 🔴 THE OFF SWITCH IS NEVER REFUSED BY SIZE OR VALIDATION OF THE POLICY IN FORCE. A readable policy
// whose stored form would be over the limit, and a stored policy full of characters JSON escapes
// (written before the token grammar held list entries), are both killable without a second factor,
// and the service-account rule survives.
func TestKillSwitchSurvivesOversizedAndEscapingPolicies(t *testing.T) {
	escaping := `{"version":"v1alpha1","actuation":"enabled","serviceAccounts":{"agentFlagRequiredFor":["command:write"]},"clients":[`
	var cs []string
	size := len(escaping) + 2
	// About 16.5 KB as written, within every list limit, and roughly six times that once JSON
	// escapes each '<': the shape the final review found could refuse the kill switch.
	for i := 0; size < 16500; i++ {
		c := fmt.Sprintf(`"%d%s"`, i, strings.Repeat("<", 200))
		cs = append(cs, c)
		size += len(c) + 1
	}
	escaping += strings.Join(cs, ",") + "]}"
	require.LessOrEqual(t, len(escaping), agentpolicy.MaxDocumentBytes)

	for name, doc := range map[string]string{
		"canonical over the limit": nearLimitPolicy(t, "enabled"),
		"escapable characters":     escaping,
	} {
		t.Run(name, func(t *testing.T) {
			e := newAgentEnv(t)
			ctx := context.Background()
			_, err := e.store.PublishAgentPolicy(ctx, agentTenant, "legacy", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
				return doc, iam.AgentPolicyOriginPublish, false, nil
			})
			require.NoError(t, err)
			view, err := e.m.SetTenantAgentActuation(ctx, adminClaims(nil, auth.AmrPasswordOnly()), false)
			require.NoError(t, err, "the kill switch must not be refused")
			require.Equal(t, 2, view.Published.Version)
			require.LessOrEqual(t, len(view.Published.Document), agentpolicy.MaxDocumentBytes)
			d, err := agentpolicy.ParseStored([]byte(view.Published.Document))
			require.NoError(t, err, "what the kill switch stores must be readable")
			require.False(t, d.ActuationOn())
			if name == "escapable characters" {
				require.Equal(t, []string{"command:write"}, d.AgentFlagRequiredFor())
			}
		})
	}
}

// Every write checks the stored form's size: a draft whose canonical form would be over the limit
// is refused at save, and a rollback to an oversized version is refused (it is not a kill).
func TestEveryWriteChecksTheStoredSize(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	now := time.Now()
	fresh := adminClaims(&now, auth.AmrPasswordOTP())
	_, err := e.m.SaveAgentPolicyDraft(ctx, fresh, nearLimitPolicy(t, "disabled"))
	require.Equal(t, AgentPolicyInvalid, policyErrCode(err))

	_, err = e.store.PublishAgentPolicy(ctx, agentTenant, "legacy", func(*iam.AgentPolicy, *iam.AgentPolicyVersion) (string, string, bool, error) {
		return nearLimitPolicy(t, "disabled"), iam.AgentPolicyOriginPublish, false, nil
	})
	require.NoError(t, err)
	_, err = e.m.SaveAgentPolicyDraft(ctx, fresh, `{"actuation":"disabled"}`)
	require.NoError(t, err)
	_, err = e.m.PublishAgentPolicyDraft(ctx, fresh)
	require.NoError(t, err)
	_, err = e.m.RollbackAgentPolicy(ctx, fresh, 1)
	require.Equal(t, AgentPolicyInvalid, policyErrCode(err))
}
