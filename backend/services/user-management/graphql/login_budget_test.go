// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The per-request credential budget, driven through the REAL data-plane handler: the
// served schema, an unauthenticated request, the real resolver over a real
// identity.Manager. The fixture's free allowance is set far above anything here, so
// the per-account backoff never fires and every refusal below is the budget's.
//
// The compare count is taken from the checker itself (credential.WithCompareObserver),
// so it counts bcrypt compares actually paid for, the dummy included — not errors, which
// cannot show whether a compare ran.

const generousFree = 1000

// byAlias indexes a response's errors by the alias they belong to.
func byAlias(t *testing.T, resp gqlResponse) map[string]gqlError {
	t.Helper()
	out := map[string]gqlError{}
	for _, e := range resp.Errors {
		require.Len(t, e.Path, 1, "every error is a per-alias resolver error: %+v", e)
		out[e.Path[0].(string)] = e
	}
	return out
}

// Five aliased logins — at the root-field ceiling, so the counter lets them through —
// pay exactly ONE compare. The first alias is evaluated (a wrong password: "invalid
// credentials", one audit row); every other alias is refused with its own code and did
// nothing: no compare, no attempt charged, no audit row.
func TestAliasedLoginsPayOneCompare(t *testing.T) {
	f := newLoginFixture(t, generousFree)

	_, resp := f.post(t, aliasedLogins(gqlcore.DefaultGraphQLMaxMutationRootFields))
	errs := byAlias(t, resp)
	require.Len(t, errs, gqlcore.DefaultGraphQLMaxMutationRootFields)
	assert.Equal(t, "invalid credentials", errs["a1"].Message)
	for i := 2; i <= gqlcore.DefaultGraphQLMaxMutationRootFields; i++ {
		a := fmt.Sprintf("a%d", i)
		assert.Equal(t, credential.CodeTooManyCredentialChecks, errs[a].Extensions["code"], a)
		assert.Equal(t, "this request has already made its credential checks; send one sign-in per request", errs[a].Message, a)
	}
	assert.Equal(t, int32(1), f.compares.Load(), "one request, one bcrypt compare")
	assert.Equal(t, int64(1), f.audit(t, rdb.AuditOpLoginFailed), "only the evaluated alias is audited")
	assert.Equal(t, []string{"get:not-found", "create:ok"}, f.store.Ops(), "only the evaluated alias charged an attempt")
}

// 🔴 THE BACKSTOP HOLDS WHEN THE ROOT-FIELD COUNT DOES NOT. With the root-field ceiling
// raised far enough to let the ORIGINAL 1,500-alias attack document through — standing
// in for any document the counter misreads — the request still pays one compare.
func TestBudgetHoldsWhateverTheRootFieldCount(t *testing.T) {
	t.Setenv(gqlcore.EnvGraphQLMaxMutationRootFields, "2000")
	f := newLoginFixture(t, generousFree)

	_, resp := f.post(t, aliasedLogins(1500))
	errs := byAlias(t, resp)
	require.Len(t, errs, 1500, "the root-field ceiling really was out of the way: every alias ran its resolver")
	refused := 0
	for _, e := range errs {
		if e.Extensions["code"] == credential.CodeTooManyCredentialChecks {
			refused++
		}
	}
	assert.Equal(t, 1499, refused)
	assert.Equal(t, int32(1), f.compares.Load())
	assert.Equal(t, int64(1), f.audit(t, rdb.AuditOpLoginFailed))
}

// A normal sign-in — the one login every client sends — is evaluated and ACCEPTED under
// the handler's budget: one compare, and the `login` audit row Login writes only once
// the checker has accepted the password. That row is the observation point because this
// fixture's Manager has no signing key (Initialize needs the NATS lock), so the token it
// would mint next cannot be minted here; identity's TestLoginWithinARequestBudget runs
// the same budget through to an issued token.
func TestSingleLoginIsEvaluatedAndAccepted(t *testing.T) {
	f := newLoginFixture(t, generousFree)
	doc := fmt.Sprintf("mutation($e:String!){login(email:$e,password:%q){identityToken}}", victimPassword)
	_, resp := f.post(t, doc)
	for _, e := range resp.Errors {
		assert.NotEqual(t, credential.CodeTooManyCredentialChecks, e.Extensions["code"], "%+v", e)
		assert.NotEqual(t, "invalid credentials", e.Message)
	}
	assert.Equal(t, int32(1), f.compares.Load())
	assert.Equal(t, int64(1), f.audit(t, rdb.AuditOpLogin), "the checker accepted the password")
	assert.Equal(t, int64(0), f.audit(t, rdb.AuditOpLoginFailed))
}

// The budget is per REQUEST: each request gets its own, so a client that signs in,
// fails, and tries again is evaluated both times.
func TestBudgetIsPerRequest(t *testing.T) {
	f := newLoginFixture(t, generousFree)
	for i := 0; i < 3; i++ {
		_, resp := f.post(t, aliasedLogins(1))
		require.Len(t, resp.Errors, 1)
		assert.Equal(t, "invalid credentials", resp.Errors[0].Message, "request %d is evaluated", i)
	}
	assert.Equal(t, int32(3), f.compares.Load())
}

// 🔴 UNDER EXHAUSTION, AN ACCOUNT THAT EXISTS AND ONE THAT DOES NOT ARE THE SAME. The
// same three-alias document against the real account and an unknown address produces
// the same response, the same number of compares, and the same attempt-store trace: the
// budget refusal is taken before the address is looked at, and the one evaluated alias
// pays a compare either way.
func TestBudgetRefusalIsTheSameForKnownAndUnknownAccounts(t *testing.T) {
	// Each run is a subtest because each fixture opens its own in-memory database,
	// named after the test.
	run := func(name, email string) (resp gqlResponse, compares int32, ops []string) {
		t.Run(name, func(t *testing.T) {
			f := newLoginFixture(t, generousFree)
			_, resp = f.postAs(t, aliasedLogins(3), email)
			compares, ops = f.compares.Load(), f.store.Ops()
		})
		return
	}
	known, knownCompares, knownOps := run("known", victimEmail)
	unknown, unknownCompares, unknownOps := run("unknown", "nobody@devicechain.local")

	assert.Equal(t, known, unknown)
	assert.Equal(t, int32(1), knownCompares)
	assert.Equal(t, knownCompares, unknownCompares)
	assert.Equal(t, knownOps, unknownOps)
	errs := byAlias(t, known)
	assert.Equal(t, credential.CodeTooManyCredentialChecks, errs["a3"].Extensions["code"], "and the case did exhaust the budget")
}
