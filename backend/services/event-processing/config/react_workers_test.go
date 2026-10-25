// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// Absent, REACT runs DefaultReactWorkers wide, through the real load path, and the default is the
// value the documentation states.
func TestReactWorkersDefault(t *testing.T) {
	for _, doc := range []string{``, `{}`, `{"react":{}}`} {
		cfg := &EventProcessingConfiguration{}
		if err := core.LoadConfiguration([]byte(doc), cfg); err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got := cfg.React.WorkerCount(); got != DefaultReactWorkers {
			t.Fatalf("%q: workers %d, want the default %d", doc, got, DefaultReactWorkers)
		}
	}
	if DefaultReactWorkers != 8 {
		t.Fatalf("the default moved to %d; the documentation states 8", DefaultReactWorkers)
	}
}

// Absent beside a small pool, the default shrinks to half the pool rather than refusing to start
// a deployment that sized its pool before the setting existed.
func TestReactWorkersDefaultFitsASmallPool(t *testing.T) {
	for _, tc := range []struct {
		pool, want int
	}{{20, 8}, {16, 8}, {10, 5}, {6, 3}, {2, 1}, {1, 1}} {
		cfg := &EventProcessingConfiguration{}
		doc := `{"rdbConfiguration":{"maxOpenConnections":` + itoa(tc.pool) + `}}`
		if err := core.LoadConfiguration([]byte(doc), cfg); err != nil {
			t.Fatalf("pool %d: %v", tc.pool, err)
		}
		if got := cfg.React.WorkerCount(); got != tc.want {
			t.Fatalf("pool %d: default workers %d, want %d", tc.pool, got, tc.want)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// A count inside its bounds is applied, 1 (the old serial consumer) included.
func TestReactWorkersApplied(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want int
	}{
		{`{"react":{"workers":1}}`, 1},
		{`{"rdbConfiguration":{"maxOpenConnections":1},"react":{"workers":1}}`, 1},
		{`{"react":{"workers":12}}`, 12},
		{`{"rdbConfiguration":{"maxOpenConnections":80},"react":{"workers":64}}`, 64},
	} {
		cfg := &EventProcessingConfiguration{}
		if err := core.LoadConfiguration([]byte(tc.doc), cfg); err != nil {
			t.Fatalf("%s: %v", tc.doc, err)
		}
		if got := cfg.React.WorkerCount(); got != tc.want {
			t.Fatalf("%s: workers %d, want %d", tc.doc, got, tc.want)
		}
	}
}

// Out of range, mistyped, or more workers than the connection pool can carry: refused at load,
// naming the key. Zero is refused rather than read as unset; an absent key asks for the default.
// The pool bound is fail-closed for a pool nobody wrote too: the default pool of 20 refuses 20.
func TestReactWorkersRefusals(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{`{"react":{"workers":0}}`, "react.workers"},
		{`{"react":{"workers":-1}}`, "react.workers"},
		{`{"react":{"workers":65}}`, "react.workers"},
		{`{"react":{"workers":2.5}}`, "workers"},
		{`{"react":{"workers":"many"}}`, "workers"},
		{`{"react":{"lanes":8}}`, "lanes"},
		{`{"react":{"workers":20}}`, "connection pool holds 20"},
		{`{"rdbConfiguration":{"maxOpenConnections":6},"react":{"workers":6}}`, "connection pool holds 6"},
		{`{"rdbConfiguration":{"maxOpenConnections":2},"react":{"workers":2}}`, "connection pool holds 2"},
	} {
		err := core.LoadConfiguration([]byte(tc.doc), &EventProcessingConfiguration{})
		if err == nil {
			t.Errorf("%s: accepted", tc.doc)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.doc, err, tc.want)
		}
	}
}
