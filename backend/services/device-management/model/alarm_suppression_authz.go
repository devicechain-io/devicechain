// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func suppressionNarrow(w *AlarmSuppressionWindow) bool {
	if w.Kind != SuppressionOneShot || w.StartTime == nil || w.EndTime == nil || w.EndTime.Sub(*w.StartTime) > 24*time.Hour || len(w.Targets) == 0 || len(w.Targets) > 20 {
		return false
	}
	for _, t := range w.Targets {
		if t.Type != "DEVICE" {
			return false
		}
	}
	return true
}

// Narrow-window alarmKeys and bypass edits deliberately require only alarm:write.
// authorizeSuppressionWrite checks both sides of a partial update, and deletion.
func (api *Api) authorizeSuppressionWrite(ctx context.Context, existing, result *AlarmSuppressionWindow) error {
	scope, err := auth.AuthorizeScoped(ctx, auth.AlarmWrite)
	if err != nil {
		return err
	}
	if scope.IsScoped() && existing != nil && result != nil && !sameSuppressionTargets(existing.Targets, result.Targets) {
		return auth.ErrForbidden
	}
	for _, w := range []*AlarmSuppressionWindow{existing, result} {
		if w == nil {
			continue
		}
		if !suppressionNarrow(w) {
			if _, err := auth.AuthorizeScoped(ctx, auth.NotificationWrite); err != nil {
				return err
			}
		}
		if visible, err := api.suppressionTargetsInScope(ctx, w); err != nil {
			return err
		} else if !visible {
			return auth.ErrForbidden
		}
	}
	return nil
}

// suppressionTargetsInScope checks current target ownership after the storage stamp filter.
// Both read visibility and write authorization use this predicate.
func (api *Api) suppressionTargetsInScope(ctx context.Context, w *AlarmSuppressionWindow) (bool, error) {
	scope, scoped := auth.ScopeFromContext(ctx)
	if !scoped {
		return true, nil
	}
	for _, target := range w.Targets {
		switch target.Type {
		case "DEVICE":
			found, err := api.DevicesByToken(ctx, []string{target.Token})
			if err != nil {
				return false, err
			}
			if len(found) != 1 {
				return false, nil
			}
		case "CUSTOMER", "ENTITY_GROUP":
			kind := "customer"
			if target.Type == "ENTITY_GROUP" {
				kind = "group"
			}
			if !slices.Contains(scope.Anchors(), kind+":"+target.Token) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

func sameSuppressionTargets(a, b []AlarmSuppressionTarget) bool {
	if len(a) != len(b) {
		return false
	}
	for _, t := range a {
		if !slices.Contains(b, t) {
			return false
		}
	}
	return true
}

// suppressionFactContext removes only the already-authorized caller's scope for internal
// graph reads. It retains tenancy, deadlines and cancellation. No result from those reads
// is exposed directly to that caller; API reads enforce visibility separately.
func suppressionFactContext(ctx context.Context) context.Context {
	return auth.WithClaims(ctx, nil)
}

func (api *Api) suppressionAnchors(ctx context.Context, w *AlarmSuppressionWindow) (rdb.Anchors, error) {
	devices, err := api.suppressionDeviceAnchors(ctx, []AlarmSuppressionWindow{*w})
	if err != nil {
		return nil, err
	}
	return suppressionAnchorsFromDevices(ctx, w, devices), nil
}

// Read each distinct DEVICE once for the entire batch. DeviceScopeAnchors uses
// FindByKeys, which REFUSES oversized lists rather than chunking them itself.
func (api *Api) suppressionDeviceAnchors(ctx context.Context, windows []AlarmSuppressionWindow) (map[string][]string, error) {
	tokens := []string{}
	seen := map[string]bool{}
	for _, w := range windows {
		for _, target := range w.Targets {
			if target.Type == "DEVICE" && !seen[target.Token] {
				seen[target.Token] = true
				tokens = append(tokens, target.Token)
			}
		}
	}
	devices := map[string][]string{}
	for start := 0; start < len(tokens); start += rdb.MaxLookupKeys {
		found, err := api.DeviceScopeAnchors(suppressionFactContext(ctx), tokens[start:min(start+rdb.MaxLookupKeys, len(tokens))])
		if err != nil {
			return nil, err
		}
		for token, anchors := range found {
			devices[token] = anchors
		}
	}
	return devices, nil
}

// Creation, target edits and ownership refresh use the same intersection,
// including ordering, empty sets and the optional caller-scope restriction.
func suppressionAnchorsFromDevices(ctx context.Context, w *AlarmSuppressionWindow, devices map[string][]string) rdb.Anchors {
	var intersection rdb.Anchors
	for i, target := range w.Targets {
		anchors := rdb.Anchors{}
		switch target.Type {
		case "DEVICE":
			anchors = devices[target.Token]
		case "CUSTOMER":
			anchors = rdb.Anchors{"customer:" + target.Token}
		case "ENTITY_GROUP":
			anchors = rdb.Anchors{groupAnchor(target.Token)}
		case "AREA", "ASSET":
			anchors = rdb.Anchors{strings.ToLower(target.Type) + ":" + target.Token}
		}
		if i == 0 {
			intersection = append(rdb.Anchors{}, anchors...)
		} else {
			intersection = intersectSuppressionAnchors(intersection, anchors)
		}
	}
	if scope, scoped := auth.ScopeFromContext(ctx); scoped {
		intersection = intersectSuppressionAnchors(intersection, scope.Anchors())
	}
	slices.Sort(intersection)
	return intersection
}

func intersectSuppressionAnchors(a, b []string) rdb.Anchors {
	out := rdb.Anchors{}
	for _, x := range a {
		if slices.Contains(b, x) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// Batch DEVICE ownership under the caller's claims; the whole-window predicate
// is identical to write authorization, including rejection of broad targets.
func (api *Api) filterSuppressionWindowsInScope(ctx context.Context, rows []AlarmSuppressionWindow) ([]AlarmSuppressionWindow, error) {
	scope, scoped := auth.ScopeFromContext(ctx)
	if !scoped {
		return rows, nil
	}
	tokens := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		for _, target := range row.Targets {
			if target.Type == "DEVICE" && !seen[target.Token] {
				seen[target.Token] = true
				tokens = append(tokens, target.Token)
			}
		}
	}
	visible := map[string]bool{}
	for start := 0; start < len(tokens); start += rdb.MaxLookupKeys {
		found, err := api.DevicesByToken(ctx, tokens[start:min(start+rdb.MaxLookupKeys, len(tokens))])
		if err != nil {
			return nil, err
		}
		for _, device := range found {
			visible[device.Token] = true
		}
	}
	out := make([]AlarmSuppressionWindow, 0, len(rows))
	for _, row := range rows {
		allowed := true
		for _, target := range row.Targets {
			switch target.Type {
			case "DEVICE":
				allowed = allowed && visible[target.Token]
			case "CUSTOMER":
				allowed = allowed && slices.Contains(scope.Anchors(), "customer:"+target.Token)
			case "ENTITY_GROUP":
				allowed = allowed && slices.Contains(scope.Anchors(), "group:"+target.Token)
			default:
				allowed = false
			}
		}
		if allowed {
			out = append(out, row)
		}
	}
	return out, nil
}
