// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func audienceDoc(extra string) string {
	return fmt.Sprintf(`{
  "infrastructure": {
    "nats": {"hostname": "dc-nats.dc-system", "port": 4222},
    "userManagement": {"hostname": "user-management", "port": 8080},
    "tokenAudience": {%s}
  }
}`, extra)
}

const chartDerivedAudience = `"areaBaseUrl": "https://iot.example.com/api", "mcpResources": ["https://iot.example.com/api/mcp"]`

func claimsWithAud(aud ...string) *auth.Claims {
	return &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings(aud)}}
}

// The wiring test (M4, M10): loading a document carrying the block installs a policy on the
// gate whose own-area resource is <base>/<area>, and which accepts the MCP resource. Without
// the SetAudiencePolicy call the gate keeps the CLOSED policy and refuses the MCP audience.
func TestLoadInstanceConfigurationInstallsTheAudiencePolicy(t *testing.T) {
	ms := &Microservice{FunctionalArea: "event-management", Readiness: NewReadinessGate()}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	// Before the load: the closed default refuses an MCP-bound token.
	require.Error(t, ms.Readiness.AudiencePolicy().Admit(claimsWithAud("https://iot.example.com/api/mcp"), auth.PlaneGraphQL))
	assert.Equal(t, config.TokenAudienceEnforce, ms.Readiness.AudiencePolicy().Mode())

	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, audienceDoc(chartDerivedAudience))))

	p := ms.Readiness.AudiencePolicy()
	assert.Equal(t, "https://iot.example.com/api/event-management", p.OwnResource(), "own = base + / + the SERVICE's area")
	assert.Equal(t, config.TokenAudienceReleaseDefault, p.Mode(), "absent enforcement is the release default")
	assert.NoError(t, p.Admit(claimsWithAud("https://iot.example.com/api/mcp"), auth.PlaneGraphQL))
	assert.NoError(t, p.Admit(claimsWithAud("https://iot.example.com/api/event-management"), auth.PlaneGraphQL))
	assert.NoError(t, p.Admit(claimsWithAud(), auth.PlaneGraphQL))
}

func TestLoadInstanceConfigurationHonoursAnExplicitMode(t *testing.T) {
	ms := &Microservice{FunctionalArea: "event-management", Readiness: NewReadinessGate()}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, audienceDoc(chartDerivedAudience+`, "enforcement": "enforce"`))))
	p := ms.Readiness.AudiencePolicy()
	assert.Equal(t, config.TokenAudienceEnforce, p.Mode())
	assert.ErrorIs(t, p.Admit(claimsWithAud("https://iot.example.com/api/device-management"), auth.PlaneGraphQL), auth.ErrAudienceRefused)
}

// The counter lands on THIS microservice's registry under its own subsystem, and a second
// load (tests do this) re-installs the policy without an AlreadyRegistered panic and without
// resetting the count.
func TestAudienceCounterRegisteredOncePerMicroservice(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &Microservice{FunctionalArea: "event-management", Readiness: NewReadinessGate()}
	ms.UseMetricsRegistry(reg)
	doc := instanceDoc(t, audienceDoc(chartDerivedAudience))

	require.NoError(t, ms.LoadInstanceConfigurationFrom(doc))
	require.NoError(t, ms.Readiness.AudiencePolicy().Admit(claimsWithAud(), auth.PlaneGraphQL))
	require.NotPanics(t, func() { require.NoError(t, ms.LoadInstanceConfigurationFrom(doc)) })
	require.NoError(t, ms.Readiness.AudiencePolicy().Admit(claimsWithAud(), auth.PlaneGraphQL))

	n, err := testutil.GatherAndCount(ms.metricsReg, "devicechain_eventmanagement_token_audience_checks_total")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 2.0, testutil.ToFloat64(ms.audienceRec.CounterFor(auth.PlaneGraphQL, auth.AudNone, auth.VerdictAccepted)),
		"the second load reused the counter")
}

// M29 / break-glass: a struct-literal Microservice with NO gate loads a document that carries
// the block, without a nil dereference and without installing anything.
func TestAudiencePolicyWiringSurvivesAMicroserviceWithNoGate(t *testing.T) {
	ms := &Microservice{FunctionalArea: "user-management"}
	require.Nil(t, ms.Readiness)
	require.NotPanics(t, func() {
		require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, audienceDoc(chartDerivedAudience))))
	})
	assert.Equal(t, "https://iot.example.com/api", ms.InstanceConfiguration.Infrastructure.TokenAudience.AreaBaseUrl)
}

// A made-up area is a warn, not a failure, and gets no own-area resource.
func TestAudiencePolicyForANonRosterAreaDoesNotFailStartup(t *testing.T) {
	for _, area := range []string{"log-level-test", ""} {
		ms := &Microservice{FunctionalArea: area, Readiness: NewReadinessGate()}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, audienceDoc(chartDerivedAudience))), area)
		assert.Equal(t, "", ms.Readiness.AudiencePolicy().OwnResource(), area)
	}
}

// An invalid block stops the load, naming the key.
func TestLoadInstanceConfigurationRefusesAnInvalidAudienceBlock(t *testing.T) {
	ms := &Microservice{FunctionalArea: "event-management", Readiness: NewReadinessGate()}
	err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, audienceDoc(`"enforcement": "maybe"`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tokenAudience")
	// A misspelled key inside the block is refused by the strict decoder.
	err = ms.LoadInstanceConfigurationFrom(instanceDoc(t, audienceDoc(`"enforcment": "enforce"`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enforcment")
}

// A gate that was never given a policy, and one given nil, are CLOSED.
func TestGateDefaultsToTheClosedPolicy(t *testing.T) {
	g := NewReadinessGate()
	assert.Equal(t, config.TokenAudienceEnforce, g.AudiencePolicy().Mode())
	assert.Error(t, g.AudiencePolicy().Admit(claimsWithAud("https://x.example.org"), auth.PlaneAdmin))
	g.SetAudiencePolicy(nil)
	assert.Equal(t, config.TokenAudienceEnforce, g.AudiencePolicy().Mode())
}
