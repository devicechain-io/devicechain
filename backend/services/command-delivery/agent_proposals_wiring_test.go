// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/verify"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/stretchr/testify/require"
)

func TestAgentProposalLifecycleWiring(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	require.NoError(t, err)
	source := string(raw)
	for _, required := range []string{"streams.AgentActuationKills", "processor.NewAgentKillConsumer", "AgentKillConsumer.Initialize", "AgentKillConsumer.Start", "AgentKillConsumer.Stop", "AgentKillConsumer.Terminate", "wireCommandProposals(infra)", "initAgentProposalReaper()", "AgentProposalReaper.Start", "AgentProposalReaper.Stop", "ExpireAgentProposals(ctx)", "PurgeAgentProposals(ctx)"} {
		require.True(t, strings.Contains(source, required), "missing lifecycle wiring: %s", required)
	}
}

func TestCommandProposalWiringUsesCallerClientsWithoutServiceSecret(t *testing.T) {
	previous := Api
	t.Cleanup(func() { Api = previous })
	infra := fullInfra()
	infra.DeviceManagement.Hostname, infra.DeviceManagement.Port = "device-management", 8080
	infra.UserManagement.Hostname, infra.UserManagement.Port = "user-management", 8080
	infra.ServiceAuth.Secret = ""
	for _, enabled := range []bool{false, true} {
		Api = &model.Api{}
		infra.Mcp.CommandProposals = enabled
		wireCommandProposals(infra)
		require.Equal(t, enabled, Api.CommandProposalsEnabled)
		reader, ok := Api.ProposalTargets.(*verify.ProposalTargetReader)
		require.True(t, ok, "proposal targets must use the caller-token reader")
		require.Equal(t, infra.DeviceManagement.Endpoint().GraphQLURL(), reader.URL)
		decider, ok := Api.AgentDecider.(*actiongate.CallerDecider)
		require.True(t, ok)
		require.Equal(t, infra.UserManagement.Endpoint().GraphQLURL(), decider.URL)
	}
	infra.UserManagement.Hostname = ""
	infra.DeviceManagement.Hostname = ""
	wireCommandProposals(infra)
	require.Nil(t, Api.AgentDecider)
	require.Nil(t, Api.ProposalTargets)
}
