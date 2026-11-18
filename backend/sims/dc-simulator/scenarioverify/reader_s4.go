// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// The documents the verifier sends for the S4 beats: groups, campaigns, the maintenance window's
// hold, installed positions, the asset hierarchy and the typed relationship graph. Each is
// validated against its served schema by documents_test.go.
const (
	queryGroupTargets = `query ScenarioGroupTargets($group: String!, $after: String, $limit: Int!) {
  resolveDeviceGroupTargets(groupToken: $group, afterId: $after, limit: $limit) { rejected code reason deviceTokens nextCursor }
}`

	queryCampaign = `query ScenarioCampaign($token: String!) {
  campaign(token: $token) { token groupToken trigger createdAt plannedThrough }
}`

	queryCampaignCycles = `query ScenarioCampaignCycles($token: String!, $limit: Int) {
  campaignCycles(campaignToken: $token, limit: $limit) { key day at resolved }
}`

	queryCampaignElements = `query ScenarioCampaignElements($token: String!, $cycle: String!, $after: ID, $limit: Int) {
  campaignElements(campaignToken: $token, cycleKey: $cycle, afterId: $after, limit: $limit) {
    id deviceToken step dueAt status reason batchToken commandStatus commandOutcome
  }
}`

	queryBatchCommands = `query ScenarioBatchCommands($c: CommandSearchCriteria!) {
  commands(criteria: $c) {
    results { deviceToken name status queuedTime sentTime respondedTime maintenanceWindowToken batchToken }
    pagination { totalRecords }
  }
}`

	queryPositions = `query ScenarioPositions($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) { token installedPosition { latitude longitude } }
}`

	queryAssetAncestors = `query ScenarioAncestors($token: String!) {
  assetAncestors(token: $token) { token }
}`

	queryAnchorDevices = `query ScenarioAnchorDevices($c: AnchorDeviceSearchCriteria!) {
  anchorDevices(criteria: $c) {
    results { token }
    pagination { totalRecords }
  }
}`

	queryRelationships = `query ScenarioRelationships($c: EntityRelationshipSearchCriteria!) {
  entityRelationships(criteria: $c) {
    results { target { token } }
    pagination { totalRecords }
  }
}`

	groupPageSize    = 1000
	cyclePageSize    = 200
	elementPageSize  = 1000
	anchorPageSize   = 1000
	relationPageSize = 1000
)

// Campaign is a campaign as the platform holds it.
type Campaign struct {
	Token          string    `json:"token"`
	GroupToken     string    `json:"groupToken"`
	Trigger        string    `json:"trigger"`
	CreatedAt      time.Time `json:"createdAt"`
	PlannedThrough time.Time `json:"plannedThrough"`
}

// Cycle is one firing of a campaign.
type Cycle struct {
	Key      string    `json:"key"`
	Day      string    `json:"day"`
	At       time.Time `json:"at"`
	Resolved int       `json:"resolved"`
}

// Element is one device's step in one cycle.
type Element struct {
	Device         string    `json:"device"`
	DueAt          time.Time `json:"dueAt"`
	Status         string    `json:"status"`
	Reason         string    `json:"reason"`
	BatchToken     string    `json:"batchToken"`
	CommandStatus  string    `json:"commandStatus"`
	CommandOutcome string    `json:"commandOutcome"`
}

// HeldCommand is a command with what decides whether a maintenance window held it.
type HeldCommand struct {
	Device     string    `json:"device"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Queued     time.Time `json:"queued"`
	Sent       time.Time `json:"sent"`
	Responded  time.Time `json:"responded"`
	Window     string    `json:"window"`
	BatchToken string    `json:"batchToken"`
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// GroupTargets is every device a group resolves to as a command target.
func (r *Reader) GroupTargets(ctx context.Context, group string) ([]string, error) {
	var out []string
	var after *string
	for page := 0; page < maxPages; page++ {
		var resp struct {
			ResolveDeviceGroupTargets struct {
				Rejected     bool     `json:"rejected"`
				Code         *string  `json:"code"`
				Reason       *string  `json:"reason"`
				DeviceTokens []string `json:"deviceTokens"`
				NextCursor   *string  `json:"nextCursor"`
			} `json:"resolveDeviceGroupTargets"`
		}
		vars := map[string]any{"group": group, "limit": groupPageSize}
		if after != nil {
			vars["after"] = *after
		}
		if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryGroupTargets, vars, &resp); err != nil {
			return nil, fmt.Errorf("resolve group %s: %w", group, err)
		}
		g := resp.ResolveDeviceGroupTargets
		if g.Rejected {
			return nil, fmt.Errorf("resolve group %s: rejected %s: %s", group, str(g.Code), str(g.Reason))
		}
		out = append(out, g.DeviceTokens...)
		if g.NextCursor == nil {
			return out, nil
		}
		after = g.NextCursor
	}
	return nil, fmt.Errorf("resolve group %s: more than %d pages", group, maxPages)
}

// CampaignByToken reads one campaign; nil when there is none.
func (r *Reader) CampaignByToken(ctx context.Context, token string) (*Campaign, error) {
	var resp struct {
		Campaign *struct {
			Token          string  `json:"token"`
			GroupToken     string  `json:"groupToken"`
			Trigger        string  `json:"trigger"`
			CreatedAt      *string `json:"createdAt"`
			PlannedThrough *string `json:"plannedThrough"`
		} `json:"campaign"`
	}
	if err := r.session.Query(ctx, r.endpoints.CommandMgmtGraphQL, queryCampaign, map[string]any{"token": token}, &resp); err != nil {
		return nil, fmt.Errorf("read campaign %s: %w", token, err)
	}
	c := resp.Campaign
	if c == nil {
		return nil, nil
	}
	created, err := parseTime(c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("campaign %s createdAt: %w", token, err)
	}
	through, err := parseTime(c.PlannedThrough)
	if err != nil {
		return nil, fmt.Errorf("campaign %s plannedThrough: %w", token, err)
	}
	return &Campaign{Token: c.Token, GroupToken: c.GroupToken, Trigger: c.Trigger, CreatedAt: created, PlannedThrough: through}, nil
}

// Cycles reads a campaign's cycles.
func (r *Reader) Cycles(ctx context.Context, campaign string) ([]Cycle, error) {
	var resp struct {
		CampaignCycles []struct {
			Key      string  `json:"key"`
			Day      *string `json:"day"`
			At       *string `json:"at"`
			Resolved int     `json:"resolved"`
		} `json:"campaignCycles"`
	}
	vars := map[string]any{"token": campaign, "limit": cyclePageSize}
	if err := r.session.Query(ctx, r.endpoints.CommandMgmtGraphQL, queryCampaignCycles, vars, &resp); err != nil {
		return nil, fmt.Errorf("read cycles of %s: %w", campaign, err)
	}
	out := make([]Cycle, 0, len(resp.CampaignCycles))
	for _, c := range resp.CampaignCycles {
		at, err := parseTime(c.At)
		if err != nil {
			return nil, fmt.Errorf("cycle %s of %s: at: %w", c.Key, campaign, err)
		}
		out = append(out, Cycle{Key: c.Key, Day: str(c.Day), At: at, Resolved: c.Resolved})
	}
	return out, nil
}

// Elements reads every element of one cycle.
func (r *Reader) Elements(ctx context.Context, campaign, cycle string) ([]Element, error) {
	var out []Element
	var after *string
	for page := 0; page < maxPages; page++ {
		var resp struct {
			CampaignElements []struct {
				ID             string  `json:"id"`
				DeviceToken    string  `json:"deviceToken"`
				DueAt          string  `json:"dueAt"`
				Status         string  `json:"status"`
				Reason         *string `json:"reason"`
				BatchToken     *string `json:"batchToken"`
				CommandStatus  *string `json:"commandStatus"`
				CommandOutcome *string `json:"commandOutcome"`
			} `json:"campaignElements"`
		}
		vars := map[string]any{"token": campaign, "cycle": cycle, "limit": elementPageSize}
		if after != nil {
			vars["after"] = *after
		}
		if err := r.session.Query(ctx, r.endpoints.CommandMgmtGraphQL, queryCampaignElements, vars, &resp); err != nil {
			return nil, fmt.Errorf("read elements of %s/%s: %w", campaign, cycle, err)
		}
		for _, e := range resp.CampaignElements {
			due, err := time.Parse(time.RFC3339Nano, e.DueAt)
			if err != nil {
				return nil, fmt.Errorf("element of %s/%s: dueAt %q: %w", campaign, cycle, e.DueAt, err)
			}
			out = append(out, Element{Device: e.DeviceToken, DueAt: due, Status: e.Status, Reason: str(e.Reason),
				BatchToken: str(e.BatchToken), CommandStatus: str(e.CommandStatus), CommandOutcome: str(e.CommandOutcome)})
		}
		if len(resp.CampaignElements) < elementPageSize {
			return out, nil
		}
		last := resp.CampaignElements[len(resp.CampaignElements)-1].ID
		after = &last
	}
	return nil, fmt.Errorf("read elements of %s/%s: more than %d pages", campaign, cycle, maxPages)
}

// BatchCommands reads every command a batch created.
func (r *Reader) BatchCommands(ctx context.Context, batch string) ([]HeldCommand, error) {
	var out []HeldCommand
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			Commands struct {
				Results []struct {
					DeviceToken            string  `json:"deviceToken"`
					Name                   string  `json:"name"`
					Status                 string  `json:"status"`
					QueuedTime             *string `json:"queuedTime"`
					SentTime               *string `json:"sentTime"`
					RespondedTime          *string `json:"respondedTime"`
					MaintenanceWindowToken *string `json:"maintenanceWindowToken"`
					BatchToken             *string `json:"batchToken"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"commands"`
		}
		vars := map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": commandPageSize, "batchToken": batch}}
		if err := r.session.Query(ctx, r.endpoints.CommandMgmtGraphQL, queryBatchCommands, vars, &resp); err != nil {
			return nil, fmt.Errorf("read commands of batch %s: %w", batch, err)
		}
		if resp.Commands.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("read commands of batch %s: the server returned a null totalRecords", batch)
		}
		for _, c := range resp.Commands.Results {
			h := HeldCommand{Device: c.DeviceToken, Name: c.Name, Status: c.Status, Window: str(c.MaintenanceWindowToken), BatchToken: str(c.BatchToken)}
			var err error
			if h.Queued, err = parseTime(c.QueuedTime); err == nil {
				if h.Sent, err = parseTime(c.SentTime); err == nil {
					h.Responded, err = parseTime(c.RespondedTime)
				}
			}
			if err != nil {
				return nil, fmt.Errorf("command of batch %s: %w", batch, err)
			}
			out = append(out, h)
		}
		if len(out) >= *resp.Commands.Pagination.TotalRecords || len(resp.Commands.Results) < commandPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("read commands of batch %s: more than %d pages", batch, maxPages)
}

// Positions reads the installed position of each device; a device with none is absent.
func (r *Reader) Positions(ctx context.Context, devices []string) (map[string]sim.PositionSpec, error) {
	var resp struct {
		DevicesByToken []struct {
			Token             string `json:"token"`
			InstalledPosition *struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"installedPosition"`
		} `json:"devicesByToken"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryPositions, map[string]any{"tokens": devices}, &resp); err != nil {
		return nil, fmt.Errorf("read installed positions: %w", err)
	}
	out := map[string]sim.PositionSpec{}
	for _, d := range resp.DevicesByToken {
		if p := d.InstalledPosition; p != nil {
			out[d.Token] = sim.PositionSpec{Latitude: p.Latitude, Longitude: p.Longitude}
		}
	}
	return out, nil
}

// Ancestors reads an asset's path to its root, nearest first.
func (r *Reader) Ancestors(ctx context.Context, asset string) ([]string, error) {
	var resp struct {
		AssetAncestors []struct {
			Token string `json:"token"`
		} `json:"assetAncestors"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryAssetAncestors, map[string]any{"token": asset}, &resp); err != nil {
		return nil, fmt.Errorf("read ancestors of %s: %w", asset, err)
	}
	out := make([]string, 0, len(resp.AssetAncestors))
	for _, a := range resp.AssetAncestors {
		out = append(out, a.Token)
	}
	return out, nil
}

// AssignedTo reads the devices assigned to an asset.
func (r *Reader) AssignedTo(ctx context.Context, asset string) ([]string, error) {
	var resp struct {
		AnchorDevices struct {
			Results []struct {
				Token string `json:"token"`
			} `json:"results"`
		} `json:"anchorDevices"`
	}
	c := map[string]any{"pageNumber": 1, "pageSize": anchorPageSize, "targetType": "asset", "target": asset, "relationshipType": "assigned"}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryAnchorDevices, map[string]any{"c": c}, &resp); err != nil {
		return nil, fmt.Errorf("read devices assigned to %s: %w", asset, err)
	}
	out := make([]string, 0, len(resp.AnchorDevices.Results))
	for _, d := range resp.AnchorDevices.Results {
		out = append(out, d.Token)
	}
	return out, nil
}

// Related reads the targets of a source asset's relationships of one type.
func (r *Reader) Related(ctx context.Context, sourceAsset, relType string) ([]string, error) {
	var resp struct {
		EntityRelationships struct {
			Results []struct {
				Target struct {
					Token string `json:"token"`
				} `json:"target"`
			} `json:"results"`
		} `json:"entityRelationships"`
	}
	c := map[string]any{"pageNumber": 1, "pageSize": relationPageSize, "sourceType": "asset", "source": sourceAsset, "relationshipType": relType}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryRelationships, map[string]any{"c": c}, &resp); err != nil {
		return nil, fmt.Errorf("read %s relationships of %s: %w", relType, sourceAsset, err)
	}
	out := make([]string, 0, len(resp.EntityRelationships.Results))
	for _, x := range resp.EntityRelationships.Results {
		out = append(out, x.Target.Token)
	}
	return out, nil
}
