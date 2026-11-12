// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package automationreview implements DM's narrow comparison seam over svcclient.
package automationreview

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

type Client struct {
	client *svcclient.Client
	url    string
}

func NewClient(client *svcclient.Client, graphqlURL string) *Client {
	return &Client{client: client, url: graphqlURL}
}

var _ model.RuleComparer = (*Client)(nil)

// All evidence fields are selected, including the canonical result string; parsing
// and re-encoding that string would destroy the engine's byte-exact result digest.
const compareQuery = `query($input: CompareRuleVersionsInput!) {
  compareRuleVersions(input: $input) {
    ok refusal { reason message } outcome
    fidelity { status reasons label }
    expectations { index name window verdict baseline reason baselineReason }
    windows {
      role start end leadInStart stream firstSeq lastSeq inputDigest degraded
      counts: changeCounts { added removed shifted contentChanged }
      changesTruncated
    }
    inputDigest engineBuild result resultDigest
  }
}`

func (c *Client) CompareRuleVersions(ctx context.Context, req model.CompareRequest) (*model.CompareResponse, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, fmt.Errorf("%w: no tenant in context", model.ErrReviewUnavailable)
	}
	// A pointer plus a nullable ok flag distinguishes a legitimate refusal from
	// missing/null/empty data on an older or malformed peer.
	var out struct {
		CompareRuleVersions *struct {
			model.CompareResponse
			Ok      *bool `json:"ok"`
			Refusal *struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"refusal"`
		} `json:"compareRuleVersions"`
	}
	if err := c.client.Query(ctx, c.url, tenant, compareQuery, map[string]any{"input": req}, &out); err != nil {
		return nil, fmt.Errorf("%w: compareRuleVersions: %v", model.ErrReviewUnavailable, err)
	}
	r := out.CompareRuleVersions
	if r == nil || r.Ok == nil {
		return nil, fmt.Errorf("%w: empty comparison answer", model.ErrReviewUnavailable)
	}
	res := r.CompareResponse
	res.Ok = *r.Ok
	if !res.Ok {
		if r.Refusal == nil || r.Refusal.Reason == "" {
			return nil, fmt.Errorf("%w: empty refusal", model.ErrReviewUnavailable)
		}
		res.RefusalReason, res.RefusalMessage = r.Refusal.Reason, r.Refusal.Message
	} else if !json.Valid([]byte(res.Result)) || res.ResultDigest == "" || res.Outcome == "" {
		return nil, fmt.Errorf("%w: incomplete comparison answer", model.ErrReviewUnavailable)
	}
	return &res, nil
}
