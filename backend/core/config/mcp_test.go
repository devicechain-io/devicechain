// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMcpCommandProposalsStrictDecodingAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  bool
		valid bool
	}{
		{`{}`, false, true},
		{`{"infrastructure":{"mcp":{"commandProposals":false}}}`, false, true},
		{`{"infrastructure":{"mcp":{"commandProposals":true}}}`, true, true},
		{`{"infrastructure":{"mcp":{"commandProposal":true}}}`, false, false},
		{`{"infrastructure":{"mcp":{"commandProposals":"true"}}}`, false, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var cfg InstanceConfiguration
			d := json.NewDecoder(strings.NewReader(tc.raw))
			d.DisallowUnknownFields()
			err := d.Decode(&cfg)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			cfg.ApplyDefaults()
			raw, err := json.Marshal(cfg.Infrastructure)
			require.NoError(t, err)
			var out struct {
				Mcp struct{ CommandProposals bool }
			}
			require.NoError(t, json.Unmarshal(raw, &out))
			require.Equal(t, tc.want, out.Mcp.CommandProposals)
		})
	}
}
