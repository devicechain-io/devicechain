// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Role is a tenant-scoped, named bundle of granted authorities (ADR-008 RBAC).
// A user's effective capabilities are the union of their roles' authorities,
// expanded into the JWT at login/refresh; resolvers gate on those capabilities,
// not on role names, so a role can be re-scoped without code changes.
//
// Authorities is a JSON-encoded set of authority strings (auth.Authority values)
// stored in a single column via GORM's json serializer — a role is just a name
// plus the capabilities it grants, and the authority vocabulary lives in code
// (core/auth), so no separate authority table is needed. Each authority is
// validated against that vocabulary on write.
type Role struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity

	Authorities []string `gorm:"serializer:json"`
}

// RoleCreateRequest is the data required to create or update a role.
type RoleCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Authorities []string
}

// UserCreateRequest is the data required to create a user.
type UserCreateRequest struct {
	Username  string
	Password  string
	Email     *string
	FirstName *string
	LastName  *string
	Enabled   bool
}
