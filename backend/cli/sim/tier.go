// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// TierSpec is a tenant tier a scenario needs to exist before its tenant can be packaged at it.
//
// Config holds only the floors the scenario requires: ensuring a tier raises a key that is below
// its floor and never lowers one an operator has set higher.
type TierSpec struct {
	Token       string
	Name        string
	Description string
	Config      map[string]int
}

// TowerpulseShowcaseTier is the tier the Towerpulse showcase gets unless --tier names another.
//
// Calculated metrics are OFF in every platform tier (the ceiling is 0 until an operator turns it
// on), so this scenario, which publishes calculated metrics, cannot run at silver. Rather than
// loosen a default tier, the sim provisions this one tier for its own tenant and says so.
//
// The values are literals: dcctl does not import the simulator, so
// TestTowerpulseTierCoversTheScenario in dc-simulator parses this file and holds
// calculatedMetricsPerProfile to the largest number of calculated metrics any Towerpulse profile
// declares. The ingest figures are the showcase's own load: 1000 readings per second, with room
// for a burst of twice that.
var TowerpulseShowcaseTier = TierSpec{
	Token:       "tp-showcase",
	Name:        "Towerpulse showcase",
	Description: "Created by dcctl sim create for the Towerpulse scenario: calculated metrics on, and ingest sized for the scripted day.",
	Config: map[string]int{
		"calculatedMetricsPerProfile": 1,
		"ingestReadingsPerSecond":     1000,
		"ingestBurst":                 2000,
	},
}

// manifestTiers maps a manifest id to the tier it needs. A manifest absent here is packaged at
// DefaultTenantTier.
var manifestTiers = map[string]TierSpec{
	"towerpulse": TowerpulseShowcaseTier,
}

// TierForManifest returns the tier a scenario needs created, if it needs one.
func TierForManifest(manifestId string) (TierSpec, bool) {
	t, ok := manifestTiers[manifestId]
	return t, ok
}

const (
	tenantTiersQuery = `query{tenantTiers{token config}}`

	createTenantTierMutation = `mutation($token:String!,$name:String,$description:String,$config:String){` +
		`createTenantTier(request:{token:$token,name:$name,description:$description,config:$config}){token}}`

	updateTenantTierMutation = `mutation($token:String!,$config:String){` +
		`updateTenantTier(token:$token,request:{config:$config}){token}}`
)

// TierRefusedError is returned when the tier a scenario needs could not be created or raised.
// It carries the fix, so the failure is read here rather than as a refused publish later.
type TierRefusedError struct {
	Tier  TierSpec
	Cause error
}

func (e *TierRefusedError) Error() string {
	cfg, _ := json.Marshal(e.Tier.Config)
	return fmt.Sprintf("the scenario needs tenant tier %q (config %s) and it could not be created or raised: %v\n"+
		"dcctl has no tier command; as a superuser with tenant:write, create it on /admin/graphql "+
		"(createTenantTier, or updateTenantTier to raise an existing one) with that config, "+
		"or package the sim at a tier that already allows it with `dcctl sim create ... --tier <token>`",
		e.Tier.Token, cfg, e.Cause)
}

func (e *TierRefusedError) Unwrap() error { return e.Cause }

// EnsureTier makes the tier exist with at least the spec's config. It is idempotent: an absent
// tier is created, one already at or above every floor is left alone, and one below a floor has
// only those keys raised (its other keys, and any key above its floor, are kept). Any failure is
// returned as a *TierRefusedError.
func (a *Admin) EnsureTier(ctx context.Context, spec TierSpec) error {
	if err := a.ensureTier(ctx, spec); err != nil {
		return &TierRefusedError{Tier: spec, Cause: err}
	}
	return nil
}

func (a *Admin) ensureTier(ctx context.Context, spec TierSpec) error {
	var out struct {
		TenantTiers []struct {
			Token  string  `json:"token"`
			Config *string `json:"config"`
		} `json:"tenantTiers"`
	}
	if err := a.session.Query(ctx, a.adminURL, tenantTiersQuery, nil, &out); err != nil {
		return fmt.Errorf("list tiers: %w", err)
	}
	for _, t := range out.TenantTiers {
		if t.Token != spec.Token {
			continue
		}
		current := map[string]any{}
		if t.Config != nil && *t.Config != "" {
			if err := json.Unmarshal([]byte(*t.Config), &current); err != nil {
				return fmt.Errorf("tier %q has a config that is not a JSON object: %w", spec.Token, err)
			}
		}
		merged, changed := raiseToFloors(current, spec.Config)
		if !changed {
			return nil
		}
		raw, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		if err := a.session.Query(ctx, a.adminURL, updateTenantTierMutation,
			map[string]any{"token": spec.Token, "config": string(raw)}, nil); err != nil {
			return fmt.Errorf("raise tier %q: %w", spec.Token, err)
		}
		return nil
	}
	raw, err := json.Marshal(spec.Config)
	if err != nil {
		return err
	}
	err = a.session.Query(ctx, a.adminURL, createTenantTierMutation,
		map[string]any{"token": spec.Token, "name": spec.Name, "description": spec.Description, "config": string(raw)}, nil)
	// A concurrent create that won the race leaves the tier in place, which is the goal.
	return tolerateExists(fmt.Errorf("create tier %q: %w", spec.Token, err), err)
}

// raiseToFloors returns current with every floor key that is missing or lower raised to the
// floor, and whether anything changed.
func raiseToFloors(current map[string]any, floors map[string]int) (map[string]any, bool) {
	out := make(map[string]any, len(current)+len(floors))
	for k, v := range current {
		out[k] = v
	}
	keys := make([]string, 0, len(floors))
	for k := range floors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changed := false
	for _, k := range keys {
		have, ok := out[k].(float64)
		if !ok || have < float64(floors[k]) {
			out[k] = floors[k]
			changed = true
		}
	}
	return out, changed
}
