// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/stretchr/testify/require"
)

// The document `dcctl broker permissions` prints is the document dcctl gives the broker it
// installs: one generator, two readers. Compared as parsed JSON so indentation is not the test.
func TestBrokerPermissionsPrintsWhatTheInstallGivesTheBroker(t *testing.T) {
	printed, err := brokerPermissionsDocument("acme", "", nil)
	require.NoError(t, err)
	installed, err := bootstrap.BrokerPermissionsJSON("acme")
	require.NoError(t, err)

	var a, b any
	require.NoError(t, json.Unmarshal(printed, &a))
	require.NoError(t, json.Unmarshal([]byte(installed), &b))
	require.Equal(t, b, a)
	require.True(t, bytes.Contains(printed, []byte("dc-device-management-g1")))
}

func TestBrokerPermissionsProfileLimitsTheLogins(t *testing.T) {
	full, err := brokerPermissionsDocument("acme", "full", nil)
	require.NoError(t, err)
	small, err := brokerPermissionsDocument("acme", "ingest-only", nil)
	require.NoError(t, err)
	require.Less(t, len(small), len(full))
	require.Contains(t, string(full), "dc-event-processing-g1")
	require.NotContains(t, string(small), "dc-event-processing-g1", "an area outside the profile must not get a login's block")
	// The shared, administrator and system logins are not area logins: they are always present.
	require.Contains(t, string(small), "dc_service")
	require.Contains(t, string(small), "dc_sys")
}

func TestBrokerPermissionsRefusesWhatItCannotResolve(t *testing.T) {
	_, err := brokerPermissionsDocument("acme", "no-such-profile", nil)
	require.Error(t, err)
	_, err = brokerPermissionsDocument("a.b", "", nil)
	require.Error(t, err, "an instance id that is not one subject token must be refused")

	brokerPermissionsInstance = ""
	err = runBrokerPermissions(brokerPermissionsCmd, nil)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "--instance-id"))
}
