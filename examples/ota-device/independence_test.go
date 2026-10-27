// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package otadevice_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const platformPrefix = "github.com/devicechain-io/"

// depsOf lists every package the two binaries are built from.
func depsOf(t *testing.T, tags string) []string {
	t.Helper()
	args := []string{"list", "-deps", "-f", "{{.ImportPath}}"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./cmd/...")
	out, err := exec.Command("go", args...).CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.Fields(string(out))
}

func leaks(deps []string) []string {
	var out []string
	for _, d := range deps {
		if strings.HasPrefix(d, platformPrefix) {
			out = append(out, d)
		}
	}
	return out
}

// The reference device is built from the published contract alone. It may import the platform in
// its TESTS (to check itself against the platform's golden fixtures) and nowhere else; if it
// imported platform code, a green end-to-end run would prove nothing about the public protocol.
func TestDeviceImportsNothingFromThePlatform(t *testing.T) {
	deps := depsOf(t, "")
	require.Greater(t, len(deps), 20, "the dependency list looks truncated")
	assert.Empty(t, leaks(deps))
}

// Negative control: with a platform package imported by a build-tagged fixture file, the same
// guard must go red. If it did not, the guard above would be reading nothing.
func TestIndependenceGuardDetectsALeak(t *testing.T) {
	assert.NotEmpty(t, leaks(depsOf(t, "otaleak")))
}
