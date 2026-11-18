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

// authorizeSuppressionWrite checks both sides of a partial update, including end/delete.
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
	var intersection rdb.Anchors
	for i, target := range w.Targets {
		anchors := rdb.Anchors{}
		switch target.Type {
		case "DEVICE":
			found, err := api.DeviceScopeAnchors(suppressionFactContext(ctx), []string{target.Token})
			if err != nil {
				return nil, err
			}
			anchors = found[target.Token]
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
	return intersection, nil
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
