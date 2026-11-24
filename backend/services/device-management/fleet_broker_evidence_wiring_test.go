// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/stretchr/testify/require"
)

func TestFleetBrokerEvidenceUsesTheServicesInstanceConfig(t *testing.T) {
	host, port := startEmbeddedNats(t)
	nmgr := startNatsManager(t, host, port, fmt.Sprintf("fleetwiring%d", time.Now().UnixNano()))
	cfg := &nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	cfg.MqttFailClosed = true
	api, _, err := buildApis(nmgr, &rdb.RdbManager{Database: putest.NewSQLiteDB(t, credentialTables()...)},
		dmtest.DeviceSecretKey(), dmtest.EnrollmentKeys(), config.NewDeviceManagementConfiguration())
	require.NoError(t, err)
	require.Same(t, cfg, api.FleetNatsConfig, "START and RESUME must read the instance config, not a startup copy or broker probe")
	cfg.MqttFailClosed = false
	require.False(t, api.FleetNatsConfig.MqttFailClosed)
}
