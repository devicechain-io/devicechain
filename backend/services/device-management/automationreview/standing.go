// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package automationreview

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

type StandingClient struct {
	client *svcclient.Client
	url    string
}

func NewStandingClient(client *svcclient.Client, url string) *StandingClient {
	return &StandingClient{client: client, url: url}
}

var _ model.PrincipalStandingChecker = (*StandingClient)(nil)

const principalStandingQuery = `query($principals:[String!]!,$authorities:[String!]!){
 principalStanding(principals:$principals, authorities:$authorities){ principal qualifies }
}`

func (c *StandingClient) PrincipalStanding(ctx context.Context, principals, authorities []string) (map[string]bool, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, fmt.Errorf("principal standing requires a tenant")
	}
	var out struct {
		PrincipalStanding *[]struct {
			Principal string `json:"principal"`
			Qualifies *bool  `json:"qualifies"`
		} `json:"principalStanding"`
	}
	if err := c.client.Query(ctx, c.url, tenant, principalStandingQuery, map[string]any{"principals": principals, "authorities": authorities}, &out); err != nil {
		return nil, err
	}
	if out.PrincipalStanding == nil {
		return nil, fmt.Errorf("principal standing answer is missing")
	}
	expected := map[string]bool{}
	for _, sub := range principals {
		expected[sub] = true
	}
	result := map[string]bool{}
	for _, row := range *out.PrincipalStanding {
		if !expected[row.Principal] || row.Qualifies == nil {
			return nil, fmt.Errorf("principal standing answer is malformed")
		}
		if _, seen := result[row.Principal]; seen {
			return nil, fmt.Errorf("principal standing answer contains a duplicate principal")
		}
		result[row.Principal] = *row.Qualifies
	}
	if len(result) != len(expected) {
		return nil, fmt.Errorf("principal standing answer is incomplete")
	}
	return result, nil
}
