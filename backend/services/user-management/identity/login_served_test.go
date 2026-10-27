// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	umgraphql "github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// A normal sign-in -- the one login every client sends -- is evaluated and ACCEPTED under
// the data-plane handler's per-request credential budget, and ends in an issued identity
// token: one compare, one `login` audit row, and NO error of any kind.
//
// It lives here rather than beside graphql's other login-budget tests because a successful
// login has to MINT, and the graphql package cannot give a Manager a signing key or a session
// store (both are installed by Initialize from NATS). Its earlier copy there passed while the
// resolver panicked on the nil session store: graphql-go recovers a panic into an error, and
// the test tolerated errors.
func TestSingleServedLoginIsAcceptedAndMintsAToken(t *testing.T) {
	const email, password = "victim@devicechain.local", "correct-horse-battery-staple"
	db := putest.NewSQLiteDB(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{}, &iam.MfaFactor{}, &iam.MfaSourceFailure{}, &iam.MfaRecoveryCode{})
	rdbm := &rdb.RdbManager{Database: db}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	sys := core.WithSystemContext(context.Background())
	require.NoError(t, iam.NewStore(rdbm).CreateIdentity(sys,
		&iam.Identity{Email: email, Enabled: true, PasswordHash: string(hash)}))

	var compares atomic.Int32
	policy := credential.Policy{Free: 1000, Base: time.Minute, Cap: 4 * time.Minute}
	checker, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindIdentity: policy, credential.KindOAuthClient: policy},
		credential.WithCompareObserver(func([]byte) { compares.Add(1) }))
	require.NoError(t, err)
	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, checker)
	identity.InstallSigningForTest(t, mgr)

	schema := gqlcore.MustParseSchema(umgraphql.SchemaContent, &umgraphql.SchemaResolver{})
	h := gqlcore.NewHttpHandler(schema, map[gqlcore.ContextKey]interface{}{umgraphql.ContextIdentityKey: mgr}, nil)
	doc := fmt.Sprintf("mutation($e:String!){login(email:$e,password:%q){identityToken}}", password)
	r := post(t, h, doc, map[string]any{"e": email}, "", "192.0.2.1:1234", "")

	require.Emptyf(t, r.Errors, "the sign-in must succeed outright: %s", r.Body)
	var login struct {
		IdentityToken string `json:"identityToken"`
	}
	require.NoError(t, json.Unmarshal(r.Data["login"], &login))
	require.NotEmpty(t, login.IdentityToken)
	require.Equal(t, int32(1), compares.Load(), "one request, one compare")

	count := func(op string) int64 {
		var n int64
		require.NoError(t, db.WithContext(sys).Model(&rdb.AuditEvent{}).Where("operation = ?", op).Count(&n).Error)
		return n
	}
	require.Equal(t, int64(1), count(rdb.AuditOpLogin))
	require.Equal(t, int64(0), count(rdb.AuditOpLoginFailed))
}

// A sign-in is not a mutation, and its audit row is written best effort: when the journal
// cannot take the row, the sign-in still succeeds. This is the deliberate exception to the
// journal's rule that an entity change and its audit row commit together or not at all — a
// password sign-in writes no audited row of its own (its session lives in the key-value
// store), so nothing here can roll back, and failing every sign-in because the audit table
// is briefly unwritable would turn a journal outage into a platform-wide lockout.
func TestASignInSucceedsWhenItsAuditRowCannotBeWritten(t *testing.T) {
	const email, password = "jo@devicechain.local", "correct-horse-battery-staple"
	db := putest.NewSQLiteDB(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{}, &iam.MfaFactor{}, &iam.MfaSourceFailure{}, &iam.MfaRecoveryCode{})
	rdbm := &rdb.RdbManager{Database: db}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	sys := core.WithSystemContext(context.Background())
	require.NoError(t, iam.NewStore(rdbm).CreateIdentity(sys,
		&iam.Identity{Email: email, Enabled: true, PasswordHash: string(hash)}))

	policy := credential.Policy{Free: 1000, Base: time.Minute, Cap: 4 * time.Minute}
	checker, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindIdentity: policy, credential.KindOAuthClient: policy})
	require.NoError(t, err)
	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, checker)
	identity.InstallSigningForTest(t, mgr)

	// The journal is unwritable from here on, so every audit insert fails.
	require.NoError(t, db.Migrator().DropTable(&rdb.AuditEvent{}))
	require.Error(t, rdbm.RecordAuthEvent(sys, rdb.AuditOpLogin, email, ""),
		"precondition: the journal must refuse a row, or this test exercises nothing")

	auth, err := mgr.Login(context.Background(), email, password)
	require.NoError(t, err, "a sign-in must not fail because its audit row could not be written")
	require.NotEmpty(t, auth.IdentityToken)
}
