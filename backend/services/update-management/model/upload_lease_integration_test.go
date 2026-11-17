// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
)

func TestUploadLeaseOwnershipOnPostgres(t *testing.T) {
	testUploadLeaseOwnership(t, func(t *testing.T) *Api {
		ctx := context.Background()
		host, port := itServer(t)
		instance := fmt.Sprintf("uploadleaseit%d", time.Now().UnixNano())
		require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
		mgr := &rdb.RdbManager{
			Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "update-management"},
			Migrations:   schema.Migrations,
			InstanceConfig: config.DatastoreConfiguration{
				Type: "postgres",
				Configuration: map[string]interface{}{
					"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
				},
			},
		}
		require.NoError(t, mgr.ExecuteInitialize(ctx))
		t.Cleanup(func() {
			db, err := mgr.Database.DB()
			require.NoError(t, err)
			require.NoError(t, db.Close())
		})
		api := NewApi(mgr, nil, Limits{MaxArtifactBytes: 1000, ConfiguredMaxArtifactBytes: 1000, MaxTenantArtifactBytes: 1000})
		api.Profiles = &fakeProfiles{known: map[string][]string{"acme": {"gateway"}}}
		return api
	})
}
