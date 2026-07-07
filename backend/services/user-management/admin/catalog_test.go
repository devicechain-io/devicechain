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
		name    string
		mps     *float64
		burst   *int
		wantErr bool
	}{
		{"both nil inherits", nil, nil, false},
		{"positive rate + burst", fptr(500), iptr(1000), false},
		{"fractional rate ok", fptr(0.5), nil, false},
		{"zero rate rejected", fptr(0), nil, true},
		{"negative rate rejected", fptr(-1), nil, true},
		{"zero burst rejected", nil, iptr(0), true},
		{"negative burst rejected", nil, iptr(-5), true},
		{"good rate but bad burst rejected", fptr(100), iptr(0), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGovernance(tc.mps, tc.burst)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
