// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔴 A DIGEST THE API WRITES MUST VERIFY THROUGH THE CHECKER main.go BUILDS.
//
// The profile's secret is digested under api.EnrollmentKeys.Provisioning and compared by the
// Checker under its own copy of that key. They are two references to one key, held in two
// places and joined by buildApis, and every other test builds its Checker by hand: a model test
// that passes the claim key to its own Checker proves nothing about what the service wires. So
// this one goes through buildApis, creates the profile through the service's own Api, and
// authenticates and registers through the same Api.
//
// The controls are the point. A Checker built with the claim key, or with the device key, where
// the provisioning key belongs refuses the very profile that authenticates above, so a buildApis
// that mixed the keys up (a transposed argument; the wrong field of EnrollmentKeys) fails here
// rather than refusing every device in the instance in production.
func TestTheServicesProvisioningCheckerVerifiesWhatItsApiDigests(t *testing.T) {
	host, port := startEmbeddedNats(t)
	instance := fmt.Sprintf("provwiring%d", time.Now().UnixNano())
	db := putest.NewSQLiteDB(t, credentialTables()...)
	nmgr := startNatsManager(t, host, port, instance)
	keys := dmtest.EnrollmentKeys()
	api, _, err := buildApis(nmgr, &rdb.RdbManager{Database: db}, dmtest.DeviceSecretKey(), keys, config.NewDeviceManagementConfiguration())
	require.NoError(t, err)
	require.NotNil(t, api.ProvisioningChecker, "buildApis did not wire the provisioning Checker")

	ctx := core.WithTenant(context.Background(), "acme")
	_, err = api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	created, err := api.CreateProvisioningProfile(ctx, &model.ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "fleet", Strategy: string(model.ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true})
	require.NoError(t, err)
	const material = "wiring-material-0123456789abcdef"

	register := func(t *testing.T) (*model.ProvisionResult, error) {
		t.Helper()
		p, err := api.AuthenticateProvisioningProfile(ctx, "fleet", created.IssuedSecret)
		if err != nil {
			return nil, err
		}
		return api.ProvisionDevice(ctx, &model.ProvisionRequest{Profile: p, ExternalId: "SN-1", CredentialSecret: material})
	}

	// The paired positive: authenticates, creates, and a replay is recognised.
	res, err := register(t)
	require.NoError(t, err)
	require.Equal(t, model.ProvisionCreated, res.Outcome)
	res, err = register(t)
	require.NoError(t, err)
	require.Equal(t, model.ProvisionReplayed, res.Outcome, "the replay compare did not verify what the Api stored")

	good := api.ProvisioningChecker
	t.Cleanup(func() { api.ProvisioningChecker = good })

	build := func(t *testing.T, device *credential.DeviceSecretKey, provisioning *credential.DeviceSecretKey) *credential.Checker {
		t.Helper()
		c, err := processor.NewProvisioningChecker(device, &credential.EnrollmentSecretKeys{Provisioning: provisioning, Claim: keys.Claim})
		require.NoError(t, err)
		return c
	}

	t.Run("the claim key where the provisioning key belongs", func(t *testing.T) {
		api.ProvisioningChecker = build(t, dmtest.DeviceSecretKey(), keys.Claim)
		_, err := register(t)
		require.ErrorIs(t, err, model.ErrProvisioningFailed)
	})
	t.Run("the device key where the provisioning key belongs", func(t *testing.T) {
		api.ProvisioningChecker = build(t, dmtest.DeviceSecretKey(), dmtest.DeviceSecretKey())
		_, err := register(t)
		require.ErrorIs(t, err, model.ErrProvisioningFailed)
	})
	t.Run("the provisioning key where the device key belongs", func(t *testing.T) {
		api.ProvisioningChecker = build(t, keys.Provisioning, keys.Provisioning)
		_, err := register(t)
		require.ErrorIs(t, err, model.ErrProvisioningAlreadyProvisioned, "a replay verified under the wrong key")
	})
	t.Run("the control: the right keys, rebuilt the way buildApis builds them", func(t *testing.T) {
		api.ProvisioningChecker = build(t, dmtest.DeviceSecretKey(), keys.Provisioning)
		res, err := register(t)
		require.NoError(t, err)
		require.Equal(t, model.ProvisionReplayed, res.Outcome)
	})
}

// Without both keys there is no Checker, and the Api answers "unavailable" instead of comparing
// anything itself.
func TestTheServiceWiresNoProvisioningCheckerWithoutBothKeys(t *testing.T) {
	host, port := startEmbeddedNats(t)
	instance := fmt.Sprintf("provwiring%d", time.Now().UnixNano())
	build := func(t *testing.T, device *credential.DeviceSecretKey, enrollment *credential.EnrollmentSecretKeys) *model.Api {
		t.Helper()
		db := putest.NewSQLiteDB(t, credentialTables()...)
		api, _, err := buildApis(startNatsManager(t, host, port, instance), &rdb.RdbManager{Database: db}, device, enrollment,
			config.NewDeviceManagementConfiguration())
		require.NoError(t, err)
		return api
	}
	ctx := core.WithTenant(context.Background(), "acme")

	for name, api := range map[string]*model.Api{
		"no device key":     build(t, nil, dmtest.EnrollmentKeys()),
		"no enrollment key": build(t, dmtest.DeviceSecretKey(), nil),
	} {
		require.Nil(t, api.ProvisioningChecker, name)
		_, err := api.AuthenticateProvisioningProfile(ctx, "fleet", "whatever")
		require.ErrorIs(t, err, model.ErrProvisioningUnavailable, name)
	}
}
