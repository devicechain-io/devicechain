// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/devicechain-io/dc-microservice/config"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/secrets"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
)

// 🔴 THESE TEST THE CALLER, NOT THE FUNCTION. The second factor's pieces are tested where they
// live; what only main.go decides is whether the Manager and the admin Service the service RUNS
// were handed them. Each test below builds its object through the function main calls
// (newIdentityManager, configureMfa, newAdminService, dataPlaneProviders), so a line dropped
// from one of them is a failing test and not a deployed gap.

type mfaWiring struct {
	db      *rdbHandle
	store   *iam.Store
	secrets secrets.SecretStore
}

type rdbHandle = rdb.RdbManager

func newMfaWiring(t *testing.T) *mfaWiring {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{}, &iam.MfaFactor{},
		&iam.MfaSourceFailure{}, &iam.MfaRecoveryCode{}, &rdb.AuditEvent{}, &iam.IdentityLink{}, &iam.Invitation{}, &iam.IdpConnection{}, &iam.SsoLinkRequest{})
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	raw := bytes.Repeat([]byte{7}, 32)
	store, err := secrets.NewFromConfig(context.Background(),
		config.SecretsConfiguration{RootKey: base64.StdEncoding.EncodeToString(raw)}, db)
	require.NoError(t, err)
	rdbm := &rdb.RdbManager{Database: db}
	return &mfaWiring{db: rdbm, store: iam.NewStore(rdbm), secrets: store}
}

// enrolled creates an identity with an active second factor, sealed in the wiring's store.
func (w *mfaWiring) enrolled(t *testing.T, email, password string) *iam.Identity {
	t.Helper()
	ctx := core.WithSystemContext(context.Background())
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	id := &iam.Identity{Email: email, Enabled: true, PasswordHash: string(hash)}
	require.NoError(t, w.store.CreateIdentity(ctx, id))
	_, err = w.store.BeginMfaEnrollment(ctx, w.secrets, id.ID, []byte("12345678901234567890"))
	require.NoError(t, err)
	require.NoError(t, w.store.ConfirmMfaEnrollment(ctx, w.secrets, id.ID,
		func([]byte) (int64, bool, error) { return 5, true, nil }, []string{"digest"}, time.Now()))
	return id
}

// The admin Service main builds can delete an identity that has a second factor, because main
// handed it the secret store; a Service built without it refuses, loudly, and leaves the seed.
func TestTheAdminServiceBuiltLikeMainReachesTheSealedSeed(t *testing.T) {
	w := newMfaWiring(t)
	id := w.enrolled(t, "pat@example.com", "pw")
	saKey, err := credential.DeriveServiceAccountSecretKey(make([]byte, 32))
	require.NoError(t, err)
	svc := newAdminService(w.store, 0, 0, nil, saKey, w.secrets)

	f, err := w.store.MfaFactorFor(context.Background(), id.ID)
	require.NoError(t, err)
	removed, err := svc.DeleteIdentity(context.Background(), "pat@example.com")
	require.NoError(t, err, "the service main builds must hold the secret store")
	require.True(t, removed)
	left, err := w.secrets.Exists(context.Background(), iam.MfaSeedRef(f.ID))
	require.NoError(t, err)
	require.False(t, left, "deleting the identity left its sealed seed behind")

	// The control: the same Service without the store refuses, and deletes nothing.
	w2 := newMfaWiring(t)
	w2.enrolled(t, "lee@example.com", "pw")
	bare := newAdminService(w2.store, 0, 0, nil, saKey, nil)
	_, err = bare.DeleteIdentity(context.Background(), "lee@example.com")
	require.ErrorIs(t, err, iam.ErrMfaNeedsSecretStore)
}

func startMfaBroker(t *testing.T) nats.KeyValue {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	bucket, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "mfa-challenges", TTL: identity.MfaChallengeTTL})
	require.NoError(t, err)
	return bucket
}

type reply struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

// served builds the data plane the way main serves it: the schema parsed from the SDL, the
// providers from dataPlaneProviders, and an identity Manager from newIdentityManager and
// configureMfa.
func (w *mfaWiring) served(t *testing.T, challenges nats.KeyValue) http.Handler {
	t.Helper()
	policy := credential.Policy{Free: 1000, Base: time.Minute, Cap: 4 * time.Minute}
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: policy, credential.KindOAuthClient: policy, credential.KindTotp: {Unthrottled: true},
	})
	require.NoError(t, err)
	m, err := newIdentityManager(nil, w.db, nil, w.secrets, 0, 0, "", identity.BootstrapConfig{}, checker,
		mscfg.InfrastructureConfiguration{})
	require.NoError(t, err)
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "user-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	configureMfa(m, challenges, newMfaMetrics(ms), nil)
	schema := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	return gqlcore.NewHttpHandler(schema, dataPlaneProviders(nil, m, nil, nil), nil)
}

func post(t *testing.T, h http.Handler, doc string, vars map[string]any) reply {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": doc, "variables": vars})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out reply
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

const (
	loginDoc  = `mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}`
	verifyDoc = `mutation($c:String!,$k:String!){verifyMfa(challenge:$c,code:$k){identityToken}}`
)

// T15. The served data plane, wired as main wires it, routes verifyMfa to the Manager's challenge
// store, and an enrolled identity's password sign-in answers MFA_CHALLENGE with a challenge in the
// error's extensions -- and no token.
func TestTheDataPlaneBuiltLikeMainServesTheSecondFactor(t *testing.T) {
	w := newMfaWiring(t)
	w.enrolled(t, "pat@example.com", "pw-1")
	h := w.served(t, startMfaBroker(t))

	r := post(t, h, loginDoc, map[string]any{"e": "pat@example.com", "p": "pw-1"})
	require.Len(t, r.Errors, 1, "the password step of an enrolled identity is an error")
	require.Equal(t, "MFA_CHALLENGE", r.Errors[0].Extensions["code"])
	require.NotEmpty(t, r.Errors[0].Extensions["challenge"])
	require.NotEmpty(t, r.Errors[0].Extensions["expiresAt"])
	require.Equal(t, []any{"TOTP", "RECOVERY_CODE"}, r.Errors[0].Extensions["methods"])
	require.Nil(t, r.Data["login"], "no token for the password alone")

	// verifyMfa is served and reaches the challenge store: an unknown challenge is expired.
	r = post(t, h, verifyDoc, map[string]any{"c": "no-such-challenge", "k": "123456"})
	require.Len(t, r.Errors, 1)
	require.Equal(t, "MFA_CHALLENGE_EXPIRED", r.Errors[0].Extensions["code"], "got %v", r.Errors[0])
}

// With no challenge bucket the Manager main builds fails an enrolled sign-in CLOSED. The twin is
// the wiring above, which holds one.
func TestAnEnrolledSignInFailsClosedWhenMainWiresNoChallengeBucket(t *testing.T) {
	w := newMfaWiring(t)
	w.enrolled(t, "pat@example.com", "pw-1")
	h := w.served(t, nil)

	r := post(t, h, loginDoc, map[string]any{"e": "pat@example.com", "p": "pw-1"})
	require.Len(t, r.Errors, 1)
	require.NotEqual(t, "MFA_CHALLENGE", r.Errors[0].Extensions["code"], "a challenge was issued with nowhere to keep it")
	require.Contains(t, r.Errors[0].Message, "could not be checked right now")
	require.Nil(t, r.Data["login"])
}

// The metrics main registers are exported at zero for every label combination, so an alert over
// increase() sees the first event.
func TestTheMfaCountersAreExportedAtZeroFromStartup(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "user-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	newMfaMetrics(ms)
	families, err := reg.Gather()
	require.NoError(t, err)
	got := map[string]int{}
	for _, f := range families {
		got[f.GetName()] = len(f.GetMetric())
	}
	require.Equal(t, len(mfaMethods)*len(mfaResults), got["devicechain_usermanagement_mfa_verifications_total"])
	require.Equal(t, len(mfaEnrollments), got["devicechain_usermanagement_mfa_enrollments_total"])
	require.Equal(t, 1, got["devicechain_usermanagement_mfa_challenges_total"])
	require.Equal(t, 1, got["devicechain_usermanagement_mfa_identity_locks_total"],
		"the required MfaIdentityLocked alert depends on this series existing at 0")
}
