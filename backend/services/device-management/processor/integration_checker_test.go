// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/stretchr/testify/require"
)

var integrationRootKey = []byte("0123456789abcdef0123456789abcdef")

// The Checker main.go builds verifies a digest made under the key the Api digests with, and
// no other: a digest under the provisioning or claim key verifies nothing, and the Checker
// refuses to exist without the key.
func TestNewIntegrationCheckerVerifiesOnlyTheIntegrationKey(t *testing.T) {
	keys, err := credential.DeriveEnrollmentSecretKeys(integrationRootKey)
	require.NoError(t, err)
	checker, err := NewIntegrationChecker(keys)
	require.NoError(t, err)

	principal := credential.Principal{Kind: credential.KindIntegration, ID: "acme:chirp-1", Tenant: "acme"}
	lookupOf := func(k *credential.DeviceSecretKey) func(context.Context) (string, error) {
		d, err := k.Digest("acme", "token-1")
		require.NoError(t, err)
		return func(context.Context) (string, error) { return d, nil }
	}
	ctx := context.Background()

	require.NoError(t, checker.Check(ctx, principal, "token-1", lookupOf(keys.Integration)),
		"the paired positive: a digest under the integration key verifies")
	require.ErrorIs(t, checker.Check(ctx, principal, "token-2", lookupOf(keys.Integration)), credential.ErrMismatch)
	for name, wrong := range map[string]*credential.DeviceSecretKey{"provisioning": keys.Provisioning, "claim": keys.Claim} {
		require.ErrorIs(t, checker.Check(ctx, principal, "token-1", lookupOf(wrong)), credential.ErrMismatch,
			"a %s digest authenticated an integration", name)
	}
}

func TestNewIntegrationCheckerRefusesToExistWithoutItsKey(t *testing.T) {
	_, err := NewIntegrationChecker(nil)
	require.Error(t, err)
	_, err = NewIntegrationChecker(&credential.EnrollmentSecretKeys{})
	require.Error(t, err)
}

// The kind stays Unthrottled: a throttled policy would be refused at construction.
func TestIntegrationPoliciesAreUnthrottled(t *testing.T) {
	require.Equal(t, map[credential.Kind]credential.Policy{credential.KindIntegration: {Unthrottled: true}}, IntegrationPolicies)
}
