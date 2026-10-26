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

// 🔴 A TOKEN THE API DIGESTS MUST VERIFY THROUGH THE CHECKER main.go BUILDS.
//
// An integration's token is digested under api.EnrollmentKeys.Integration and compared by
// the Checker under its own copy of that key. They are two references to one key, joined
// only by buildApis, and every model test builds its Checker by hand. So this one goes
// through buildApis, creates the integration through the service's own Api, and
// authenticates through the same Api. A Checker over another purpose's key refuses the very
// token that authenticates above, so a buildApis that wired the wrong key (or none) fails
// here instead of refusing every sender in the instance in production.
func TestTheServicesIntegrationCheckerVerifiesWhatItsApiDigests(t *testing.T) {
	host, port := startEmbeddedNats(t)
	instance := fmt.Sprintf("intwiring%d", time.Now().UnixNano())
	db := putest.NewSQLiteDB(t, append(credentialTables(), &model.Integration{}, &model.IntegrationDeviceType{})...)
	nmgr := startNatsManager(t, host, port, instance)
	keys := dmtest.EnrollmentKeys()
	api, _, err := buildApis(nmgr, &rdb.RdbManager{Database: db}, dmtest.DeviceSecretKey(), keys, config.NewDeviceManagementConfiguration())
	require.NoError(t, err)
	require.NotNil(t, api.IntegrationChecker, "buildApis did not wire the integration Checker")

	ctx := core.WithTenant(context.Background(), "acme")
	_, err = api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	created, err := api.CreateIntegration(ctx, &model.IntegrationCreateRequest{
		Token: "chirp-1", Type: string(model.IntegrationChirpStackV4), Enabled: true, DeviceTypeTokens: []string{"dt"}})
	require.NoError(t, err)

	authenticate := func() error {
		_, err := api.AuthenticateIntegration(ctx, "chirp-1", created.IssuedToken)
		return err
	}
	require.NoError(t, authenticate(), "the paired positive: the service's own Checker verifies what its Api digested")

	good := api.IntegrationChecker
	t.Cleanup(func() { api.IntegrationChecker = good })
	build := func(t *testing.T, integration *credential.DeviceSecretKey) *credential.Checker {
		t.Helper()
		c, err := processor.NewIntegrationChecker(&credential.EnrollmentSecretKeys{
			Provisioning: keys.Provisioning, Claim: keys.Claim, Integration: integration})
		require.NoError(t, err)
		return c
	}
	for name, wrong := range map[string]*credential.DeviceSecretKey{
		"the provisioning key": keys.Provisioning,
		"the claim key":        keys.Claim,
		"the device key":       dmtest.DeviceSecretKey(),
	} {
		api.IntegrationChecker = build(t, wrong)
		require.ErrorIs(t, authenticate(), model.ErrIntegrationAuthFailed, "a Checker over %s verified an integration token", name)
	}
	api.IntegrationChecker = build(t, keys.Integration)
	require.NoError(t, authenticate(), "the control: the right key, rebuilt the way buildApis builds it")
}
