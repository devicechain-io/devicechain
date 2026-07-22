// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyDefaultsFillsHostIdButNotGroups pins that each source's HostId defaults
// while an empty Groups stays empty — the two have different meaning (all-groups
// vs unset), and defaulting Groups would silently narrow the subscription. A
// configured HostId is preserved.
func TestApplyDefaultsFillsHostIdButNotGroups(t *testing.T) {
	c := &SparkplugConfiguration{Sources: []SparkplugSource{
		{Tenant: "acme", Broker: SourceBroker{URL: "tcp://b:1883"}},
		{Tenant: "globex", HostId: "plant-a", Broker: SourceBroker{URL: "tcp://b2:1883"}},
	}}
	c.ApplyDefaults()
	assert.Equal(t, DefaultHostId, c.Sources[0].HostId)
	assert.Empty(t, c.Sources[0].Groups)
	assert.Equal(t, "plant-a", c.Sources[1].HostId, "a configured HostId is preserved")
}

// TestValidateAcceptsCleanConfig is the counterweight to the rejection cases: a
// well-formed set of sources with real tenants, brokers, and group names must
// pass untouched. An empty Sources list (connect to nothing) is also valid.
func TestValidateAcceptsCleanConfig(t *testing.T) {
	c := &SparkplugConfiguration{Sources: []SparkplugSource{
		{Tenant: "acme", HostId: "devicechain", Broker: SourceBroker{URL: "ssl://a:8883", Username: "u", PasswordEnv: "SPARKPLUG_ACME_PW"}, Groups: []string{"plant-a", "plant-b"}},
		{Tenant: "globex", HostId: "devicechain", Broker: SourceBroker{URL: "tcp://b:1883"}},
	}}
	require.NoError(t, c.Validate())

	require.NoError(t, (&SparkplugConfiguration{}).Validate())
}

// TestValidateRejectsMissingTenantOrBroker proves the two structural requirements
// of connection-scoped tenancy: a source with no tenant has nothing to attribute
// its telemetry to, and a source with no broker URL has nothing to connect to.
func TestValidateRejectsMissingTenantOrBroker(t *testing.T) {
	noTenant := &SparkplugConfiguration{Sources: []SparkplugSource{{HostId: "h", Broker: SourceBroker{URL: "tcp://b:1883"}}}}
	require.Error(t, noTenant.Validate())

	noBroker := &SparkplugConfiguration{Sources: []SparkplugSource{{Tenant: "acme", HostId: "h"}}}
	require.Error(t, noBroker.Validate())
}

// TestValidateRejectsTopicMetacharacters proves the fail-closed guard: any value
// that could escape its MQTT topic level is rejected, for a source's HostId (which
// addresses the retained STATE message) and for a group (which scopes a
// subscription). A '/' in either would silently mis-target.
func TestValidateRejectsTopicMetacharacters(t *testing.T) {
	cases := []struct {
		name string
		src  SparkplugSource
	}{
		{"empty hostId", SparkplugSource{Tenant: "t", HostId: "", Broker: SourceBroker{URL: "tcp://b:1883"}}},
		{"hostId with slash", SparkplugSource{Tenant: "t", HostId: "a/b", Broker: SourceBroker{URL: "tcp://b:1883"}}},
		{"hostId with plus", SparkplugSource{Tenant: "t", HostId: "a+b", Broker: SourceBroker{URL: "tcp://b:1883"}}},
		{"hostId with hash", SparkplugSource{Tenant: "t", HostId: "a#b", Broker: SourceBroker{URL: "tcp://b:1883"}}},
		{"empty group", SparkplugSource{Tenant: "t", HostId: "h", Broker: SourceBroker{URL: "tcp://b:1883"}, Groups: []string{""}}},
		{"group with slash", SparkplugSource{Tenant: "t", HostId: "h", Broker: SourceBroker{URL: "tcp://b:1883"}, Groups: []string{"plant/a"}}},
		{"group with wildcard", SparkplugSource{Tenant: "t", HostId: "h", Broker: SourceBroker{URL: "tcp://b:1883"}, Groups: []string{"+"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, (&SparkplugConfiguration{Sources: []SparkplugSource{tc.src}}).Validate())
		})
	}
}

// TestValidateRejectsSameBrokerDifferentTenant proves the M7 invariant: a broker
// connection is the tenant trust boundary, so two tenants cannot share one broker
// (they would both receive every message, or be told apart only by the spoofable
// Group ID). The normalized-endpoint key also catches a case/host variant of the
// same URL — a source cannot dodge the check by upper-casing the host.
func TestValidateRejectsSameBrokerDifferentTenant(t *testing.T) {
	same := &SparkplugConfiguration{Sources: []SparkplugSource{
		{Tenant: "acme", HostId: "dc-acme", Broker: SourceBroker{URL: "tcp://shared:1883"}},
		{Tenant: "globex", HostId: "dc-globex", Broker: SourceBroker{URL: "tcp://shared:1883"}},
	}}
	require.Error(t, same.Validate())

	variant := &SparkplugConfiguration{Sources: []SparkplugSource{
		{Tenant: "acme", HostId: "dc-acme", Broker: SourceBroker{URL: "tcp://Shared:1883"}},
		{Tenant: "globex", HostId: "dc-globex", Broker: SourceBroker{URL: "tcp://shared:1883"}},
	}}
	require.Error(t, variant.Validate(), "a host-case variant of the same broker must not dodge the check")
}

// TestValidateRejectsHostCollisionOnOneBroker proves two Host Applications for the
// SAME tenant cannot claim the same STATE identity on one broker: they would
// publish conflicting retained STATE and race for one MQTT client id. Distinct
// hostIds for one tenant on a broker, and the same hostId on different brokers, are
// both fine.
func TestValidateRejectsHostCollisionOnOneBroker(t *testing.T) {
	collide := &SparkplugConfiguration{Sources: []SparkplugSource{
		{Tenant: "acme", HostId: "devicechain", Broker: SourceBroker{URL: "tcp://shared:1883"}},
		{Tenant: "acme", HostId: "devicechain", Broker: SourceBroker{URL: "tcp://shared:1883"}},
	}}
	require.Error(t, collide.Validate())

	ok := &SparkplugConfiguration{Sources: []SparkplugSource{
		{Tenant: "acme", HostId: "dc-line-1", Broker: SourceBroker{URL: "tcp://shared:1883"}},
		{Tenant: "acme", HostId: "dc-line-2", Broker: SourceBroker{URL: "tcp://shared:1883"}},
		{Tenant: "globex", HostId: "devicechain", Broker: SourceBroker{URL: "tcp://other:1883"}},
	}}
	require.NoError(t, ok.Validate())
}

// TestValidateRejectsBadBrokerURL proves a scheme-less or unsupported-scheme URL is
// rejected at config time (not deferred to dial), keeping config validity
// self-contained for a lint tool or authoring API.
func TestValidateRejectsBadBrokerURL(t *testing.T) {
	for _, raw := range []string{"broker:1883", "http://broker", "ws://broker/mqtt", "tcp://"} {
		cfg := &SparkplugConfiguration{Sources: []SparkplugSource{{Tenant: "t", HostId: "h", Broker: SourceBroker{URL: raw}}}}
		require.Error(t, cfg.Validate(), "url %q must be rejected", raw)
	}
}
