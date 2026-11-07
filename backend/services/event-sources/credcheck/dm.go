// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credcheck

import "context"

// GraphQLClient is the slice of svcclient.Client the backend needs.
type GraphQLClient interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// checkQuery is device-management's checkDeviceCredential. The field names are pinned to the
// schema, and graphql-go rejects an unknown one, so a drift between the two fails the call
// loudly (which the verifier reads as UNKNOWN and counts as an error).
const checkQuery = `query($device: String!, $type: String!, $id: String!, $secret: String) {
  checkDeviceCredential(device: $device, credentialType: $type, credentialId: $id, secret: $secret)
}`

// DM is the Backend over device-management's GraphQL endpoint, on the service plane.
type DM struct {
	client GraphQLClient
	url    string
}

// NewDM binds a backend to a service-token client and device-management's GraphQL URL.
func NewDM(client GraphQLClient, graphqlURL string) *DM { return &DM{client: client, url: graphqlURL} }

// Check asks whether the credential would authenticate.
func (d *DM) Check(ctx context.Context, tenant string, p Presented) (bool, error) {
	vars := map[string]any{"device": p.Device, "type": p.Type, "id": p.Id}
	if p.Secret != "" {
		vars["secret"] = p.Secret
	}
	var out struct {
		Accepted bool `json:"checkDeviceCredential"`
	}
	if err := d.client.Query(ctx, d.url, tenant, checkQuery, vars, &out); err != nil {
		return false, err
	}
	return out.Accepted, nil
}
