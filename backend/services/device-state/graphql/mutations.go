// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"
)

// ---------------------------------
// Presence demotion result resolver
// ---------------------------------

type PresenceDemotionResultResolver struct {
	M model.PresenceDemotionResult
	S *SchemaResolver
	C context.Context
}

func (r *PresenceDemotionResultResolver) Scanned() int32 { return r.M.Scanned }
func (r *PresenceDemotionResultResolver) Demoted() int32 { return r.M.Demoted }
func (r *PresenceDemotionResultResolver) Skipped() int32 { return r.M.Skipped }

// LastId is the cursor for the next page, null when the page was empty. A zero id is
// not a row — ids ascend from 1 — so returning it as an id would hand the caller a
// cursor that restarts the walk at the beginning on every empty page.
func (r *PresenceDemotionResultResolver) LastId() *gql.ID {
	if r.M.LastId == 0 {
		return nil
	}
	id := gql.ID(fmt.Sprint(r.M.LastId))
	return &id
}

// actor is the identity recorded on every demotion this call emits: the caller's
// username, falling back to email (identity tokens carry email, tenant tokens a
// username). It is taken from the authenticated subject and never from an argument,
// so the provenance stamped on a fleet-wide write cannot be forged. Empty when
// unauthenticated, which is unreachable — the resolver is gated above this.
func actor(ctx context.Context) string {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return ""
	}
	if claims.Username != "" {
		return claims.Username
	}
	return claims.Email
}

// DemoteAssertedPresence returns one page of an event source's ASSERTED device states
// to INFERRED presence (ADR-067 demotion) — the operator's door onto the repair a
// source performs for itself at its own disable boundary.
//
// 🔴 DeviceTokens IS A POINTER, AND THAT IS WHAT MAKES OMITTED DISTINGUISHABLE FROM
// EMPTY. A `[]string` would collapse both onto a nil slice, and the model reads nil as
// "no narrowing" — so `deviceTokens: []`, which must demote nothing, would instead
// demote the entire source. That is the #753 shape arriving from the opposite
// direction, and the whole reason the three states are carried this far down rather
// than folded into a slice at the door.
//
// The authority check is first, ahead of every argument test, so an unauthorized
// caller cannot learn which limits are legal or whether a source exists.
func (r *SchemaResolver) DemoteAssertedPresence(ctx context.Context, args struct {
	Source       string
	DeviceTokens *[]string
	Limit        int32
	AfterId      *gql.ID
	Reason       string
}) (*PresenceDemotionResultResolver, error) {
	if err := auth.Authorize(ctx, auth.StateDemote); err != nil {
		return nil, err
	}
	after, err := cursorFrom(args.AfterId)
	if err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	result, err := api.DemoteAssertedPresence(ctx, args.Source, args.DeviceTokens, after,
		int(args.Limit), actor(ctx), args.Reason)
	if err != nil {
		// A publish failure carries the broker's own words — subject names, stream
		// names, cluster addressing — and this is a TENANT-facing plane. The detail is
		// logged where an operator can read it and the client is told only that the
		// demotion could not be published, which is the whole of what it can act on.
		// Every other failure here is the caller's own argument and is returned as-is.
		if errors.Is(err, model.ErrDemotionPublish) {
			log.Error().Err(err).Str("source", args.Source).Str("actor", actor(ctx)).
				Msg("Presence demotion could not be published")
			return nil, errors.New("the presence demotion could not be published; no devices in this page were demoted")
		}
		return nil, err
	}
	return &PresenceDemotionResultResolver{M: *result, S: r, C: ctx}, nil
}
