// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstanceEndpointPortBounds(t *testing.T) {
	for _, endpoint := range []struct {
		name     string
		field    string
		required bool
	}{
		{"nats", "port", true},
		{"userManagement", "port", true},
		{"userManagement", "internalPort", false},
		{"deviceManagement", "port", false},
		{"eventProcessing", "port", false},
		{"commandDelivery", "port", false},
		{"deviceState", "port", false},
		{"aiInference", "port", false},
	} {
		for _, port := range []uint32{65536, 65535, 0} {
			t.Run(fmt.Sprintf("%s.%s/%d", endpoint.name, endpoint.field, port), func(t *testing.T) {
				// Override one field in an otherwise valid instance document.
				infra := map[string]map[string]any{
					"nats":           {"hostname": "nats", "port": 4222},
					"userManagement": {"hostname": "user", "port": 8080},
				}
				if infra[endpoint.name] == nil {
					infra[endpoint.name] = map[string]any{"hostname": "ai"}
				}
				infra[endpoint.name][endpoint.field] = port
				doc, err := json.Marshal(map[string]any{"infrastructure": infra})
				require.NoError(t, err)
				cfg, err := loadShutdown(t, string(doc))
				if port > 65535 {
					require.Error(t, err)
					assert.Contains(t, err.Error(), "infrastructure."+endpoint.name+"."+endpoint.field)
				} else if port == 0 && endpoint.required {
					require.Error(t, err)
					assert.Contains(t, err.Error(), "hostname and port are required")
				} else {
					require.NoError(t, err)
					if endpoint.field == "internalPort" && port == 0 {
						assert.EqualValues(t, DefaultUserManagementInternalPort, cfg.Infrastructure.UserManagement.InternalPort)
					}
				}
			})
		}
	}
}

func TestTerminationGracePeriodValuesAtLoad(t *testing.T) {
	for _, tc := range []struct {
		grace int
		want  int
	}{
		{-1, -1},
		{0, 30},
		{45, 45},
	} {
		t.Run(fmt.Sprint(tc.grace), func(t *testing.T) {
			cfg, err := loadShutdown(t, withShutdown(fmt.Sprintf(
				`{"drainSeconds":5,"terminationGracePeriodSeconds":%d}`, tc.grace)))
			if tc.grace < 0 {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "infrastructure.shutdown.terminationGracePeriodSeconds")
				assert.Equal(t, tc.grace, cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds)
			}
		})
	}
}

func TestNegativeGraceIsRefusedBeforeDrainBudgetValidation(t *testing.T) {
	for _, drain := range []*int{nil, new(int)} {
		t.Run(fmt.Sprint(drain == nil), func(t *testing.T) {
			err := (ShutdownConfiguration{DrainSeconds: drain, TerminationGracePeriodSeconds: -1}).validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "infrastructure.shutdown.terminationGracePeriodSeconds")
		})
	}
}
