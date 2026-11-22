// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// Three served operations no other test named: rejectSsoLinkRequest, tierColorPalette and
// serviceAccountTokenEndpoint.

// adminCtxWith is the admin plane's request context for a caller holding exactly the given
// authorities, so an authority check is exercised rather than satisfied by the "*" wildcard.
func (e *adminEscalationEnv) adminCtxWith(authorities ...string) context.Context {
	ctx := auth.WithClaims(core.WithSystemContext(context.Background()),
		&auth.Claims{TokenType: auth.TokenTypeIdentity, Username: "helen@example.com", Authorities: authorities})
	ctx = context.WithValue(ctx, ContextAdminKey, e.svc)
	return context.WithValue(ctx, ContextAdminSsoKey, e.mgr)
}

func (e *adminEscalationEnv) seedLinkRequest(t *testing.T, subject, email string) *iam.SsoLinkRequest {
	t.Helper()
	var conn iam.IdpConnection
	require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).First(&conn).Error)
	request := &iam.SsoLinkRequest{ConnectionID: conn.ID, Subject: subject, Email: email}
	require.NoError(t, e.store.UpsertLinkRequest(context.Background(), request))
	return request
}

func rejectDoc(id any) string {
	return fmt.Sprintf(`mutation { rejectSsoLinkRequest(id: %q) }`, fmt.Sprint(id))
}

// Reject deletes exactly the request it names, links nothing, and a second reject (or an id
// that never existed) answers false rather than an error.
func TestRejectSsoLinkRequestDeletesOnlyThatRequest(t *testing.T) {
	e := newAdminEscalationEnv(t)
	ctx := context.Background()
	rejected := e.seedLinkRequest(t, "rejected-sub", "plain@example.com")
	kept := e.seedLinkRequest(t, "kept-sub", "plain@example.com")

	res := e.schema.Exec(e.adminCtxWith("user:write"), rejectDoc(rejected.ID), "", nil)
	require.Empty(t, res.Errors)
	var data struct {
		Reject bool `json:"rejectSsoLinkRequest"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &data))
	require.True(t, data.Reject, "rejecting an existing request answers true")

	_, err := e.store.SsoLinkRequestByID(ctx, rejected.ID)
	require.Error(t, err, "the rejected request is still stored")
	_, err = e.store.SsoLinkRequestByID(ctx, kept.ID)
	require.NoError(t, err, "rejecting one request deleted another")
	pending, err := e.store.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// Rejecting links nothing: no identity link exists for the rejected subject.
	_, err = e.store.LinkBySubject(ctx, rejected.ConnectionID, "rejected-sub")
	require.Error(t, err, "a rejected request produced an identity link")

	// Absent: the first request again, and an id that never existed, answer false.
	for _, id := range []any{rejected.ID, 999999} {
		res = e.schema.Exec(e.adminCtxWith("user:write"), rejectDoc(id), "", nil)
		require.Empty(t, res.Errors, "id %v", id)
		require.NoError(t, json.Unmarshal(res.Data, &data))
		require.False(t, data.Reject, "id %v is absent and must answer false", id)
	}
	// A malformed id is a not-found refusal, as approve's.
	res = e.schema.Exec(e.adminCtxWith("user:write"), rejectDoc("nope"), "", nil)
	require.Len(t, res.Errors, 1)
	require.Contains(t, res.Errors[0].Message, "link request not found")
	_, err = e.store.SsoLinkRequestByID(ctx, kept.ID)
	require.NoError(t, err)
}

// Reject needs user:write: user:read alone, an unrelated authority and no claims are all
// refused and the request survives.
func TestRejectSsoLinkRequestNeedsUserWrite(t *testing.T) {
	e := newAdminEscalationEnv(t)
	request := e.seedLinkRequest(t, "guarded-sub", "plain@example.com")

	for name, c := range map[string]context.Context{
		"user:read only": e.adminCtxWith("user:read"),
		"unrelated":      e.adminCtxWith("tenant:write"),
		"no authorities": e.adminCtxWith(),
		"anonymous":      auth.WithClaims(e.adminCtxWith("user:write"), nil),
	} {
		res := e.schema.Exec(c, rejectDoc(request.ID), "", nil)
		require.Len(t, res.Errors, 1, name)
		_, err := e.store.SsoLinkRequestByID(context.Background(), request.ID)
		require.NoError(t, err, "%s: a refused reject deleted the request", name)
	}
}

// tierColorPalette serves the closed vocabulary the write path validates: a stable, non-empty,
// duplicate-free list in which every token is a valid tier colour, and which a tier can
// actually be created with.
func TestTierColorPaletteMatchesTheWriteValidation(t *testing.T) {
	e := newAdminEscalationEnv(t)
	const doc = `{ tierColorPalette { colors } }`
	read := func() []string {
		res := e.schema.Exec(e.adminCtxWith("tenant:read"), doc, "", nil)
		require.Empty(t, res.Errors)
		var data struct {
			Palette struct {
				Colors []string `json:"colors"`
			} `json:"tierColorPalette"`
		}
		require.NoError(t, json.Unmarshal(res.Data, &data))
		return data.Palette.Colors
	}

	colors := read()
	require.NotEmpty(t, colors)
	require.Equal(t, colors, read(), "the palette order is not stable between reads")
	require.Equal(t, iam.TierColors(), colors)
	seen := map[string]bool{}
	for _, c := range colors {
		require.NotEmpty(t, c, "the palette lists the empty (no colour) value")
		require.False(t, seen[c], "colour %q is listed twice", c)
		seen[c] = true
		require.True(t, iam.ValidTierColor(c), "listed colour %q fails the tier colour validation", c)
	}
	require.False(t, iam.ValidTierColor("not-a-palette-colour"), "the validator accepts a colour outside the palette")

	// Every listed colour is accepted when a tier is created with it.
	for i, c := range colors {
		res := e.schema.Exec(e.adminCtxWith("tenant:write"), createTierDoc, "",
			map[string]any{"r": map[string]any{"token": fmt.Sprintf("palette-%d", i), "color": c}})
		require.Empty(t, res.Errors, "creating a tier coloured %q", c)
	}
	res := e.schema.Exec(e.adminCtxWith("tenant:write"), createTierDoc, "",
		map[string]any{"r": map[string]any{"token": "palette-bad", "color": "not-a-palette-colour"}})
	require.Len(t, res.Errors, 1, "a colour outside the palette was accepted")
}

// The palette needs tenant:read.
func TestTierColorPaletteNeedsTenantRead(t *testing.T) {
	e := newAdminEscalationEnv(t)
	for name, c := range map[string]context.Context{
		"user:write only": e.adminCtxWith("user:write"),
		"no authorities":  e.adminCtxWith(),
		"anonymous":       auth.WithClaims(e.adminCtxWith("tenant:read"), nil),
	} {
		res := e.schema.Exec(c, `{ tierColorPalette { colors } }`, "", nil)
		require.Len(t, res.Errors, 1, name)
	}
}

// serviceAccountTokenEndpoint is null when the instance has no OAuth issuer, the token URL
// when it has one, and needs member:read.
func TestServiceAccountTokenEndpoint(t *testing.T) {
	const doc = `{ serviceAccountTokenEndpoint }`
	readOnly := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@acme.example",
		Authorities: []string{string(auth.MemberRead)}}

	unset := newWireEnv(t) // issuer ""
	data, errs := unset.exec(t, readOnly, doc, nil)
	require.Nil(t, errs)
	require.Contains(t, data, "serviceAccountTokenEndpoint")
	require.Nil(t, data["serviceAccountTokenEndpoint"], "no issuer configured must answer null, not an empty string")

	set := newServiceAccountWireEnv(t) // issuer https://dc.example.com/api/user-management
	data, errs = set.exec(t, readOnly, doc, nil)
	require.Nil(t, errs)
	require.Equal(t, "https://dc.example.com/api/user-management/oauth/token", data["serviceAccountTokenEndpoint"])

	for name, claims := range map[string]*auth.Claims{
		"no authorities": {TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "x@acme.example"},
		"unrelated":      {TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "x@acme.example", Authorities: []string{string(auth.DeviceRead)}},
	} {
		data, errs = set.exec(t, claims, doc, nil)
		require.NotNil(t, errs, name)
		require.Nil(t, data["serviceAccountTokenEndpoint"], name)
	}
}

func (e *adminEscalationEnv) rejectAuditRows(t *testing.T) []rdb.AuditEvent {
	t.Helper()
	require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&rdb.AuditEvent{}))
	var rows []rdb.AuditEvent
	require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).
		Where("operation = ?", rdb.AuditOpIdentityLinkRejected).Order("id").Find(&rows).Error)
	return rows
}

// A reject writes exactly one audit row, naming the actor and the request; a repeated reject,
// an unknown id and a refused caller write none.
func TestRejectSsoLinkRequestIsAudited(t *testing.T) {
	e := newAdminEscalationEnv(t)
	request := e.seedLinkRequest(t, "audited-sub", "plain@example.com")
	other := e.seedLinkRequest(t, "other-sub", "plain@example.com")
	require.Empty(t, e.rejectAuditRows(t))

	// Refused callers: nothing deleted, nothing audited.
	for _, c := range []context.Context{e.adminCtxWith("user:read"), auth.WithClaims(e.adminCtxWith("user:write"), nil)} {
		require.Len(t, e.schema.Exec(c, rejectDoc(request.ID), "", nil).Errors, 1)
	}
	require.Empty(t, e.rejectAuditRows(t), "a refused reject wrote an audit row")

	res := e.schema.Exec(e.adminCtxWith("user:write"), rejectDoc(request.ID), "", nil)
	require.Empty(t, res.Errors)
	rows := e.rejectAuditRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, "helen@example.com", rows[0].Actor)
	require.Equal(t, rdb.AuditCategoryAuth, rows[0].Category)
	require.Empty(t, rows[0].TenantId)
	require.Contains(t, rows[0].EntityLabel, fmt.Sprintf("request=%d", request.ID))
	require.Contains(t, rows[0].EntityLabel, "connection=instance")
	require.NotContains(t, rows[0].EntityLabel, "audited-sub")
	require.NotContains(t, rows[0].EntityLabel, "plain@example.com")

	// Already gone, never existed, malformed: no further rows.
	for _, id := range []any{request.ID, 999999, "nope"} {
		e.schema.Exec(e.adminCtxWith("user:write"), rejectDoc(id), "", nil)
	}
	require.Len(t, e.rejectAuditRows(t), 1)
	_ = other
}
