// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"
	"math"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// A solar campaign trigger is computed for a device's INSTALLED position, the one
// device-management records with setInstalledPosition (or a fleet import). It is deliberately
// not the last reported fix: a street light does not move, and a light whose GNSS module is
// faulty must not dim at the wrong dusk. Reading it needs location:installed:read, which the
// service token this resolver's client carries must request.

// siteLookupChunk is the most tokens device-management answers in one devicesByToken call.
const siteLookupChunk = 1000

// SiteResolver reads devices' installed locations from device-management, the owner of the
// device record. It satisfies model.SiteResolver.
type SiteResolver struct {
	client *svcclient.Client
	url    string
}

// NewSiteResolver binds a resolver to a service client and device-management's GraphQL
// endpoint URL.
func NewSiteResolver(client *svcclient.Client, graphqlURL string) *SiteResolver {
	return &SiteResolver{client: client, url: graphqlURL}
}

const devicesByTokenSites = `query($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) {
    token
    installedPosition { latitude longitude }
  }
}`

// DeviceSites returns the installed location of each named device that has a valid one. A
// device with no installed position, or coordinates off the globe is simply
// absent: the caller records it as having no site, never as being somewhere.
func (r *SiteResolver) DeviceSites(ctx context.Context, tokens []string) (map[string]model.Site, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verify: no tenant in context")
	}
	sites := make(map[string]model.Site, len(tokens))
	for start := 0; start < len(tokens); start += siteLookupChunk {
		end := min(start+siteLookupChunk, len(tokens))
		var out struct {
			DevicesByToken *[]struct {
				Token             string `json:"token"`
				InstalledPosition *struct {
					Latitude  *float64 `json:"latitude"`
					Longitude *float64 `json:"longitude"`
				} `json:"installedPosition"`
			} `json:"devicesByToken"`
		}
		if err := r.client.Query(ctx, r.url, tenant, devicesByTokenSites,
			map[string]any{"tokens": tokens[start:end]}, &out); err != nil {
			return nil, err
		}
		if out.DevicesByToken == nil {
			return nil, fmt.Errorf("verify: device-management returned no devices")
		}
		for _, d := range *out.DevicesByToken {
			if site, ok := siteOf(d.InstalledPosition); ok {
				sites[d.Token] = site
			}
		}
	}
	return sites, nil
}

// siteOf turns an installed position into a site. A device with none, or coordinates off the
// globe (device-management refuses those on write, so this is a second line), has no site.
func siteOf(p *struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}) (model.Site, bool) {
	if p == nil || p.Latitude == nil || p.Longitude == nil {
		return model.Site{}, false
	}
	lat, lon := *p.Latitude, *p.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return model.Site{}, false
	}
	return model.Site{Latitude: lat, Longitude: lon}, true
}
