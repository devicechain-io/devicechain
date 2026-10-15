// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// GraphQLPlatform reads stored history through event-management's tenant GraphQL API,
// signed in as the sim's own tenant identity.
type GraphQLPlatform struct {
	Session  *userclient.TenantSession
	EventURL string
}

// historyPageSize is the most event-management returns in one page.
const historyPageSize = 1000

const measurementsQuery = `query($c: EventSearchCriteria!) {
  measurementEvents(criteria: $c) {
    results { deviceToken occurredTime name value }
    pagination { pageEnd totalRecords }
  }
}`

const locationsQuery = `query($c: EventSearchCriteria!) {
  locationEvents(criteria: $c) {
    results { deviceToken occurredTime latitude longitude heading speed }
    pagination { pageEnd totalRecords }
  }
}`

func criteria(device string, from, to time.Time, page int) map[string]any {
	return map[string]any{"c": map[string]any{
		"pageNumber":  page,
		"pageSize":    historyPageSize,
		"deviceToken": device,
		"startTime":   from.UTC().Format(time.RFC3339Nano),
		"endTime":     to.UTC().Format(time.RFC3339Nano),
	}}
}

type pagination struct {
	PageEnd      int `json:"pageEnd"`
	TotalRecords int `json:"totalRecords"`
}

// Measurements reads every stored measurement of one device in [from, to], all pages. The
// total is read on every page and must not move: a window still being written to would
// shift the pages under the reader and could skip or repeat a row.
func (g *GraphQLPlatform) Measurements(ctx context.Context, device string, from, to time.Time) ([]Row, error) {
	var out []Row
	total := -1
	for page := 1; ; page++ {
		var res struct {
			MeasurementEvents struct {
				Results []struct {
					DeviceToken  string   `json:"deviceToken"`
					OccurredTime string   `json:"occurredTime"`
					Name         string   `json:"name"`
					Value        *float64 `json:"value"`
				} `json:"results"`
				Pagination pagination `json:"pagination"`
			} `json:"measurementEvents"`
		}
		if err := g.Session.Query(ctx, g.EventURL, measurementsQuery, criteria(device, from, to, page), &res); err != nil {
			return nil, fmt.Errorf("read %s measurements: %w", device, err)
		}
		pg := res.MeasurementEvents.Pagination
		if total >= 0 && pg.TotalRecords != total {
			return nil, refuse("%s's measurements changed while they were read (%d, then %d): read a window that has closed", device, total, pg.TotalRecords)
		}
		total = pg.TotalRecords
		for _, r := range res.MeasurementEvents.Results {
			at, err := time.Parse(time.RFC3339Nano, r.OccurredTime)
			if err != nil || r.Value == nil {
				return nil, refuse("a stored %s reading has no time or value", device)
			}
			out = append(out, Row{Device: r.DeviceToken, Name: r.Name, At: at, Value: *r.Value})
		}
		if pg.PageEnd >= total || len(res.MeasurementEvents.Results) == 0 {
			break
		}
	}
	if len(out) != total {
		return nil, refuse("read %d of %s's %d measurements", len(out), device, total)
	}
	return out, nil
}

// Locations reads every stored position of one device in [from, to], all pages.
func (g *GraphQLPlatform) Locations(ctx context.Context, device string, from, to time.Time) ([]Fix, error) {
	var out []Fix
	total := -1
	for page := 1; ; page++ {
		var res struct {
			LocationEvents struct {
				Results []struct {
					DeviceToken  string   `json:"deviceToken"`
					OccurredTime string   `json:"occurredTime"`
					Latitude     *float64 `json:"latitude"`
					Longitude    *float64 `json:"longitude"`
					Heading      *float64 `json:"heading"`
					Speed        *float64 `json:"speed"`
				} `json:"results"`
				Pagination pagination `json:"pagination"`
			} `json:"locationEvents"`
		}
		if err := g.Session.Query(ctx, g.EventURL, locationsQuery, criteria(device, from, to, page), &res); err != nil {
			return nil, fmt.Errorf("read %s positions: %w", device, err)
		}
		pg := res.LocationEvents.Pagination
		if total >= 0 && pg.TotalRecords != total {
			return nil, refuse("%s's positions changed while they were read: read a window that has closed", device)
		}
		total = pg.TotalRecords
		for _, r := range res.LocationEvents.Results {
			at, err := time.Parse(time.RFC3339Nano, r.OccurredTime)
			if err != nil || r.Latitude == nil || r.Longitude == nil {
				return nil, refuse("a stored %s position has no time or coordinates", device)
			}
			out = append(out, Fix{Device: r.DeviceToken, At: at, Latitude: *r.Latitude, Longitude: *r.Longitude, Heading: r.Heading, Speed: r.Speed})
		}
		if pg.PageEnd >= total || len(res.LocationEvents.Results) == 0 {
			break
		}
	}
	if len(out) != total {
		return nil, refuse("read %d of %s's %d positions", len(out), device, total)
	}
	return out, nil
}
