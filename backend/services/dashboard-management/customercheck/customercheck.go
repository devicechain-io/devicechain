// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package customercheck confirms customer tokens against device-management, which owns
// customers, over a service token scoped to device:read and the caller's tenant.
package customercheck

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// Querier is the slice of svcclient.Client this package uses.
type Querier interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

var _ Querier = (*svcclient.Client)(nil)

// Checker implements model.CustomerChecker.
type Checker struct {
	client Querier
	url    string
}

// New builds a checker that asks the device-management GraphQL endpoint at graphqlURL.
func New(client Querier, graphqlURL string) *Checker {
	return &Checker{client: client, url: graphqlURL}
}

const customersByToken = `query($tokens: [String!]!) { customersByToken(tokens: $tokens) { token } }`

// Missing returns the requested tokens that are not customers in the tenant, in request
// order. An error means the answer could not be obtained; it is never reported as "none
// missing". An answer that omits the list is such an error: read as empty it would report
// every customer missing, and read the other way it would confirm customers nobody checked.
func (c *Checker) Missing(ctx context.Context, tenant string, tokens []string) ([]string, error) {
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
	var missing []string
	for _, t := range tokens {
		if !found[t] {
			missing = append(missing, t)
		}
	}
	return missing, nil
}
