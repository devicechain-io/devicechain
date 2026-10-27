// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"fmt"
)

// Outcome is what device-management concluded about a sender-named device.
type Outcome int

const (
	// Resolved: the alias names a device whose type is in the integration's scope.
	Resolved Outcome = iota + 1
	// Unregistered: no device in scope carries the alias (a sighting was recorded).
	Unregistered
	// Disabled: the token matched an integration an operator switched off. It is not a failed
	// credential.
	Disabled
	// Unauthenticated: no such integration, or the token did not match.
	Unauthenticated
)

// Resolution is device-management's answer to resolveIntegrationDevice.
type Resolution struct {
	Outcome         Outcome
	DeviceToken     string
	DeviceTypeToken string
	// Overflow: the sighting was not stored because the integration already records the most it
	// may.
	Overflow bool
}

// Backend is the device-management side of an integration, over the service plane. Every method
// fails closed: an error says nothing about the sender's credential, and a caller must not read
// it as a rejection or as an acceptance.
type Backend interface {
	// Auth proves a bearer token belongs to the integration. A nil descriptor with a nil error is
	// the answer to both an unknown integration and a wrong token.
	Auth(ctx context.Context, tenant, integration, bearer string) (*Descriptor, error)
	// Resolve turns the sender's identifier into a device.
	Resolve(ctx context.Context, tenant, integration, bearer, scheme, value string) (Resolution, error)
	// RecordUndecoded adds to the integration's undecoded tally.
	RecordUndecoded(ctx context.Context, tenant, integration, bearer string, count int, sampleHex string) error
	// RecordAuthFailure stamps the time the integration last failed authentication.
	RecordAuthFailure(ctx context.Context, tenant, integration string) error
}

// GraphQLClient is the slice of svcclient.Client the backend needs.
type GraphQLClient interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// The device-management operations. Field names are pinned to the schema, and graphql-go
// rejects an unknown one, so a drift between the two fails the call loudly.
const (
	authQuery = `query($token: String!, $secret: String!) {
  integrationAuth(token: $token, secret: $secret) { token type enabled identity generic linkMetrics }
}`
	resolveMutation = `mutation($integration: String!, $secret: String!, $scheme: String!, $value: String!) {
  resolveIntegrationDevice(integration: $integration, secret: $secret, scheme: $scheme, value: $value) {
    outcome deviceToken deviceTypeToken overflow
  }
}`
	undecodedMutation = `mutation($integration: String!, $secret: String!, $count: Int!, $sample: String) {
  recordIntegrationUndecoded(integration: $integration, secret: $secret, count: $count, sample: $sample)
}`
	authFailureMutation = `mutation($integration: String!) {
  recordIntegrationAuthFailure(integration: $integration)
}`
)

// DM is the Backend over device-management's GraphQL endpoint.
type DM struct {
	client GraphQLClient
	url    string
}

// NewDM binds a backend to a service-token client and device-management's GraphQL URL.
func NewDM(client GraphQLClient, graphqlURL string) *DM { return &DM{client: client, url: graphqlURL} }

func (d *DM) Auth(ctx context.Context, tenant, integration, bearer string) (*Descriptor, error) {
	var out struct {
		IntegrationAuth *Descriptor `json:"integrationAuth"`
	}
	vars := map[string]any{"token": integration, "secret": bearer}
	if err := d.client.Query(ctx, d.url, tenant, authQuery, vars, &out); err != nil {
		return nil, err
	}
	if out.IntegrationAuth == nil {
		return nil, nil
	}
	if err := out.IntegrationAuth.Prepare(); err != nil {
		return nil, err
	}
	return out.IntegrationAuth, nil
}

func (d *DM) Resolve(ctx context.Context, tenant, integration, bearer, scheme, value string) (Resolution, error) {
	var out struct {
		Resolve *struct {
			Outcome         string  `json:"outcome"`
			DeviceToken     *string `json:"deviceToken"`
			DeviceTypeToken *string `json:"deviceTypeToken"`
			Overflow        bool    `json:"overflow"`
		} `json:"resolveIntegrationDevice"`
	}
	vars := map[string]any{"integration": integration, "secret": bearer, "scheme": scheme, "value": value}
	if err := d.client.Query(ctx, d.url, tenant, resolveMutation, vars, &out); err != nil {
		return Resolution{}, err
	}
	if out.Resolve == nil {
		return Resolution{}, fmt.Errorf("device-management's answer carries no resolution")
	}
	res := Resolution{Overflow: out.Resolve.Overflow}
	switch out.Resolve.Outcome {
	case "RESOLVED":
		if out.Resolve.DeviceToken == nil || *out.Resolve.DeviceToken == "" {
			return Resolution{}, fmt.Errorf("device-management resolved a device and named none")
		}
		res.Outcome, res.DeviceToken = Resolved, *out.Resolve.DeviceToken
		if out.Resolve.DeviceTypeToken != nil {
			res.DeviceTypeToken = *out.Resolve.DeviceTypeToken
		}
	case "UNREGISTERED":
		res.Outcome = Unregistered
	case "DISABLED":
		res.Outcome = Disabled
	case "UNAUTHENTICATED":
		res.Outcome = Unauthenticated
	default:
		// An outcome added later is not one this build can act on, and guessing at it is how a
		// refusal becomes an acceptance.
		return Resolution{}, fmt.Errorf("device-management answered an unknown outcome %q", out.Resolve.Outcome)
	}
	return res, nil
}

func (d *DM) RecordUndecoded(ctx context.Context, tenant, integration, bearer string, count int, sampleHex string) error {
	vars := map[string]any{"integration": integration, "secret": bearer, "count": count}
	if sampleHex != "" {
		vars["sample"] = sampleHex
	}
	var out struct{}
	return d.client.Query(ctx, d.url, tenant, undecodedMutation, vars, &out)
}

func (d *DM) RecordAuthFailure(ctx context.Context, tenant, integration string) error {
	var out struct{}
	return d.client.Query(ctx, d.url, tenant, authFailureMutation, map[string]any{"integration": integration}, &out)
}
