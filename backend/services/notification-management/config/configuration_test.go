// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/config"
	"github.com/stretchr/testify/require"
)

// Overflow must be refused while safe bounds, defaults, and sweep disabling still load.
func TestASecondsSettingThatOverflowsADurationIsRefused(t *testing.T) {
	for _, tc := range []struct {
		key      string
		duration func(*config.NotificationManagementConfiguration) time.Duration
		disable  bool
	}{
		{"deliverySeconds", (*config.NotificationManagementConfiguration).DeliveryTimeout, false},
		{"stateRetentionSeconds", (*config.NotificationManagementConfiguration).StateRetention, false},
		{"attemptRetentionSeconds", (*config.NotificationManagementConfiguration).AttemptRetention, false},
		{"retentionSweepSeconds", (*config.NotificationManagementConfiguration).RetentionSweepInterval, true},
		{"escalationSweepSeconds", (*config.NotificationManagementConfiguration).EscalationSweepInterval, true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			cfg := &config.NotificationManagementConfiguration{}
			err := core.LoadConfiguration([]byte(fmt.Sprintf(`{"%s":9223372037}`, tc.key)), cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.key)

			cfg = &config.NotificationManagementConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(fmt.Sprintf(`{"%s":0}`, tc.key)), cfg))
			require.Equal(t, tc.duration(config.NewNotificationManagementConfiguration()), tc.duration(cfg))
			require.Positive(t, tc.duration(cfg))

			cfg = &config.NotificationManagementConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(fmt.Sprintf(`{"%s":9223372036}`, tc.key)), cfg))
			require.Equal(t, time.Duration(9223372036)*time.Second, tc.duration(cfg))
			require.Positive(t, tc.duration(cfg))

			if tc.disable {
				cfg = &config.NotificationManagementConfiguration{}
				require.NoError(t, core.LoadConfiguration([]byte(fmt.Sprintf(`{"%s":-1}`, tc.key)), cfg))
				require.Equal(t, -time.Second, tc.duration(cfg))
			}
		})
	}
}
