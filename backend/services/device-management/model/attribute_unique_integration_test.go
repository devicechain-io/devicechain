// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// A second connection must wait on an uncommitted first insert, then update it.
// A different key is the counterweight: it must commit while the first is open.
func TestAttributeConcurrentFirstWritePostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("attribute-race-%d", time.Now().UnixNano()))
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"})
	require.NoError(t, err)
	device, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	tx := api.RDB.DB(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	north := "north"
	first := &EntityAttribute{EntityType: "device", EntityId: device.ID, Scope: "SERVER", AttrKey: "site", ValueType: "STRING", Value: rdb.NullStrOf(&north)}
	require.NoError(t, tx.Create(first).Error)
	set := func(key string) (*EntityAttribute, error) {
		south := "south"
		return api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "d1", Scope: "SERVER", AttrKey: key, ValueType: "STRING", Value: &south})
	}
	_, err = set("other")
	require.NoError(t, err)
	type result struct {
		row *EntityAttribute
		err error
	}
	done := make(chan result, 1)
	go func() { row, err := set("site"); done <- result{row, err} }()
	waitForBlockedBackend(t, api.RDB.Database)
	select {
	case got := <-done:
		t.Fatalf("same-key write did not block: %v", got.err)
	default:
	}
	require.NoError(t, tx.Commit().Error)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, first.ID, got.row.ID)
		require.Equal(t, "south", got.row.Value.String)
	case <-time.After(30 * time.Second):
		t.Fatal("upsert did not finish after commit")
	}
	var rows []EntityAttribute
	require.NoError(t, api.RDB.DB(ctx).Where("attr_key = ?", "site").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "south", rows[0].Value.String)
}

func TestRestoreAttributeConcurrentFirstWritePostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("attribute-restore-race-%d", time.Now().UnixNano()))
	device := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "site", ValueType: "STRING"}})
	tx := api.RDB.DB(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	markAsProtocolWriter(t, tx)
	north := "north"
	first := &EntityAttribute{EntityType: "device", EntityId: device.ID, Scope: "SHARED", AttrKey: "site",
		ValueType: "STRING", Value: rdb.NullStrOf(&north)}
	require.NoError(t, tx.Create(first).Error)
	done := make(chan error, 1)
	go func() {
		done <- api.applyConfigurationDocument(ctx, device.Token, device.ID, `{"site":"south"}`, "restore")
	}()
	waitForBlockedBackend(t, api.RDB.Database)
	select {
	case err := <-done:
		t.Fatalf("restore did not wait for the first insert: %v", err)
	default:
	}
	require.NoError(t, tx.Commit().Error)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("restore did not finish after commit")
	}
	var rows []EntityAttribute
	require.NoError(t, api.RDB.DB(ctx).Where("attr_key = ?", "site").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, first.ID, rows[0].ID)
	require.Equal(t, "south", rows[0].Value.String)
}
