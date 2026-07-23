// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validConfig is a minimal document that passes Validate after ApplyDefaults. Each
// negative test starts from a copy and breaks exactly one field, so a failure names
// the constraint under test.
func validConfig() Lwm2mConfiguration {
	return Lwm2mConfiguration{
		Security: SecurityConfiguration{
			Identities: []PskIdentity{{
				Identity: "dev-1", PskEnv: "DC_LWM2M_PSK_DEV1",
				Tenant: "acme", ExternalId: "plant-a/sensor-1",
			}},
		},
	}
}

// loaded applies defaults then validates, mirroring core.LoadConfiguration's order.
func loaded(c Lwm2mConfiguration) error {
	c.ApplyDefaults()
	return c.Validate()
}

func TestApplyDefaultsFillsZeroValues(t *testing.T) {
	c := validConfig()
	c.ApplyDefaults()
	assert.Equal(t, DefaultListenHost, c.Listen.Host)
	assert.Equal(t, DefaultListenPort, c.Listen.Port)
	require.NotNil(t, c.Security.ConnectionIdLength)
	assert.Equal(t, DefaultConnectionIdLength, *c.Security.ConnectionIdLength)
	assert.Equal(t, DefaultHandshakeTimeoutSeconds, c.Security.HandshakeTimeoutSeconds)
	assert.Equal(t, DefaultMaxSessions, c.Security.MaxSessions)
}

func TestApplyDefaultsDoesNotOverrideSetValues(t *testing.T) {
	c := validConfig()
	c.Listen.Host = "127.0.0.1"
	c.Listen.Port = 15684
	c.Security.HandshakeTimeoutSeconds = 30
	c.Security.MaxSessions = 5
	c.ApplyDefaults()
	assert.Equal(t, "127.0.0.1", c.Listen.Host)
	assert.Equal(t, 15684, c.Listen.Port)
	assert.Equal(t, 30, c.Security.HandshakeTimeoutSeconds)
	assert.Equal(t, 5, c.Security.MaxSessions)
}

// An explicit CID length of 0 (disable) must be PRESERVED, not defaulted back on — the
// whole reason the field is a pointer (nil = omitted → default; 0 = off).
func TestConnectionIdLengthZeroIsPreserved(t *testing.T) {
	c := validConfig()
	zero := 0
	c.Security.ConnectionIdLength = &zero
	c.ApplyDefaults()
	require.NotNil(t, c.Security.ConnectionIdLength)
	assert.Equal(t, 0, *c.Security.ConnectionIdLength)
	assert.Equal(t, 0, c.ConnectionIdLengthOrDefault())
	require.NoError(t, c.Validate())
}

func TestValidateAcceptsValid(t *testing.T) {
	require.NoError(t, loaded(validConfig()))
}

// An empty identity set is a valid, inert posture: the server binds but authenticates
// no one until credentials are provisioned. It must not be rejected as if it were a
// misconfiguration.
func TestValidateAcceptsEmptyIdentities(t *testing.T) {
	c := validConfig()
	c.Security.Identities = nil
	require.NoError(t, loaded(c))
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Lwm2mConfiguration)
		want   string
	}{
		{"port out of range high", func(c *Lwm2mConfiguration) { c.Listen.Port = 70000 }, "out of range"},
		{"port negative", func(c *Lwm2mConfiguration) { c.Listen.Port = -1 }, "out of range"},
		{"cid too long", func(c *Lwm2mConfiguration) { n := MaxConnectionIdLength + 1; c.Security.ConnectionIdLength = &n }, "out of range"},
		{"cid negative", func(c *Lwm2mConfiguration) { n := -1; c.Security.ConnectionIdLength = &n }, "out of range"},
		{"idle negative", func(c *Lwm2mConfiguration) { c.Security.IdleTimeoutSeconds = -1 }, "must be >= 0"},
		{"identity missing name", func(c *Lwm2mConfiguration) { c.Security.Identities[0].Identity = "" }, "identity is required"},
		{"identity missing pskEnv", func(c *Lwm2mConfiguration) { c.Security.Identities[0].PskEnv = "" }, "pskEnv is required"},
		{"duplicate identity", func(c *Lwm2mConfiguration) {
			c.Security.Identities = append(c.Security.Identities, PskIdentity{Identity: "dev-1", PskEnv: "DC_LWM2M_PSK_DEV1B"})
		}, "duplicate PSK identity"},
		{"missing tenant", func(c *Lwm2mConfiguration) { c.Security.Identities[0].Tenant = "" }, "tenant is required"},
		{"missing externalId", func(c *Lwm2mConfiguration) { c.Security.Identities[0].ExternalId = "" }, "externalId is required"},
		{"autoRegister without deviceTypeToken", func(c *Lwm2mConfiguration) {
			c.Security.Identities[0].AutoRegister = true // DeviceTypeToken left empty
		}, "deviceTypeToken is required when autoRegister is set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			// Port cases need to bypass the port default (which fills 0), so set a base
			// port the mutate can override; others use the defaulted value.
			tc.mutate(&c)
			err := loaded(c)
			require.Error(t, err, "expected an error containing %q", tc.want)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// A duplicate (tenant, externalId) across two DISTINCT identities is deliberately
// allowed — it is the credential-rotation overlap (two live PSKs for one device while a
// new key is provisioned). The presence epoch guard converges it (whichever registers
// later supersedes). This pins that the config does NOT reject it, so nobody "fixes" the
// overlap with a uniqueness check.
func TestValidateAllowsDuplicateTenantExternalIdAcrossIdentities(t *testing.T) {
	c := validConfig()
	c.Security.Identities = append(c.Security.Identities, PskIdentity{
		Identity: "dev-1-newkey", PskEnv: "DC_LWM2M_PSK_DEV1_NEW",
		Tenant: "acme", ExternalId: "plant-a/sensor-1", // SAME tenant + externalId, different identity
	})
	require.NoError(t, loaded(c), "an overlapping (tenant, externalId) across two identities must be accepted")
}

// AutoRegister WITH a deviceTypeToken is accepted (the happy path for the ALLOW_NEW
// posture) — the counterweight to the missing-deviceTypeToken rejection.
func TestValidateAcceptsAutoRegisterWithDeviceType(t *testing.T) {
	c := validConfig()
	c.Security.Identities[0].AutoRegister = true
	c.Security.Identities[0].DeviceTypeToken = "sensor"
	require.NoError(t, loaded(c))
}

// Bindings maps each identity to its resolved tenancy binding — what a registration
// handler resolves through after recovering the authenticated PSK identity (ADR-075 D1).
func TestBindingsResolveEachIdentity(t *testing.T) {
	c := validConfig()
	c.Security.Identities[0].AutoRegister = true
	c.Security.Identities[0].DeviceTypeToken = "sensor"
	require.NoError(t, loaded(c))
	b := c.Bindings()
	require.Len(t, b, 1)
	got, ok := b["dev-1"]
	require.True(t, ok, "the authenticated identity must have a binding")
	assert.Equal(t, PskBinding{Tenant: "acme", ExternalId: "plant-a/sensor-1", DeviceTypeToken: "sensor", AutoRegister: true}, got)
}

// The port range check must fire on the DEFAULTED value too: a document that omits the
// port gets 5684 (valid), so the guard must not reject the zero value before
// ApplyDefaults runs. LoadConfiguration guarantees that order; this pins it.
func TestPortDefaultIsValid(t *testing.T) {
	c := validConfig() // Port left 0
	require.NoError(t, loaded(c))
}

func TestResolveCredentialsDecodesBase64(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes (>= MinPskBytes)
	t.Setenv("DC_LWM2M_PSK_DEV1", base64.StdEncoding.EncodeToString(key))
	c := validConfig()
	c.ApplyDefaults()
	creds, err := c.ResolveCredentials()
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.Equal(t, key, creds["dev-1"])
}

func TestResolveCredentialsFailsClosed(t *testing.T) {
	t.Run("empty env is refused", func(t *testing.T) {
		// DC_LWM2M_PSK_DEV1 is unset in this subtest.
		c := validConfig()
		_, err := c.ResolveCredentials()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "environment variable is empty")
	})
	t.Run("non-base64 is refused", func(t *testing.T) {
		t.Setenv("DC_LWM2M_PSK_DEV1", "not valid base64 !!!")
		c := validConfig()
		_, err := c.ResolveCredentials()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not valid base64")
	})
	t.Run("whitespace-only (decodes to empty) is refused", func(t *testing.T) {
		t.Setenv("DC_LWM2M_PSK_DEV1", "   ")
		c := validConfig()
		_, err := c.ResolveCredentials()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least")
	})
	t.Run("too-short key is refused", func(t *testing.T) {
		t.Setenv("DC_LWM2M_PSK_DEV1", base64.StdEncoding.EncodeToString([]byte("short")))
		c := validConfig()
		_, err := c.ResolveCredentials()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least")
	})
}

func TestResolveCredentialsEmptyIdentitiesIsNonNilMap(t *testing.T) {
	c := validConfig()
	c.Security.Identities = nil
	creds, err := c.ResolveCredentials()
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Empty(t, creds)
}

// A rendered chart document must round-trip through the struct (the chart↔service
// seam). This pins the JSON field names the chart emits against the struct tags; a
// drift crash-loops the pod, invisible to a hand-built struct.
func TestJsonFieldNamesMatchRenderedShape(t *testing.T) {
	rendered := `{"listen":{"host":"0.0.0.0","port":5684},"security":{"connectionIdLength":8,"handshakeTimeoutSeconds":10,"idleTimeoutSeconds":0,"maxSessions":50000,"identities":[{"identity":"dev-1","pskEnv":"DC_LWM2M_PSK_DEV1","tenant":"acme","externalId":"plant-a/sensor-1","deviceTypeToken":"sensor","autoRegister":true}]}}`
	// Reject unknown fields the way core.LoadConfiguration does, so a stray key fails.
	dec := json.NewDecoder(strings.NewReader(rendered))
	dec.DisallowUnknownFields()
	var c Lwm2mConfiguration
	require.NoError(t, dec.Decode(&c))
	assert.Equal(t, 5684, c.Listen.Port)
	require.NotNil(t, c.Security.ConnectionIdLength)
	assert.Equal(t, 8, *c.Security.ConnectionIdLength)
	assert.Equal(t, 50000, c.Security.MaxSessions)
	require.Len(t, c.Security.Identities, 1)
	assert.Equal(t, "dev-1", c.Security.Identities[0].Identity)
	assert.Equal(t, "DC_LWM2M_PSK_DEV1", c.Security.Identities[0].PskEnv)
	assert.Equal(t, "acme", c.Security.Identities[0].Tenant)
	assert.Equal(t, "plant-a/sensor-1", c.Security.Identities[0].ExternalId)
	assert.Equal(t, "sensor", c.Security.Identities[0].DeviceTypeToken)
	assert.True(t, c.Security.Identities[0].AutoRegister)
}
