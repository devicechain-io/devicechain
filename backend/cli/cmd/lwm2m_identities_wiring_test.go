// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

var someIdentities = []bootstrap.Lwm2mIdentity{{Identity: "h", PSK: "AAAAAAAAAAAAAAAAAAAAAA==", Tenant: "t", ExternalID: "e"}}

// --lwm2m-identities and --withdraw-lwm2m-identities exist on both verbs, and what they decide
// reaches the State the pipeline starts from. A deleted copy would leave a flag accepted and
// inert: the identities parsed and never written, or a withdrawal that never withdraws.
func TestLwm2mIdentitiesReachTheBootstrapAndUpgradeOptions(t *testing.T) {
	for _, flag := range []string{"lwm2m-identities", "withdraw-lwm2m-identities"} {
		if bootstrapCmd.Flags().Lookup(flag) == nil || upgradeCmd.Flags().Lookup(flag) == nil {
			t.Fatalf("--%s is missing from a verb", flag)
		}
	}

	bootstrapLwm2mIdentityList = someIdentities
	t.Cleanup(func() { bootstrapLwm2mIdentityList = nil })
	opts := bootstrapOptions("prod", nil, nil)
	if len(opts.Lwm2mIdentities) != 1 {
		t.Fatalf("bootstrap options lost the identities: %v", opts.Lwm2mIdentities)
	}
	st := bootstrapStateFor(opts, bootstrap.ClusterBinding{}, "uid", "local", nil, opts.Lwm2mIdentities, nil, nil, false, bootstrap.EscrowPlan{})
	if len(st.Lwm2mIdentities) != 1 || !st.Lwm2mIdentitiesGiven {
		t.Fatalf("the bootstrap state lost the identities (given=%v)", st.Lwm2mIdentitiesGiven)
	}

	upgradeLwm2mIdentityList = someIdentities
	t.Cleanup(func() { upgradeLwm2mIdentityList = nil })
	if up := upgradeOptions("prod", nil, nil); len(up.Lwm2mIdentities) != 1 {
		t.Fatalf("upgrade options lost the identities: %v", up.Lwm2mIdentities)
	}
	upgradeLwm2mIdentityList = nil
	if up := upgradeOptions("prod", nil, nil); up.Lwm2mIdentities != nil || up.WithdrawLwm2mIdentities {
		t.Fatalf("an upgrade given no file carries identities: %v", up.Lwm2mIdentities)
	}

	bootstrapWithdrawLwm2m, upgradeWithdrawLwm2m = true, true
	t.Cleanup(func() { bootstrapWithdrawLwm2m, upgradeWithdrawLwm2m = false, false })
	if !upgradeOptions("prod", nil, nil).WithdrawLwm2mIdentities {
		t.Fatal("upgrade options lost the withdrawal")
	}
	bootstrapLwm2mIdentityList = nil
	bopts := bootstrapOptions("prod", nil, nil)
	if !bopts.WithdrawLwm2mIdentities {
		t.Fatal("bootstrap options lost the withdrawal")
	}
	st = bootstrapStateFor(bopts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, nil, nil, false, bootstrap.EscrowPlan{})
	if !st.Lwm2mIdentitiesGiven || len(st.Lwm2mIdentities) != 0 {
		t.Fatalf("a withdrawing bootstrap must decide the identities (given=%v)", st.Lwm2mIdentitiesGiven)
	}
}
