// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func fptr(f float64) *float64 { return &f }
func iptr(i int) *int         { return &i }

// A nil override (inherit the platform default) and any positive override are
// accepted; a zero or negative override is rejected — the only way to inherit is
// to omit, never to set zero.
func TestValidateGovernance(t *testing.T) {
	cases := []struct {
		name        string
		ingestMps   *float64
		ingestBurst *int
		outMps      *float64
		outBurst    *int
		wantErr     bool
	}{
		{"all nil inherits", nil, nil, nil, nil, false},
		{"positive ingest rate + burst", fptr(500), iptr(1000), nil, nil, false},
		{"fractional ingest rate ok", fptr(0.5), nil, nil, nil, false},
		{"zero ingest rate rejected", fptr(0), nil, nil, nil, true},
		{"negative ingest rate rejected", fptr(-1), nil, nil, nil, true},
		{"zero ingest burst rejected", nil, iptr(0), nil, nil, true},
		{"negative ingest burst rejected", nil, iptr(-5), nil, nil, true},
		{"good ingest rate but bad ingest burst rejected", fptr(100), iptr(0), nil, nil, true},
		{"positive outbound rate + burst", nil, nil, fptr(50), iptr(100), false},
		{"zero outbound rate rejected", nil, nil, fptr(0), nil, true},
		{"negative outbound rate rejected", nil, nil, fptr(-2), nil, true},
		{"zero outbound burst rejected", nil, nil, nil, iptr(0), true},
		{"negative outbound burst rejected", nil, nil, nil, iptr(-3), true},
		{"valid ingest but bad outbound rejected", fptr(500), iptr(1000), fptr(-1), nil, true},
		{"all four dimensions positive", fptr(500), iptr(1000), fptr(50), iptr(100), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGovernance(tc.ingestMps, tc.ingestBurst, tc.outMps, tc.outBurst)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
