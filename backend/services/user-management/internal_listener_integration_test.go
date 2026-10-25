// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	ucfg "github.com/devicechain-io/dc-user-management/config"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// The internal auth listener, started by the REAL lifecycle wiring of this service rather than by
// a call the test makes itself.
//
// 🔴 WHAT THIS CATCHES THAT THE UNIT TESTS CANNOT. TestInternalAuthListenerBindsTheConfiguredPort
// calls startInternalAuthListener directly, so deleting its one call site in
// afterMicroserviceStarted passes it. Here that call is the only thing that can start the
// listener: the test drives the callbacks lifecycleCallbacks() declares, over a real PostgreSQL
// (migrations, secret store, signing key) and an embedded NATS server, and then asks the sockets.
//
// It drives the Initializer and Starter callbacks rather than Microservice.InitializeAndStart,
// because that path reads /etc/dci-config and /etc/dct-config, fixed mount points a test cannot
// redirect without a change to core.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 -run InternalAuthListener .
func TestInternalAuthListenerIsStartedByTheRealLifecycleWiring(t *testing.T) {
	pgPort := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err)
		pgPort = p
	}
	pgHost := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		pgHost = h
	}
	instance := fmt.Sprintf("umauthport%d", time.Now().UnixNano())
	ctx := context.Background()
	require.NoError(t, rdbtest.EnsureDatabase(ctx, pgHost, pgPort, "postgres", "postgres", instance, ""))
	// The broker administrator's streams and buckets, which services only verify, and the identity
	// account the sign-in buckets live in: the same broker the catalog boot test starts.
	broker := permtest.Start(t, instance)
	t.Setenv(envIdentityPassword, permtest.Password(natsauth.IdentityUser))

	prevMs, prevCfg, prevInternal, prevSrv := Microservice, Configuration, InternalAuthMux, InternalAuthServer
	prevNats, prevRdb, prevGql, prevIdent := NatsManager, RdbManager, GraphQLManager, IdentityManager
	t.Cleanup(func() {
		Microservice, Configuration, InternalAuthMux, InternalAuthServer = prevMs, prevCfg, prevInternal, prevSrv
		NatsManager, RdbManager, GraphQLManager, IdentityManager = prevNats, prevRdb, prevGql, prevIdent
	})
	InternalAuthMux, InternalAuthServer = nil, nil

	const secret = "it-shared-service-secret"
	t.Setenv(ucfg.SuperuserPasswordEnv, "it-superuser-password-1234")
	internalPort, publicPort := freePort(t), freePort(t)

	Microservice = core.NewMicroservice(lifecycleCallbacks())
	Microservice.InstanceId = instance
	Microservice.Deployed = true
	Microservice.FunctionalArea = "user-management"
	Microservice.Readiness = core.NewReadinessGate()
	Microservice.MicroserviceConfigurationRaw = []byte(`{"auth":{"superuserEmail":"su@example.com"}}`)

	cfg := &Microservice.InstanceConfiguration
	cfg.Persistence.Rdb = config.DatastoreConfiguration{Type: "postgres", Configuration: map[string]interface{}{
		"hostname": pgHost, "port": pgPort, "username": "postgres", "password": "postgres", "sslMode": "disable",
	}}
	cfg.Infrastructure.Nats = broker.NatsConfiguration("user-management")
	cfg.Infrastructure.Nats.Auth.IdentityAccount = true
	cfg.Infrastructure.Secrets.RootKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	cfg.Infrastructure.ServiceAuth.Secret = secret
	cfg.Infrastructure.UserManagement = config.UserManagementConfiguration{
		Hostname: "127.0.0.1", Port: uint32(publicPort), InternalPort: uint32(internalPort),
	}
	cfg.ApplyDefaults()

	// Production's own callbacks, in production's order.
	cb := lifecycleCallbacks()
	require.NoError(t, cb.Initializer.Postprocess(ctx), "initialize")
	// The public listener binds the chart's fixed port in production; a test needs its own.
	GraphQLManager.Port = int32(publicPort)
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cb.Stopper.Preprocess(ctx)
	}
	t.Cleanup(func() {
		stop()
		_ = cb.Terminator.Preprocess(ctx)
	})
	require.NoError(t, cb.Starter.Postprocess(ctx), "start")

	internal := fmt.Sprintf("http://127.0.0.1:%d", internalPort)
	public := fmt.Sprintf("http://127.0.0.1:%d", publicPort)
	client := &http.Client{Timeout: 5 * time.Second}

	// The internal JWKS answers, and a service token minted with the right secret validates
	// against it.
	resp, err := client.Get(internal + auth.JWKSPath)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "internal %s", auth.JWKSPath)

	mint := func(base, sec string) *http.Response {
		body, _ := json.Marshal(auth.ServiceTokenRequest{Subject: "it", Authorities: []string{string(auth.TenantRead)}})
		req, err := http.NewRequest(http.MethodPost, base+auth.ServiceTokenPath, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.ServiceSecretHeader, sec)
		r, err := client.Do(req)
		require.NoError(t, err)
		return r
	}
	r := mint(internal, secret)
	require.Equal(t, http.StatusOK, r.StatusCode, "internal mint with the right secret")
	var minted auth.ServiceTokenResponse
	require.NoError(t, json.NewDecoder(r.Body).Decode(&minted))
	r.Body.Close()
	require.NotEmpty(t, minted.Token)

	v, err := auth.NewValidatorFromJWKSURL(ctx, internal+auth.JWKSPath, 1, 0)
	require.NoError(t, err)
	_, err = v.ValidateService(minted.Token)
	require.NoError(t, err, "the minted token must validate against the internal JWKS")

	r = mint(internal, "wrong")
	r.Body.Close()
	require.Equal(t, http.StatusUnauthorized, r.StatusCode, "internal mint with the wrong secret")

	// The public port serves neither.
	for _, p := range []string{auth.JWKSPath, auth.ServiceTokenPath} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, _ := http.NewRequest(method, public+p, bytes.NewReader([]byte(`{}`)))
			req.Header.Set(auth.ServiceSecretHeader, secret)
			pr, err := client.Do(req)
			require.NoError(t, err)
			pr.Body.Close()
			require.Equal(t, http.StatusNotFound, pr.StatusCode, "public %s %s", method, p)
		}
	}
	// The public listener is really up (a 404 from nothing listening would be a refusal).
	hr, err := client.Get(public + "/healthz")
	require.NoError(t, err)
	hr.Body.Close()

	// After Stop the internal port refuses connections.
	stop()
	_, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", internalPort), time.Second)
	require.Error(t, err, "the internal port still accepts connections after Stop")
}
