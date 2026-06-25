// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// --------------------------
// Role resolver
// --------------------------

type RoleResolver struct {
	M model.Role
	C context.Context
}

func (r *RoleResolver) Id() gql.ID            { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *RoleResolver) CreatedAt() *string    { return util.FormatTime(r.M.CreatedAt) }
func (r *RoleResolver) UpdatedAt() *string    { return util.FormatTime(r.M.UpdatedAt) }
func (r *RoleResolver) Token() string         { return r.M.Token }
func (r *RoleResolver) Name() *string         { return util.NullStr(r.M.Name) }
func (r *RoleResolver) Description() *string  { return util.NullStr(r.M.Description) }
func (r *RoleResolver) Authorities() []string { return r.M.Authorities }

// --------------------------
// User resolver
// --------------------------

type UserResolver struct {
	M model.User
	C context.Context
}

func (r *UserResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *UserResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }
func (r *UserResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }
func (r *UserResolver) Username() string   { return r.M.Username }
func (r *UserResolver) Enabled() bool      { return r.M.Enabled }

func (r *UserResolver) Email() *string     { return optStr(r.M.Email) }
func (r *UserResolver) FirstName() *string { return optStr(r.M.FirstName) }
func (r *UserResolver) LastName() *string  { return optStr(r.M.LastName) }

func (r *UserResolver) Roles() []*RoleResolver {
	resolvers := make([]*RoleResolver, 0, len(r.M.Roles))
	for _, role := range r.M.Roles {
		resolvers = append(resolvers, &RoleResolver{M: role, C: r.C})
	}
	return resolvers
}

// optStr maps an empty string column to a null GraphQL field.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
