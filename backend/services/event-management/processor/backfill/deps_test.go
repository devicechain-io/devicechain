// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forbiddenTrees are packages this package must never depend on, nor anything beneath them.
var forbiddenTrees = map[string]string{
	"github.com/devicechain-io/dc-microservice/messaging": "the broker layer: a backfilled row must never be published",
	"github.com/nats-io/nats.go":                          "the broker client (including nats.go/jetstream)",
	"github.com/devicechain-io/dc-device-management":      "device-management is reached over its GraphQL door, never imported",
}

// forbiddenExact are packages this package must not depend on, matched exactly: the processor
// package is forbidden, and this one lives beneath its directory.
var forbiddenExact = map[string]string{
	"github.com/devicechain-io/dc-event-management/processor": "the persistence processor, whose dependencies include the broker layer",
}

// depsOf lists every package the package in dir depends on, test files excluded.
func depsOf(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.Fields(string(out))
}

// violations returns the forbidden packages among deps.
func violations(deps []string) []string {
	var bad []string
	for _, d := range deps {
		if _, ok := forbiddenExact[d]; ok {
			bad = append(bad, d)
		}
		for f := range forbiddenTrees {
			if d == f || strings.HasPrefix(d, f+"/") {
				bad = append(bad, d)
			}
		}
	}
	return bad
}

// The worker cannot publish: nothing it imports reaches the broker, and it does not import the
// persistence processor that does.
func TestBackfillPackageCannotReachTheBroker(t *testing.T) {
	assert.Empty(t, violations(depsOf(t, ".")),
		"the backfill worker must not depend on the messaging layer, nats.go or the event processor")
}

// The check can fail: the persistence processor, one directory up, depends on all of what is
// forbidden, so the same filter finds it there. Without this the test above could pass over a
// filter that matches nothing.
func TestTheDependencyCheckFindsWhatItForbids(t *testing.T) {
	bad := violations(depsOf(t, ".."))
	joined := strings.Join(bad, " ")
	assert.Contains(t, joined, "dc-microservice/messaging")
	assert.Contains(t, joined, "nats-io/nats.go")
}

// The model package the worker writes through is as clean.
func TestEventManagementModelStaysFreeOfDeviceManagement(t *testing.T) {
	deps := depsOf(t, "../../model")
	for _, d := range deps {
		assert.NotContains(t, d, "dc-device-management", "event-management's model must not import device-management")
	}
}
