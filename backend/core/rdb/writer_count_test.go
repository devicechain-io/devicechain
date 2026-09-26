// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
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
