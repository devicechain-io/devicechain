// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withHttp(http string) string {
	return `{"infrastructure":{"nats":{"hostname":"h","port":4222},
	         "userManagement":{"hostname":"u","port":8080},
	         "http":` + http + `}}`
}

// infrastructure.http.trustedProxies is read by the load every service runs, and parsed.
func TestLoadInstanceConfigurationReadsTrustedProxies(t *testing.T) {
	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t,
		withHttp(`{"trustedProxies":["10.42.0.0/16","fd00::/8"]}`))))
	got, err := ms.InstanceConfiguration.Infrastructure.Http.TrustedProxyPrefixes()
	require.NoError(t, err)
	assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("fd00::/8")}, got)

	// Omitted: nobody is trusted.
	ms = &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, minimalInstanceDoc)))
	got, err = ms.InstanceConfiguration.Infrastructure.Http.TrustedProxyPrefixes()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Fail closed: a malformed list stops the service at startup, naming the key. A list that
// silently dropped an entry would read as configured and behave as though it were not.
func TestLoadInstanceConfigurationRefusesABadTrustedProxy(t *testing.T) {
	for name, entry := range map[string]string{
		"not a CIDR":             `"ingress-nginx"`,
		"a bare address":         `"10.42.0.1"`,
		"bad prefix length":      `"10.42.0.0/33"`,
		"host bits set (widens)": `"10.42.0.7/16"`,
		"empty":                  `""`,
		"blank":                  `"  "`,
	} {
		ms := &Microservice{}
		err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, withHttp(`{"trustedProxies":["10.1.0.0/16",`+entry+`]}`)))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "infrastructure.http.trustedProxies", name)
	}

	// A misspelled key is refused too, not ignored.
	ms := &Microservice{}
	err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, withHttp(`{"trustedProxy":["10.1.0.0/16"]}`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trustedProxy")
}
