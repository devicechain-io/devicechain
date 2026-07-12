// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
)

// AdminOAuthClientResolver resolves the AdminOAuthClient type from an
// iam.OAuthClient (ADR-047).
type AdminOAuthClientResolver struct {
	M iam.OAuthClient
}

func (r *AdminOAuthClientResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *AdminOAuthClientResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }
func (r *AdminOAuthClientResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }
func (r *AdminOAuthClientResolver) ClientId() string   { return r.M.ClientId }
func (r *AdminOAuthClientResolver) Name() *string      { return util.NullStr(r.M.Name) }
func (r *AdminOAuthClientResolver) Description() *string {
	return util.NullStr(r.M.Description)
}
func (r *AdminOAuthClientResolver) Enabled() bool { return r.M.Enabled }
func (r *AdminOAuthClientResolver) RedirectUris() []string {
	if r.M.RedirectURIs == nil {
		return []string{}
	}
	return r.M.RedirectURIs
}
func (r *AdminOAuthClientResolver) Scopes() []string {
	if r.M.Scopes == nil {
		return []string{}
	}
	return r.M.Scopes
}

func wrapOAuthClient(c *iam.OAuthClient, err error) (*AdminOAuthClientResolver, error) {
	if err != nil {
		return nil, err
	}
	return &AdminOAuthClientResolver{M: *c}, nil
}

// OauthClients lists the OAuth client registry (requires client:read).
func (r *AdminResolver) OauthClients(ctx context.Context) ([]*AdminOAuthClientResolver, error) {
	if err := auth.Authorize(ctx, auth.ClientRead); err != nil {
		return nil, err
	}
	clients, err := r.getAdminService(ctx).ListOAuthClients(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AdminOAuthClientResolver, 0, len(clients))
	for i := range clients {
		out = append(out, &AdminOAuthClientResolver{M: clients[i]})
	}
	return out, nil
}

// adminOAuthClientCreateInput mirrors AdminOAuthClientCreateRequest.
type adminOAuthClientCreateInput struct {
	ClientId     string
	Name         *string
	Description  *string
	RedirectUris []string
	Scopes       []string
}

// adminOAuthClientUpdateInput mirrors AdminOAuthClientUpdateRequest.
type adminOAuthClientUpdateInput struct {
	Name         *string
	Description  *string
	RedirectUris []string
	Scopes       []string
}

// CreateOauthClient registers an OAuth client (requires client:write).
func (r *AdminResolver) CreateOauthClient(ctx context.Context, args struct {
	Request adminOAuthClientCreateInput
}) (*AdminOAuthClientResolver, error) {
	if err := auth.Authorize(ctx, auth.ClientWrite); err != nil {
		return nil, err
	}
	c, err := r.getAdminService(ctx).CreateOAuthClient(ctx, admin.OAuthClientInput{
		ClientId:     args.Request.ClientId,
		Name:         strOrEmpty(args.Request.Name),
		Description:  strOrEmpty(args.Request.Description),
		RedirectURIs: args.Request.RedirectUris,
		Scopes:       args.Request.Scopes,
	})
	return wrapOAuthClient(c, err)
}

// UpdateOauthClient replaces a client's mutable fields by clientId (requires
// client:write).
func (r *AdminResolver) UpdateOauthClient(ctx context.Context, args struct {
	ClientId string
	Request  adminOAuthClientUpdateInput
}) (*AdminOAuthClientResolver, error) {
	if err := auth.Authorize(ctx, auth.ClientWrite); err != nil {
		return nil, err
	}
	c, err := r.getAdminService(ctx).UpdateOAuthClient(ctx, args.ClientId, admin.OAuthClientMutableInput{
		Name:         strOrEmpty(args.Request.Name),
		Description:  strOrEmpty(args.Request.Description),
		RedirectURIs: args.Request.RedirectUris,
		Scopes:       args.Request.Scopes,
	})
	return wrapOAuthClient(c, err)
}

// SetOauthClientEnabled enables or disables a client (requires client:write).
func (r *AdminResolver) SetOauthClientEnabled(ctx context.Context, args struct {
	ClientId string
	Enabled  bool
}) (*AdminOAuthClientResolver, error) {
	if err := auth.Authorize(ctx, auth.ClientWrite); err != nil {
		return nil, err
	}
	c, err := r.getAdminService(ctx).SetOAuthClientEnabled(ctx, args.ClientId, args.Enabled)
	return wrapOAuthClient(c, err)
}

// DeleteOauthClient removes a client; returns whether one was removed (requires
// client:write).
func (r *AdminResolver) DeleteOauthClient(ctx context.Context, args struct {
	ClientId string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.ClientWrite); err != nil {
		return false, err
	}
	return r.getAdminService(ctx).DeleteOAuthClient(ctx, args.ClientId)
}
