// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// gatewayWireCtx is a tenant with a device type and three devices, behind a real Api over sqlite.
func gatewayWireCtx(t *testing.T) context.Context {
	t.Helper()
	// A shared-cache in-memory database, one per call: graphql-go resolves sibling fields on
	// separate goroutines, and each connection to a plain ":memory:" database is its own empty one.
	db := sqlitetest.OpenMemory(t, &gorm.Config{})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceType{}, &model.Device{}, &model.DeviceCredential{}))
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, keyedApi(db))
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)

	dt := &model.DeviceType{}
	dt.Token = "dt"
	require.NoError(t, api.RDB.DB(ctx).Create(dt).Error)
	for _, token := range []string{"gw", "c1", "c2"} {
		_, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: token, DeviceTypeToken: "dt"})
		require.NoError(t, err)
	}
	return ctx
}

const assignGatewayMutation = `mutation($d: String!, $g: String!) {
  assignDeviceGateway(deviceToken: $d, gatewayToken: $g) { token gateway { token } }
}`

const clearGatewayMutation = `mutation($d: String!) { clearDeviceGateway(deviceToken: $d) { token gateway { token } } }`

// Assigning a gateway grants the gateway the same standing over the device as issuing the device
// a credential, so the gate is the credential-managing authority (device:write): a viewer is
// forbidden, an anonymous caller unauthenticated, and a member limited to part of the tenant is
// refused by the resolver itself (Authorize refuses every scoped caller, so both devices being
// "in scope" is not a question a scoped caller can reach). The device:write holder succeeds,
// which is the control for the refusals.
func TestGatewayMutationsAreHeldToTheCredentialAuthority(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	vars := map[string]any{"d": "c1", "g": "gw"}

	anon := schema.Exec(gatewayWireCtx(t), assignGatewayMutation, "", vars)
	require.Len(t, anon.Errors, 1)
	require.Equal(t, auth.ErrUnauthenticated.Error(), anon.Errors[0].Message)

	viewer := schema.Exec(withAuthorities(gatewayWireCtx(t), viewerBaseline...), assignGatewayMutation, "", vars)
	require.Len(t, viewer.Errors, 1)
	require.Equal(t, auth.ErrForbidden.Error(), viewer.Errors[0].Message)

	clearViewer := schema.Exec(withAuthorities(gatewayWireCtx(t), viewerBaseline...), clearGatewayMutation, "", map[string]any{"d": "c1"})
	require.Len(t, clearViewer.Errors, 1)
	require.Equal(t, auth.ErrForbidden.Error(), clearViewer.Errors[0].Message)

	scopedCtx := auth.WithClaims(gatewayWireCtx(t), &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceWrite)}, ScopeAnchors: []string{"customer:acme-corp"}})
	scoped := schema.Exec(scopedCtx, assignGatewayMutation, "", vars)
	require.Len(t, scoped.Errors, 1)
	require.Equal(t, auth.ErrScopeUnsupported.Error(), scoped.Errors[0].Message,
		"a scoped member was not refused by the resolver itself")

	ctx := withAuthorities(gatewayWireCtx(t), append(viewerBaseline, auth.DeviceWrite)...)
	ok := schema.Exec(ctx, assignGatewayMutation, "", vars)
	require.Empty(t, ok.Errors)
	var out struct {
		AssignDeviceGateway struct {
			Token   string
			Gateway struct{ Token string }
		}
	}
	require.NoError(t, json.Unmarshal(ok.Data, &out))
	require.Equal(t, "c1", out.AssignDeviceGateway.Token)
	require.Equal(t, "gw", out.AssignDeviceGateway.Gateway.Token)

	// The rule refusals carry the INVALID_VALUE code, not a bare 500.
	self := schema.Exec(ctx, assignGatewayMutation, "", map[string]any{"d": "gw", "g": "gw"})
	require.Len(t, self.Errors, 1)
	require.Contains(t, self.Errors[0].Message, "its own gateway")
	require.Equal(t, "INVALID_VALUE", self.Errors[0].Extensions["code"])
}

// Device.gateway and Device.children read the assignment over the wire, and a clear takes the
// device out of the gateway's list.
func TestDeviceGatewayAndChildrenFieldsReadTheAssignment(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	ctx := withAuthorities(gatewayWireCtx(t), append(viewerBaseline, auth.DeviceWrite)...)
	for _, c := range []string{"c1", "c2"} {
		res := schema.Exec(ctx, assignGatewayMutation, "", map[string]any{"d": c, "g": "gw"})
		require.Empty(t, res.Errors)
	}
	query := `query { devicesByToken(tokens: ["gw", "c1"]) { token gateway { token } children(pagination: {pageNumber: 1, pageSize: 10}) { results { token } pagination { totalRecords } } } }`
	read := func() []struct {
		Token    string
		Gateway  *struct{ Token string }
		Children struct {
			Results    []struct{ Token string }
			Pagination struct{ TotalRecords int }
		}
	} {
		res := schema.Exec(ctx, query, "", nil)
		require.Empty(t, res.Errors)
		var out struct {
			DevicesByToken []struct {
				Token    string
				Gateway  *struct{ Token string }
				Children struct {
					Results    []struct{ Token string }
					Pagination struct{ TotalRecords int }
				}
			}
		}
		require.NoError(t, json.Unmarshal(res.Data, &out))
		return out.DevicesByToken
	}
	byToken := map[string]int{}
	rows := read()
	for i, r := range rows {
		byToken[r.Token] = i
	}
	gw, c1 := rows[byToken["gw"]], rows[byToken["c1"]]
	require.Nil(t, gw.Gateway)
	require.Len(t, gw.Children.Results, 2)
	require.Equal(t, 2, gw.Children.Pagination.TotalRecords)
	require.NotNil(t, c1.Gateway)
	require.Equal(t, "gw", c1.Gateway.Token)
	require.Empty(t, c1.Children.Results)

	cleared := schema.Exec(ctx, clearGatewayMutation, "", map[string]any{"d": "c1"})
	require.Empty(t, cleared.Errors)
	rows = read()
	for i, r := range rows {
		byToken[r.Token] = i
	}
	require.Len(t, rows[byToken["gw"]].Children.Results, 1)
	require.Nil(t, rows[byToken["c1"]].Gateway)
}

// A device is created behind a gateway through the create request's gatewayToken.
func TestCreateDeviceAcceptsAGatewayToken(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	ctx := withAuthorities(gatewayWireCtx(t), append(viewerBaseline, auth.DeviceWrite)...)
	res := schema.Exec(ctx, `mutation($r: DeviceCreateRequest) { createDevice(request: $r) { token } }`, "",
		map[string]any{"r": map[string]any{"token": "behind", "deviceTypeToken": "dt", "gatewayToken": "gw"}})
	require.Empty(t, res.Errors)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	rows, err := api.DevicesByToken(ctx, []string{"behind"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].GatewayId)

	// Unknown field names are rejected (graphql-go's unknown-field guard), so a misspelling cannot half-configure.
	bad := schema.Exec(ctx, `mutation($r: DeviceCreateRequest) { createDevice(request: $r) { token } }`, "",
		map[string]any{"r": map[string]any{"token": "x", "deviceTypeToken": "dt", "gatewayTokn": "gw"}})
	require.NotEmpty(t, bad.Errors)
}
