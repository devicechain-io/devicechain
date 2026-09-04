// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// Helpers shared by the partial-update path (the platform-wide three-state update
// semantic: omitted leaves alone, null clears, a value sets).
//
// The scalar folds themselves live on the Optional* types in core — ApplyTo and
// ApplyToValue. What needs saying here is the one case those cannot express: a
// reference whose FK column is NOT NULL.

// resolveRequiredTypeRef decides what an update should do with an entity's
// REQUIRED type reference — Asset.assetTypeToken, Device.deviceTypeToken and their
// peers, all of which sit on a NOT NULL column.
//
// It returns the token that still needs looking up and whether a lookup is needed
// at all, so a caller can do the (potentially failing) database hop BEFORE writing
// anything. That ordering is the whole point: an unknown token must refuse the
// WHOLE update, not apply the fields it liked and then fail on the reference.
//
// 🔴 An explicit null is REFUSED rather than honoured, and the asymmetry with
// DeviceType.profileToken is deliberate. A device type's profile is nullable — "no
// profile adopted" is a real state — so null there detaches. An asset's type is
// not: the column is NOT NULL, so honouring a null could only mean writing a
// dangling zero FK or quietly ignoring what the caller asked for. Refusing says so.
// An empty or whitespace-only token is the same request spelled differently and is
// refused the same way; on the nullable profileToken it detaches, which is exactly
// why the two cannot share one rule.
//
// currentToken is the token the entity references today, or "" when the caller
// could not determine it (a nil preload). "" forces a re-resolve rather than a
// skip, so a dangling reference is repaired by naming a valid token rather than
// being compared against nothing and silently kept.
func resolveRequiredTypeRef(field dcgraphql.OptionalString, currentToken string,
	refLabel string) (token string, needsResolve bool, err error) {
	if !field.Set {
		return "", false, nil
	}
	if field.Value == nil {
		return "", false, fmt.Errorf("%s cannot be cleared: every entity of this kind must "+
			"reference one, so send a different token to re-point it", refLabel)
	}
	requested := strings.TrimSpace(*field.Value)
	if requested == "" {
		return "", false, fmt.Errorf("%s cannot be empty: every entity of this kind must reference one", refLabel)
	}
	if currentToken != "" && requested == currentToken {
		return "", false, nil
	}
	return requested, true, nil
}

// errPayloadTokenDisagrees refuses an update whose payload token names a DIFFERENT
// row than the `token` argument does.
//
// It exists for the update mutations still on the full-replace shape, which reuse a
// *CreateRequest and therefore carry the token twice: once to say which row, once
// inside the shared input. Those two used to be able to disagree, and the disagreement
// resolved the wrong way — the payload token was the LOOKUP KEY and the mandatory
// `token` argument was ignored outright, so a caller naming one entity in the
// argument and another in the payload silently updated the second and got a 200
// describing it. A dead mandatory argument is worse than an absent one: it reads like
// the identity channel, so a client that gets it right and the payload wrong has no
// way to find out.
//
// The families converted to a dedicated *UpdateRequest do not need this — their input
// carries no token at all, which makes the disagreement unrepresentable rather than
// merely refused. This is the interim rule for the ones that still do, and it matches
// what updateGeoFence has always done (errGeoFenceTokenImmutable); the difference is
// that a fence refuses a rename because rules name fences by token, whereas these
// refuse it because a silent lookup-key swap is not something a caller can have meant.
//
// An EMPTY payload token is accepted as "unspecified" rather than refused, because
// under a shared create input a caller who has nothing to say about identity has no
// other way to say it. Once a family converts, that ambiguity goes away with the field.
func errPayloadTokenDisagrees(entity, token, requested string) error {
	if requested == "" || requested == token {
		return nil
	}
	return fmt.Errorf("cannot update %s %q through a request naming %q: the token argument "+
		"identifies the record, and a request that disagrees with it is refused rather than "+
		"applied to whichever one the payload happened to name", entity, token, requested)
}
