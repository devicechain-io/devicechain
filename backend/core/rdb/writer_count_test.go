// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// The writer bound is the pool the service will actually open: the configured size, or
// the default when none is configured — never the idle count, and never a restated
// default.
func TestCheckWriterCountBoundsWritersBelowTheEffectivePool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		writers int
		cfg     config.MicroserviceDatastoreConfiguration
		wantErr string
	}{
		{"none", 0, config.MicroserviceDatastoreConfiguration{}, "w must be at least 1, got 0"},
		{"one below the default pool", 19, config.MicroserviceDatastoreConfiguration{}, ""},
		{"the default pool", 20, config.MicroserviceDatastoreConfiguration{},
			"w is 20, but the connection pool holds 20; keep it below 20 so reads are not starved"},
		{"below a configured pool", 29, config.MicroserviceDatastoreConfiguration{MaxOpenConnections: 30}, ""},
		{"a configured pool", 30, config.MicroserviceDatastoreConfiguration{MaxOpenConnections: 30},
			"w is 30, but the connection pool holds 30"},
		{"idle is not the pool", 10, config.MicroserviceDatastoreConfiguration{MaxOpenConnections: 12, MaxIdleConnections: 4}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckWriterCount("w", tc.writers, tc.cfg)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("CheckWriterCount(%d) = %v; want nil", tc.writers, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("CheckWriterCount(%d) = %v; want an error containing %q", tc.writers, err, tc.wantErr)
			}
		})
	}
	if got := EffectiveMaxOpenConnections(config.MicroserviceDatastoreConfiguration{}); got != defaultMaxOpenConnections {
		t.Errorf("EffectiveMaxOpenConnections of an unset pool = %d; want the default %d", got, defaultMaxOpenConnections)
	}
}

// More than half the pool is allowed and logged; half or fewer is not logged. The boundary
// is exact: on the default pool of 20, 10 writers are quiet and 11 are logged.
func TestCheckWriterCountLogsWritersOverHalfThePool(t *testing.T) {
	for _, tc := range []struct {
		writers int
		logged  bool
	}{{1, false}, {10, false}, {11, true}, {19, true}} {
		t.Run(fmt.Sprint(tc.writers), func(t *testing.T) {
			logs := logSink.Capture(t)
			if err := CheckWriterCount("w", tc.writers, config.MicroserviceDatastoreConfiguration{}); err != nil {
				t.Fatalf("CheckWriterCount(%d) = %v; want nil", tc.writers, err)
			}
			got := strings.Contains(logs.String(), "More than half the connection pool")
			if got != tc.logged {
				t.Errorf("%d writers on a pool of 20: logged = %v; want %v (log: %q)", tc.writers, got, tc.logged, logs.String())
			}
		})
	}
}

// The batch bound is exact at both edges of both settings, and names the setting under the
// prefix the caller passes, so each service's error reads as its own document.
func TestCheckWriterBatchBoundsBatchAndLinger(t *testing.T) {
	for _, tc := range []struct {
		maxBatch, linger int
		wantErr          string
	}{
		{0, 0, "p.maxBatch must be between 1 and 64, got 0"},
		{1, 0, ""},
		{64, 0, ""},
		{65, 0, "p.maxBatch must be between 1 and 64, got 65"},
		{32, -1, "p.lingerMillis must be between 0 and 1000, got -1"},
		{32, 0, ""},
		{32, 1000, ""},
		{32, 1001, "p.lingerMillis must be between 0 and 1000, got 1001"},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.maxBatch, tc.linger), func(t *testing.T) {
			err := CheckWriterBatch("p", tc.maxBatch, tc.linger)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("CheckWriterBatch(%d, %d) = %v; want nil", tc.maxBatch, tc.linger, err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("CheckWriterBatch(%d, %d) = %v; want %q", tc.maxBatch, tc.linger, err, tc.wantErr)
			}
		})
	}
}
