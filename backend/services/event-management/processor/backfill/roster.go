// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// RosterEntry is one device of the profile being filled.
type RosterEntry struct {
	DeviceId  int64
	Token     string
	CreatedAt time.Time
	// Constants are the numbers the device's metadata holds under the plan's constant keys: a
	// usable number, or the "present but unusable" kind the formulas skip as invalid_input. A
	// device holding none has an empty slice, and a formula reading one finds it absent.
	Constants []calcfield.Constant
}

// RosterPage is one keyset page of the roster.
type RosterPage struct {
	Entries []RosterEntry
	// NextCursor is the id to pass as afterId for the next page; 0 when the walk is complete.
	NextCursor int64
	// Total is how many devices the whole walk covers; set on the first page only.
	Total *int64
}

// Roster is where the devices of a profile come from. The worker walks it in keyset pages and
// never holds more than one.
type Roster interface {
	// Page returns up to limit devices of the profile after afterId (0 for the first page). A
	// non-empty deviceTokens narrows the walk to those devices.
	Page(ctx context.Context, tenant, profileToken string, constantKeys, deviceTokens []string,
		afterId int64, limit int) (*RosterPage, error)
}

// rosterQuery is the document sent to device-management's calculatedBackfillRoster door. It
// is validated against that service's served schema by a test, since the compiler does not
// read it.
const rosterQuery = `query CalculatedBackfillRoster($profileToken: String!, $constantKeys: [String!]!, ` +
	`$deviceTokens: [String!], $afterId: String, $limit: Int!) {
  calculatedBackfillRoster(profileToken: $profileToken, constantKeys: $constantKeys, ` +
	`deviceTokens: $deviceTokens, afterId: $afterId, limit: $limit) {
    entries { deviceId deviceToken createdAt constants { key number } }
    nextCursor
    total
  }
}`

// GraphQLRoster is the Roster over device-management's GraphQL door, called with a service
// token that holds device:read. It acts for no user: a backfill is the platform filling
// history, and the person who started it was authorized when the job was created.
type GraphQLRoster struct {
	client *svcclient.Client
	url    string
}

// NewGraphQLRoster returns the roster reading from the device-management GraphQL endpoint at
// url, through client.
func NewGraphQLRoster(client *svcclient.Client, url string) *GraphQLRoster {
	return &GraphQLRoster{client: client, url: url}
}

type rosterResponse struct {
	CalculatedBackfillRoster struct {
		Entries []struct {
			DeviceId    string `json:"deviceId"`
			DeviceToken string `json:"deviceToken"`
			CreatedAt   string `json:"createdAt"`
			Constants   []struct {
				Key    string   `json:"key"`
				Number *float64 `json:"number"`
			} `json:"constants"`
		} `json:"entries"`
		NextCursor *string  `json:"nextCursor"`
		Total      *float64 `json:"total"`
	} `json:"calculatedBackfillRoster"`
}

// Page implements Roster.
func (r *GraphQLRoster) Page(ctx context.Context, tenant, profileToken string, constantKeys, deviceTokens []string,
	afterId int64, limit int) (*RosterPage, error) {
	vars := map[string]any{
		"profileToken": profileToken,
		"constantKeys": nonNil(constantKeys),
		"limit":        limit,
	}
	if len(deviceTokens) > 0 {
		vars["deviceTokens"] = deviceTokens
	}
	if afterId > 0 {
		vars["afterId"] = strconv.FormatInt(afterId, 10)
	}
	var out rosterResponse
	if err := r.client.Query(ctx, r.url, tenant, rosterQuery, vars, &out); err != nil {
		return nil, err
	}
	src := out.CalculatedBackfillRoster
	page := &RosterPage{Entries: make([]RosterEntry, 0, len(src.Entries))}
	for _, e := range src.Entries {
		id, err := strconv.ParseInt(e.DeviceId, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("the roster named a device id %q that is not a number: %w", e.DeviceId, err)
		}
		created, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("the roster gave device %q a creation time %q that is not RFC 3339: %w",
				e.DeviceToken, e.CreatedAt, err)
		}
		entry := RosterEntry{DeviceId: id, Token: e.DeviceToken, CreatedAt: created.UTC()}
		for _, c := range e.Constants {
			if c.Number == nil {
				entry.Constants = append(entry.Constants, calcfield.InvalidConstant(c.Key))
			} else {
				entry.Constants = append(entry.Constants, calcfield.NewConstant(c.Key, *c.Number))
			}
		}
		page.Entries = append(page.Entries, entry)
	}
	if src.NextCursor != nil {
		next, err := strconv.ParseInt(*src.NextCursor, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("the roster gave a next cursor %q that is not a number: %w", *src.NextCursor, err)
		}
		page.NextCursor = next
	}
	if src.Total != nil {
		t := int64(*src.Total)
		page.Total = &t
	}
	return page, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
