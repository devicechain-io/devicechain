// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boundsCtx authorizes the caller and supplies a nil admin service: every refusal below
// must happen before the service is touched, so a call that got past its bound would
// dereference nil and fail the test loudly.
func boundsCtx() context.Context {
	ctx := adminCtx(string(auth.UserWrite), string(auth.RoleWrite))
	return context.WithValue(ctx, ContextAdminKey, (*admin.Service)(nil))
}

func manyStrings(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "t"
	}
	return out
}

func requireLimit(t *testing.T, err error) {
	t.Helper()
	_, ok := limit.As(err)
	require.True(t, ok, "want a limit refusal, got %v", err)
}

func TestAdminPasswordOverSeventyTwoBytesIsRefused(t *testing.T) {
	r := &AdminResolver{}
	long := strings.Repeat("p", maxPasswordBytes+1)

	_, err := r.SetPassword(boundsCtx(), struct {
		Email    string
		Password string
	}{Email: "a@b.c", Password: long})
	requireLimit(t, err)

	_, err = r.CreateIdentity(boundsCtx(), struct{ Request adminIdentityCreateInput }{
		Request: adminIdentityCreateInput{Email: "a@b.c", Password: long}})
	requireLimit(t, err)

	// 72 bytes is accepted by the bound (it then reaches the nil service, which panics).
	assert.Panics(t, func() {
		_, _ = r.SetPassword(boundsCtx(), struct {
			Email    string
			Password string
		}{Email: "a@b.c", Password: strings.Repeat("p", maxPasswordBytes)})
	}, "a 72-byte password must pass the bound")
}

func TestAdminListsOverTheBoundAreRefused(t *testing.T) {
	r := &AdminResolver{}
	over := manyStrings(maxAdminListEntries + 1)

	_, err := r.SetSystemRoles(boundsCtx(), struct {
		Email      string
		RoleTokens []string
	}{Email: "a@b.c", RoleTokens: over})
	requireLimit(t, err)

	_, err = r.AddMembership(boundsCtx(), struct {
		Email      string
		Tenant     string
		RoleTokens []string
	}{Email: "a@b.c", Tenant: "t", RoleTokens: over})
	requireLimit(t, err)

	_, err = r.SetMembershipRoles(boundsCtx(), struct {
		Email      string
		Tenant     string
		RoleTokens []string
	}{Email: "a@b.c", Tenant: "t", RoleTokens: over})
	requireLimit(t, err)

	_, err = r.CreateIdentity(boundsCtx(), struct{ Request adminIdentityCreateInput }{
		Request: adminIdentityCreateInput{Email: "a@b.c", Password: "pw", SystemRoles: over}})
	requireLimit(t, err)

	_, err = r.CreateRole(boundsCtx(), struct{ Request adminRoleCreateInput }{
		Request: adminRoleCreateInput{Scope: "tenant", Token: "r", Authorities: over}})
	requireLimit(t, err)

	update := admin.RoleUpdateRequest{}
	update.Authorities.Set = true
	update.Authorities.Value = over
	_, err = r.UpdateRole(boundsCtx(), struct {
		Scope   string
		Token   string
		Request admin.RoleUpdateRequest
	}{Scope: "tenant", Token: "r", Request: update})
	requireLimit(t, err)
}

// An unauthorized caller is told so, not told about a size bound.
func TestAdminBoundsRunAfterAuthorization(t *testing.T) {
	r := &AdminResolver{}
	ctx := context.WithValue(adminCtx(string(auth.UserRead)), ContextAdminKey, (*admin.Service)(nil))
	_, err := r.SetSystemRoles(ctx, struct {
		Email      string
		RoleTokens []string
	}{Email: "a@b.c", RoleTokens: manyStrings(maxAdminListEntries + 1)})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}
