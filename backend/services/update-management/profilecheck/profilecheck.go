// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package profilecheck confirms device-profile tokens against device-management, over a
// service token scoped to device:read and the caller's tenant.
package profilecheck

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// Querier is the slice of svcclient.Client this package uses.
type Querier interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// Checker implements model.ProfileChecker.
type Checker struct {
	client Querier
	url    string
}

var _ Querier = (*svcclient.Client)(nil)

// New builds a checker that asks the device-management GraphQL endpoint at graphqlURL.
func New(client Querier, graphqlURL string) *Checker {
	return &Checker{client: client, url: graphqlURL}
}

const profilesByToken = `query($tokens: [String!]!) { deviceProfilesByToken(tokens: $tokens) { token } }`

// Missing returns the requested tokens that are not device profiles in the tenant. An
// error means the answer could not be obtained; it is never reported as "none missing".
func (c *Checker) Missing(ctx context.Context, tenant string, tokens []string) ([]string, error) {
	var out struct {
		DeviceProfilesByToken []struct {
			Token string `json:"token"`
		} `json:"deviceProfilesByToken"`
	}
	if err := c.client.Query(ctx, c.url, tenant, profilesByToken, map[string]any{"tokens": tokens}, &out); err != nil {
		return nil, fmt.Errorf("device-management profile lookup: %w", err)
	}
	found := make(map[string]bool, len(out.DeviceProfilesByToken))
	for _, p := range out.DeviceProfilesByToken {
		found[p.Token] = true
	}
	var missing []string
	for _, t := range tokens {
		if !found[t] {
			missing = append(missing, t)
		}
	}
	return missing, nil
}
