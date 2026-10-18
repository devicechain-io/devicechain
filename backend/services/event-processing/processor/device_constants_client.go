// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// deviceConstantsPageSize is how many devices one page of the constants door scans. The door
// allows 500; a page lists at most that many devices with up to 32 numbers each, and this keeps
// the worst case a fraction of the service client's response cap.
const deviceConstantsPageSize = 200

const deviceConstantsPageQuery = `query($profileToken: String!, $keys: [String!]!, $afterId: String, $limit: Int!) {
  deviceConstantsPage(profileToken: $profileToken, keys: $keys, afterId: $afterId, limit: $limit) {
    entries { deviceToken constants { key number } }
    nextCursor
  }
}`

type deviceConstantsPageResponse struct {
	DeviceConstantsPage struct {
		Entries []struct {
			DeviceToken string `json:"deviceToken"`
			Constants   []struct {
				Key    string   `json:"key"`
				Number *float64 `json:"number"`
			} `json:"constants"`
		} `json:"entries"`
		NextCursor *string `json:"nextCursor"`
	} `json:"deviceConstantsPage"`
}

// deviceConstantsClient implements preview.ConstantSource over device-management's constants
// door, with a service token carrying device:read: the same authority the preview's caller
// needed to ask, reading the tenant's own devices (the tenant rides the token's header).
type deviceConstantsClient struct {
	client *svcclient.Client
	url    string
	// exec, when non-nil, carries a query INSTEAD of the HTTP call, so a test can drive the
	// paging and decoding against device-management's real schema in-process.
	exec factExec
}

// NewDeviceConstantsClient builds the preview's per-device constants source.
func NewDeviceConstantsClient(client *svcclient.Client, dmURL string) preview.ConstantSource {
	return &deviceConstantsClient{client: client, url: dmURL}
}

func (c *deviceConstantsClient) query(ctx context.Context, tenant string, vars map[string]any, out any) error {
	if c.exec != nil {
		return c.exec(ctx, factDeviceManagement, tenant, deviceConstantsPageQuery, vars, out)
	}
	return c.client.Query(ctx, c.url, tenant, deviceConstantsPageQuery, vars, out)
}

// DeviceConstants walks the constants door for the profile's devices until the walk ends or
// maxDevices devices have been scanned, whichever is first. It returns what was read, or an
// error and nothing: a partial walk that failed midway would be a fleet with some calibrations
// and not others, indistinguishable from a real one.
func (c *deviceConstantsClient) DeviceConstants(ctx context.Context, tenant, profileToken string, keys []string,
	maxDevices int) (map[string][]calcfield.Constant, bool, error) {
	byDevice := map[string][]calcfield.Constant{}
	var cursor *string
	for scanned := 0; ; scanned += deviceConstantsPageSize {
		if scanned >= maxDevices {
			return byDevice, true, nil
		}
		var out deviceConstantsPageResponse
		err := c.query(ctx, tenant, map[string]any{"profileToken": profileToken, "keys": keys,
			"afterId": cursor, "limit": deviceConstantsPageSize}, &out)
		if err != nil {
			if peerLacksQuery(err, "deviceConstantsPage") {
				return nil, false, fmt.Errorf("%w: device-management does not serve the device constants read (it is "+
					"running an older build): %v", errFactsSkew, err)
			}
			return nil, false, fmt.Errorf("reading device constants for the preview: %w", err)
		}
		for _, e := range out.DeviceConstantsPage.Entries {
			cs := make([]calcfield.Constant, 0, len(e.Constants))
			for _, k := range e.Constants {
				if k.Number != nil {
					cs = append(cs, calcfield.NewConstant(k.Key, *k.Number))
				} else {
					cs = append(cs, calcfield.InvalidConstant(k.Key))
				}
			}
			byDevice[e.DeviceToken] = cs
		}
		if out.DeviceConstantsPage.NextCursor == nil {
			return byDevice, false, nil
		}
		cursor = out.DeviceConstantsPage.NextCursor
	}
}
