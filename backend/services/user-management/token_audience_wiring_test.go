// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/identity"
)

// 🔴 THIS TESTS THE CALLER, NOT THE FUNCTION. The authorization server's resource allowlist is
// proved in package identity by installing it on a Manager directly; nothing there shows that
// the Manager main builds is given the instance document's block. These build the Manager the
// way main does, through newIdentityManager.

func TestTheManagerBuiltLikeMainRunsTheInstancesTokenAudienceMode(t *testing.T) {
	build := func(block mscfg.TokenAudienceConfiguration) (*identity.Manager, error) {
		return newIdentityManager(nil, nil, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil,
			mscfg.InfrastructureConfiguration{TokenAudience: block})
	}

	m, err := build(mscfg.TokenAudienceConfiguration{Enforcement: "enforce"})
	require.NoError(t, err)
	require.Equal(t, mscfg.TokenAudienceEnforce, m.ResourceEnforcement(),
		"the document's enforcement never reached the Manager main builds")

	// An absent block is the release default, enforce from v1.3.
	m, err = build(mscfg.TokenAudienceConfiguration{})
	require.NoError(t, err)
	require.Equal(t, mscfg.TokenAudienceEnforce, m.ResourceEnforcement())

	// Programmatic construction retains observe compatibility; documents refuse it at load.

	m, err = build(mscfg.TokenAudienceConfiguration{Enforcement: "observe"})
	require.NoError(t, err)
	require.Equal(t, mscfg.TokenAudienceObserve, m.ResourceEnforcement())
}

func TestTheManagerBuiltLikeMainRefusesAnInvalidTokenAudienceBlock(t *testing.T) {
	_, err := newIdentityManager(nil, nil, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil,
		mscfg.InfrastructureConfiguration{TokenAudience: mscfg.TokenAudienceConfiguration{Enforcement: "sometimes"}})
	require.Error(t, err, "a block the loader would refuse was accepted at wiring")
}

// With a Microservice the counters are built on its registerer and its subsystem; a struct
// literal (as the break-glass build uses) has no registry and must not panic.
func TestTokenAudienceCountersAreBuiltFromTheMicroservice(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "user-management"}
	m, err := newIdentityManager(ms, nil, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil,
		mscfg.InfrastructureConfiguration{TokenAudience: mscfg.TokenAudienceConfiguration{Enforcement: "enforce"}})
	require.NoError(t, err)
	require.Equal(t, mscfg.TokenAudienceEnforce, m.ResourceEnforcement())
}
