// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package sharecheck confirms the customers and groups a dashboard share names against
// device-management, which owns both, over a service token scoped to device:read and the
// caller's tenant.
package sharecheck

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// Querier is the slice of svcclient.Client this package uses.
type Querier interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

var _ Querier = (*svcclient.Client)(nil)

// Checker implements model.ShareChecker.
type Checker struct {
	client Querier
	url    string
}

// New builds a checker that asks the device-management GraphQL endpoint at graphqlURL.
func New(client Querier, graphqlURL string) *Checker {
	return &Checker{client: client, url: graphqlURL}
}

const customersByToken = `query($tokens: [String!]!) { customersByToken(tokens: $tokens) { token } }`

const groupsByToken = `query($tokens: [String!]!) { entityGroupsByToken(tokens: $tokens) { token accessScope } }`

// Missing returns the requested tokens of the anchor kind that name nothing a dashboard can be
// shared with in the tenant, in request order: for a customer, one that does not exist; for a
// group, one that does not exist or is not an access scope. A group that is not an access scope
// is missing because no member's access follows it, so a share with it would sit as a promise
// nobody could see — and the author learns no more about it than about a group that is not there.
//
// An error means the answer could not be obtained; it is never reported as "none missing". An
// answer that omits the list is such an error: read as empty it would report every token
// missing, and read the other way it would confirm tokens nobody checked.
func (c *Checker) Missing(ctx context.Context, tenant, kind string, tokens []string) ([]string, error) {
	var found map[string]bool
	var err error
	switch kind {
	case auth.AnchorKindCustomer:
		found, err = c.customers(ctx, tenant, tokens)
	case auth.AnchorKindGroup:
		found, err = c.accessGroups(ctx, tenant, tokens)
	default:
		return nil, fmt.Errorf("device-management lookup: no lookup for anchor kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, t := range tokens {
		if !found[t] {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

func (c *Checker) customers(ctx context.Context, tenant string, tokens []string) (map[string]bool, error) {
	var out struct {
		CustomersByToken *[]struct {
			Token string `json:"token"`
		} `json:"customersByToken"`
	}
	if err := c.client.Query(ctx, c.url, tenant, customersByToken, map[string]any{"tokens": tokens}, &out); err != nil {
		return nil, fmt.Errorf("device-management customer lookup: %w", err)
	}
	if out.CustomersByToken == nil {
		return nil, fmt.Errorf("device-management customer lookup: the answer carries no customers")
	}
	found := make(map[string]bool, len(*out.CustomersByToken))
	for _, c := range *out.CustomersByToken {
		found[c.Token] = true
	}
	return found, nil
}

func (c *Checker) accessGroups(ctx context.Context, tenant string, tokens []string) (map[string]bool, error) {
	var out struct {
		EntityGroupsByToken *[]struct {
			Token string `json:"token"`
			// A pointer, so an answer that leaves the flag out is an error rather than read
			// as a value nobody sent.
			AccessScope *bool `json:"accessScope"`
		} `json:"entityGroupsByToken"`
	}
	if err := c.client.Query(ctx, c.url, tenant, groupsByToken, map[string]any{"tokens": tokens}, &out); err != nil {
		return nil, fmt.Errorf("device-management group lookup: %w", err)
	}
	if out.EntityGroupsByToken == nil {
		return nil, fmt.Errorf("device-management group lookup: the answer carries no groups")
	}
	found := make(map[string]bool, len(*out.EntityGroupsByToken))
	for _, g := range *out.EntityGroupsByToken {
		if g.AccessScope == nil {
			return nil, fmt.Errorf("device-management group lookup: group %q carries no access-scope flag", g.Token)
		}
		if *g.AccessScope {
			found[g.Token] = true
		}
	}
	return found, nil
}
