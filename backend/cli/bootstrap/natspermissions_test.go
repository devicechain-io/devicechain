// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dc-microservice/natsauth"
)

// The generated permissions reach the broker through a variable file, because the document is
// larger than a Windows command line. These pin the three things that make that safe: the file
// holds exactly the generated document, the instance root declares the variable it sets (an
// undeclared name in a var file is an error at best and a silently ignored default at worst), and
// a broker with no authentication leaves no stale file behind.

func TestTheBrokerPermissionsFileCarriesTheGeneratedDocument(t *testing.T) {
	dir := t.TempDir()
	st := &State{Instance: "acme", Values: map[string]string{"natsCalloutIssuerPublic": "AISSUER"}}
	require.NoError(t, writeBrokerPermissionsVarFile(dir, st))

	raw, err := os.ReadFile(filepath.Join(dir, brokerPermissionsVarFile))
	require.NoError(t, err)
	var vars map[string]string
	require.NoError(t, json.Unmarshal(raw, &vars))
	require.Len(t, vars, 1)

	want, err := BrokerPermissionsJSON("acme")
	require.NoError(t, err)
	require.Equal(t, want, vars["nats_service_users"])

	// And it is the document the broker module reads: JSON with the two accounts.
	var doc map[string]map[string]natsauth.Permissions
	require.NoError(t, json.Unmarshal([]byte(vars["nats_service_users"]), &doc))
	require.Contains(t, doc[natsauth.AppAccount], natsauth.ServiceUser)
	require.Contains(t, doc[natsauth.AppAccount], natsauth.LoginUser("device-management"))
	require.Contains(t, doc[natsauth.SystemAccount], natsauth.SysUser)

	// The size is the reason it is a file. If the document ever shrinks under a command line the
	// file is still correct, so this only fails loudly in the direction that matters: a document
	// that fits in a -var today and is on its way to not fitting.
	require.Greater(t, len(want), 8000, "the document is expected to be far larger than a comfortable command line")
}

func TestTheBrokerPermissionsFileIsRemovedWhenThereIsNoBrokerAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, brokerPermissionsVarFile)
	require.NoError(t, os.WriteFile(path, []byte(`{"nats_service_users":"stale"}`), 0o600))

	require.NoError(t, writeBrokerPermissionsVarFile(dir, &State{Instance: "acme", Values: map[string]string{}}))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "a stale permissions file must not outlive the broker authentication it described")

	// Absent already is fine.
	require.NoError(t, writeBrokerPermissionsVarFile(dir, &State{Instance: "acme", Values: map[string]string{}}))
}

func TestTheInstanceRootDeclaresTheVariableTheFileSets(t *testing.T) {
	declared, err := rootDeclaredVars(assets.OpenTofuInstance())
	require.NoError(t, err)
	require.True(t, declared["nats_service_users"], "the instance root must declare nats_service_users")

	// The variable travels in a file, so it must NOT also be a -var: the same value twice is two
	// places to disagree, and the command-line copy is the one that breaks on Windows.
	raw, err := os.ReadFile("tofu.go")
	require.NoError(t, err)
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		require.NotContains(t, trimmed, `"nats_service_users=`, "the permissions document must not be passed as a -var")
	}
}

func TestAnInvalidInstanceStopsTheFileFromBeingWritten(t *testing.T) {
	err := writeBrokerPermissionsVarFile(t.TempDir(), &State{Instance: "a.b", Values: map[string]string{"natsCalloutIssuerPublic": "AISSUER"}})
	require.Error(t, err)
}
