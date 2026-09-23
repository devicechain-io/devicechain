// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// One unauthenticated request used to be able to carry ~1,500 aliased `login`
// mutations: graphql-go ran them one after another, each a database lookup, a bcrypt
// compare and an audit write, and nothing counted attempts. These tests drive the REAL
// data-plane surface — the served schema through gqlcore.MustParseSchema, the request
// through gqlcore.NewHttpHandler with no Authorization header, the real resolver over a
// real identity.Manager — and pin the two defences:
//
//   - the root-field ceiling refuses the amplified document before any resolver runs;
//   - a document under the ceiling still cannot buy more than the free attempts,
//     because every alias goes through the same per-account backoff.

const (
	victimEmail    = "victim@devicechain.local"
	victimPassword = "correct-horse-battery-staple"
)

// loginFixture is a Manager whose credential checker uses an in-memory attempt store
// and a clock that does not move, so which aliases are throttled is decided by the
// attempt COUNT alone and never by how long the test took.
type loginFixture struct {
	mgr   *identity.Manager
	rdbm  *rdb.RdbManager
	store *credentialtest.Store
	// compares counts the bcrypt compares the checker actually ran, the dummy included.
	compares atomic.Int32
}

func newLoginFixture(t *testing.T, free int) *loginFixture {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	hash, err := bcrypt.GenerateFromPassword([]byte(victimPassword), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, iam.NewStore(rdbm).CreateIdentity(core.WithSystemContext(context.Background()),
		&iam.Identity{Email: victimEmail, Enabled: true, PasswordHash: string(hash)}))

	policy := credential.Policy{Free: free, Base: time.Minute, Cap: 4 * time.Minute}
	store := credentialtest.NewStore()
	frozen := time.Unix(1_700_000_000, 0)
	f := &loginFixture{rdbm: rdbm, store: store}
	checker, err := credential.NewChecker(store,
		map[credential.Kind]credential.Policy{credential.KindIdentity: policy, credential.KindOAuthClient: policy},
		credential.WithClock(func() time.Time { return frozen }),
		credential.WithCompareObserver(func([]byte) { f.compares.Add(1) }))
	require.NoError(t, err)
	f.mgr = identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, checker)
	return f
}

// aliasedLogins builds one document carrying n aliased login mutations, all with
// wrong passwords, against the email in $e.
func aliasedLogins(n int) string {
	var sb strings.Builder
	sb.WriteString("mutation($e:String!){")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "a%d:login(email:$e,password:%q){identityToken}", i, fmt.Sprintf("wrong-guess-%d", i))
	}
	sb.WriteString("}")
	return sb.String()
}

type gqlError struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path"`
	Extensions map[string]any `json:"extensions"`
}

type gqlResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []gqlError                 `json:"errors"`
}

// post drives one document through the real data-plane handler, unauthenticated, with
// $e set to the victim's email.
func (f *loginFixture) post(t *testing.T, doc string) (int, gqlResponse) {
	t.Helper()
	return f.postAs(t, doc, victimEmail)
}

// postAs is post with $e set to email.
func (f *loginFixture) postAs(t *testing.T, doc, email string) (int, gqlResponse) {
	t.Helper()
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	h := gqlcore.NewHttpHandler(schema, map[gqlcore.ContextKey]interface{}{ContextIdentityKey: f.mgr}, nil)
	body, err := json.Marshal(map[string]any{"query": doc, "variables": map[string]any{"e": email}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out gqlResponse
	require.NoErrorf(t, json.Unmarshal(rec.Body.Bytes(), &out), "not GraphQL JSON (status %d): %s", rec.Code, rec.Body.String())
	return rec.Code, out
}

func (f *loginFixture) audit(t *testing.T, op string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.rdbm.Database.WithContext(core.WithSystemContext(context.Background())).
		Model(&rdb.AuditEvent{}).Where("operation = ?", op).Count(&n).Error)
	return n
}

// The amplified document from the original report is refused as a whole, before any
// resolver runs. The positive control runs in the SAME fixture afterwards: a single
// wrong login writes exactly one login_failed row — so the zero before it is a
// refusal, not a fixture that cannot record anything.
func TestAmplifiedLoginDocumentIsRefusedBeforeExecution(t *testing.T) {
	f := newLoginFixture(t, 5)

	doc := aliasedLogins(1500)
	require.Less(t, len(doc), gqlcore.DefaultGraphQLMaxQueryLength,
		"the document must be under the byte ceiling, or this would test the wrong limit")

	code, resp := f.post(t, doc)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, resp.Errors, 1)
	assert.Empty(t, resp.Errors[0].Path, "a request-level error, not a resolver error")
	assert.Equal(t, "TOO_MANY_ROOT_FIELDS", resp.Errors[0].Extensions["code"])
	assert.Equal(t, "mutation (anonymous) selects 1500 root fields; the maximum is 5", resp.Errors[0].Message)
	assert.Nil(t, resp.Data)
	assert.Equal(t, int64(0), f.audit(t, rdb.AuditOpLoginFailed))
	assert.Empty(t, f.store.Ops(), "no login ran, so no attempt was charged")

	code, resp = f.post(t, aliasedLogins(1))
	require.Equal(t, http.StatusOK, code)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, []any{"a1"}, resp.Errors[0].Path)
	assert.Equal(t, int64(1), f.audit(t, rdb.AuditOpLoginFailed), "control: one wrong login writes one row")
}

// A document UNDER the ceiling still cannot buy more than the free attempts. With the
// free allowance at 3, five aliased wrong logins in one request evaluate three and
// come back THROTTLED for the other two — which wrote no audit row.
//
// The per-request credential budget (default 1) would refuse the second alias on its
// own, so it is raised here to 5: this test is about the per-ACCOUNT backoff, and it
// has to be the only limit in play for the throttles below to be the backoff's.
func TestAliasesUnderTheCeilingAreThrottledPerAccount(t *testing.T) {
	t.Setenv(gqlcore.EnvGraphQLMaxCredentialChecks, "5")
	f := newLoginFixture(t, 3)

	_, resp := f.post(t, aliasedLogins(5))
	byAlias := map[string]gqlError{}
	for _, e := range resp.Errors {
		require.Len(t, e.Path, 1, "every error is a per-alias resolver error: %+v", e)
		byAlias[e.Path[0].(string)] = e
	}
	require.Len(t, byAlias, 5)
	for _, a := range []string{"a1", "a2", "a3"} {
		assert.Equal(t, "invalid credentials", byAlias[a].Message, a)
		assert.Nil(t, byAlias[a].Extensions, a)
	}
	for _, a := range []string{"a4", "a5"} {
		assert.Equal(t, "THROTTLED", byAlias[a].Extensions["code"], a)
		assert.Equal(t, float64(60), byAlias[a].Extensions["retryAfterSeconds"], a)
		assert.Equal(t, "too many failed sign-in attempts; try again in 60 seconds", byAlias[a].Message, a)
	}
	assert.Equal(t, int64(3), f.audit(t, rdb.AuditOpLoginFailed), "only the evaluated attempts are audited")
}

// When the attempt store is down, login fails closed and SAYS so, with its own code —
// otherwise every broker outage would reach the user as "wrong password".
func TestLoginReportsAnUnavailableAttemptStore(t *testing.T) {
	f := newLoginFixture(t, 3)
	f.store.Fail = errors.New("nats: connection closed")

	doc := fmt.Sprintf("mutation($e:String!){login(email:$e,password:%q){identityToken}}", victimPassword)
	_, resp := f.post(t, doc)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "UNAVAILABLE", resp.Errors[0].Extensions["code"])
	assert.Equal(t, "sign-in is temporarily unavailable; try again shortly", resp.Errors[0].Message)
	assert.Equal(t, int64(0), f.audit(t, rdb.AuditOpLogin), "the correct password must not sign in unchecked")
}
