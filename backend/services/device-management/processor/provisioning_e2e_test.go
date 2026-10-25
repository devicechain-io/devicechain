// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/provisioning"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/selfreg"
	"github.com/prometheus/client_golang/prometheus"
)

// recordingKicker records the sessions the Api asked to end.
type recordingKicker struct {
	mu      sync.Mutex
	devices []string
}

func (k *recordingKicker) KickDeviceSessions(_ context.Context, _, _ string, tokens []string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.devices = append(k.devices, tokens...)
	return nil
}

// 🔴 END TO END: A DEVICE REGISTERS ITSELF OVER HTTP AND THEN CONNECTS.
//
// The client is the one dcctl and the simulator use (core/selfreg), the server is the real
// handler over the real model, and the broker side is the real auth-callout responder's
// authorize over the same Api. What is asserted is the thing a device cares about: the
// username it built from what it was given gets a grant, with the client id it was told to use.
// Every step that must NOT work is paired with the control that does.
func TestARegisteredDeviceCanConnectAndIsCutOffWhenRevoked(t *testing.T) {
	tables := []any{&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{}, &model.DeviceProfileVersion{},
		&model.DeviceProfileActivation{}, &model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{},
		&model.DetectionRuleScopeRef{}, &model.ProvisioningProfile{}, &model.DeviceCredential{}}
	api := model.NewApi(&rdb.RdbManager{Database: putest.NewSQLiteDB(t, tables...)})
	api.DeviceSecretKey = dmtest.DeviceSecretKey()
	api.EnrollmentKeys = dmtest.EnrollmentKeys()
	var err error
	api.ProvisioningChecker, err = NewProvisioningChecker(dmtest.DeviceSecretKey(), dmtest.EnrollmentKeys())
	require.NoError(t, err)
	kicker := &recordingKicker{}
	api.SessionKicker = kicker

	ctx := putest.TenantContext("acme")()
	_, err = api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)
	created, err := api.CreateProvisioningProfile(ctx, &model.ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "fleet-key", Strategy: string(model.ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true})
	require.NoError(t, err)

	// The endpoint, as the service mounts it, with the instance id the responder below expects.
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	h, err := provisioning.New(provisioning.Config{ReadDeadline: 10 * time.Second, MaxInFlight: 4, MaxConcurrentRegistrations: 2, SourceBurst: 100, SourceEvery: time.Millisecond},
		provisioning.Deps{Registrar: api, InstanceId: "inst-1", Tenants: core.NewTenantRateLimiter(core.StaticCeiling(1000, 1000)),
			Metrics: provisioning.NewMetrics(ms)})
	require.NoError(t, err)
	mux := http.NewServeMux()
	provisioning.Register(mux, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The broker side: the real responder's authorize over the same Api.
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)
	responder := mustResponder(t, nil, api, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)
	connect := func(username, clientID string) (string, string) {
		t.Helper()
		return responder.authorize(testRequestFromClient(t, username, "", clientID))
	}

	register := func(file string) *selfreg.Result {
		t.Helper()
		res, err := selfreg.Register(context.Background(),
			selfreg.Request{Tenant: "acme", Key: "fleet-key", Secret: created.IssuedSecret, ExternalId: "SN-000123"},
			selfreg.Options{BaseURL: srv.URL, MaterialFile: file, MaxAttempts: 1, AllowPlaintext: true})
		require.NoError(t, err)
		return res
	}
	file := filepath.Join(t.TempDir(), "SN-000123.credential")

	// Register, then connect with exactly what the device was given.
	res := register(file)
	require.Equal(t, "CREATED", res.Outcome)
	username := "acme:" + res.Material
	userJWT, denied := connect(username, res.ClientId)
	require.Empty(t, denied, "a freshly registered device was refused by the broker")
	uc, err := jwt.DecodeUserClaims(userJWT)
	require.NoError(t, err)
	assert.Contains(t, []string(uc.Permissions.Pub.Allow), "inst-1.acme."+"devices."+res.DeviceToken+".events",
		"the grant is for the device that registered")

	// Controls: the same device with another credential, and with another client id, is refused.
	_, denied = connect("acme:"+res.Material+"x", res.ClientId)
	assert.NotEmpty(t, denied, "a wrong credential connected")
	_, denied = connect(username, "inst-1:acme:someone-else")
	assert.NotEmpty(t, denied, "a wrong client id connected")

	// The same run again is a replay and presents the same credential.
	again := register(file)
	assert.Equal(t, "REPLAYED", again.Outcome)
	assert.Equal(t, res.Material, again.Material)
	assert.False(t, again.MaterialCreated)
	assert.Equal(t, res.DeviceToken, again.DeviceToken)

	// An operator disabling the device cuts it off; enabling it restores the control.
	_, err = api.UpdateDevice(ctx, res.DeviceToken, &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	_, denied = connect(username, res.ClientId)
	assert.NotEmpty(t, denied, "a disabled device connected")
	_, err = api.UpdateDevice(ctx, res.DeviceToken, &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	_, denied = connect(username, res.ClientId)
	require.Empty(t, denied, "the control: re-enabled, the device connects again")

	// Revoking the profile's devices disables the credential and kicks the live session.
	kicker.mu.Lock()
	kicker.devices = nil
	kicker.mu.Unlock()
	n, err := api.RevokeProvisionedDevices(ctx, "pp-1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, denied = connect(username, res.ClientId)
	assert.NotEmpty(t, denied, "a revoked device connected")
	kicker.mu.Lock()
	assert.Equal(t, []string{res.DeviceToken}, kicker.devices, "the revoked device's session was not ended")
	kicker.mu.Unlock()

	// And a revoked device does not quietly register again with its old credential.
	_, err = selfreg.Register(context.Background(),
		selfreg.Request{Tenant: "acme", Key: "fleet-key", Secret: created.IssuedSecret, ExternalId: "SN-000123"},
		selfreg.Options{BaseURL: srv.URL, MaterialFile: file, MaxAttempts: 1, AllowPlaintext: true})
	var apiErr *selfreg.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.Status)
	assert.Equal(t, "ALREADY_PROVISIONED", apiErr.Code)
}
