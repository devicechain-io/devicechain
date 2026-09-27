// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolution.workers loads as written, and a document that omits it gets 10.
func TestResolutionWorkersLoadAndDefault(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"resolution":{"workers":7}}`), cfg))
	assert.Equal(t, 7, cfg.Resolution.Workers)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, 10, cfg.Resolution.Workers)
	assert.Equal(t, 10, NewDeviceManagementConfiguration().Resolution.Workers)
	assert.Equal(t, 10, DefaultResolutionWorkers)
}

// resolution.workers is bounded by the relational pool the resolvers draw from — 20 unless
// rdbConfiguration sets one — and an out-of-range value stops the service, naming the
// setting. The edges are exact: the pool size is refused, one below it is accepted.
func TestResolutionWorkersAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc, wantErr string
	}{
		{"negative", `{"resolution":{"workers":-1}}`, "resolution.workers must be at least 1, got -1"},
		{"at the default pool", `{"resolution":{"workers":20}}`,
			"resolution.workers is 20, but the connection pool holds 20; keep it below 20 so reads are not starved"},
		{"below the default pool", `{"resolution":{"workers":19}}`, ""},
		{"at a configured pool", `{"rdbConfiguration":{"maxOpenConnections":8},"resolution":{"workers":8}}`,
			"resolution.workers is 8, but the connection pool holds 8"},
		{"below a configured pool", `{"rdbConfiguration":{"maxOpenConnections":8},"resolution":{"workers":7}}`, ""},
		// The default is refused on a pool of 10 or fewer, which is what the upgrade note tells
		// an operator who set one.
		{"the default on a pool of 10", `{"rdbConfiguration":{"maxOpenConnections":10}}`,
			"resolution.workers is 10, but the connection pool holds 10"},
		{"the default on a pool of 11", `{"rdbConfiguration":{"maxOpenConnections":11}}`, ""},
		{"a misspelled key", `{"resolution":{"resolvers":8}}`, `unknown field "resolvers"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

// The default is quiet on the default pool: 10 of 20 is not "more than half", so an
// operator who changed nothing sees no warning. A default above 10 would log here. The
// counterweight shows the same capture does see the warning, so an empty capture cannot pass
// for a quiet one.
func TestTheDefaultResolutionWorkersDoNotWarnOnTheDefaultPool(t *testing.T) {
	const warning = "More than half the connection pool"

	for _, tc := range []struct {
		doc    string
		logged bool
	}{{``, false}, {`{"resolution":{"workers":11}}`, true}} {
		t.Run(tc.doc, func(t *testing.T) {
			logs := logSink.Capture(t)
			require.NoError(t, core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{}))
			if got := strings.Contains(logs.String(), warning); got != tc.logged {
				t.Errorf("document %q: logged the warning = %v, want %v (log: %q)", tc.doc, got, tc.logged, logs.String())
			}
		})
	}
}
