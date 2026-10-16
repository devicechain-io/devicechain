// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// The startup path runs CheckMigrationIDs, and runs it before any database contact. The
// configuration points at no server at all: were the check skipped, or run after
// connecting, the error would be a connection failure rather than the chain's own.
func TestExecuteInitializeRefusesABadChainBeforeConnecting(t *testing.T) {
	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: "unused", FunctionalArea: "migration-ids"},
		Migrations:   []*gormigrate.Migration{{ID: "a"}, {ID: "a"}},
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": "127.0.0.1", "port": 1, "username": "u", "password": "p",
				"sslMode": "disable",
			},
		},
	}
	err := mgr.ExecuteInitialize(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `migration-ids migration chain: migrations[1] reuses the ID "a"`)
	assert.Nil(t, mgr.Database, "a refused chain never opens a connection")
}
