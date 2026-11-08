// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseParams(t *testing.T) {
	hex242 := strings.Repeat("ab", 242)
	hex243 := strings.Repeat("ab", 243)
	cases := []struct {
		name    string
		in      string
		want    Params
		badName string // the parameter the refusal must name; "" means accepted
	}{
		{"hex", `{"fPort":10,"hex":"0a0B"}`, Params{FPort: 10, Data: []byte{0x0a, 0x0b}}, ""},
		{"base64", `{"fPort":1,"base64":"CgsM"}`, Params{FPort: 1, Data: []byte{10, 11, 12}}, ""},
		{"confirmed parses (the adapter refuses it)", `{"fPort":2,"hex":"01","confirmed":true}`,
			Params{FPort: 2, Data: []byte{1}, Confirmed: true}, ""},
		{"max 242 bytes", `{"fPort":223,"hex":"` + hex242 + `"}`, Params{FPort: 223, Data: []byte(strings.Repeat("\xab", 242))}, ""},
		{"243 bytes", `{"fPort":1,"hex":"` + hex243 + `"}`, Params{}, "hex"},
		{"fPort 0", `{"fPort":0,"hex":"01"}`, Params{}, "fPort"},
		{"fPort 224", `{"fPort":224,"hex":"01"}`, Params{}, "fPort"},
		{"fPort fractional", `{"fPort":1.5,"hex":"01"}`, Params{}, "fPort"},
		{"fPort string", `{"fPort":"1","hex":"01"}`, Params{}, "fPort"},
		{"fPort missing", `{"hex":"01"}`, Params{}, "fPort"},
		{"both encodings", `{"fPort":1,"hex":"01","base64":"AQ=="}`, Params{}, "hex"},
		{"neither encoding", `{"fPort":1}`, Params{}, "hex"},
		{"odd hex", `{"fPort":1,"hex":"abc"}`, Params{}, "hex"},
		{"non hex", `{"fPort":1,"hex":"zz"}`, Params{}, "hex"},
		{"url-safe base64", `{"fPort":1,"base64":"-_8="}`, Params{}, "base64"},
		{"unpadded base64", `{"fPort":1,"base64":"AQ"}`, Params{}, "base64"},
		{"empty payload", `{"fPort":1,"hex":""}`, Params{}, "payload"},
		{"unknown key", `{"fPort":1,"hex":"01","priority":3}`, Params{}, "priority"},
		{"confirmed not bool", `{"fPort":1,"hex":"01","confirmed":"yes"}`, Params{}, "confirmed"},
		{"not an object", `[1]`, Params{}, "(payload)"},
		{"null", `null`, Params{}, "fPort"},
		{"empty", ``, Params{}, "fPort"},
		{"trailing data", `{"fPort":1,"hex":"01"} {}`, Params{}, "(payload)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseParams([]byte(c.in))
			if c.badName == "" {
				require.NoError(t, err)
				require.Equal(t, c.want, got)
				return
			}
			var pe *ParamError
			require.True(t, errors.As(err, &pe), "want a ParamError, got %v", err)
			require.Equal(t, c.badName, pe.Param)
		})
	}
}

// A refusal must never echo payload bytes.
func TestParamErrorCarriesNoPayload(t *testing.T) {
	_, err := ParseParams([]byte(`{"fPort":1,"hex":"SECRETSECRET"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRETSECRET")
}
