// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/gorm"
)

// Helpers shared by the partial-update path (the platform-wide three-state update
// semantic: omitted leaves alone, null clears, a value sets).
//
// The scalar folds themselves live on the Optional* types in core — ApplyTo for a
// nullable column, ApplyToRequired for one that cannot be cleared. What needs saying
// here is the one case neither expresses: a reference whose FK column is NOT NULL.

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
// currentToken is the token the entity references today, or "" when the caller could
// not determine it (a nil preload — a dangling FK). The skip below compares the
// requested token against it, and "" can never equal a requested token, because a
// blank request has already been refused above. So a nil preload always re-resolves:
// a dangling reference is repaired by naming a valid token rather than being compared
// against nothing and silently kept.
//
// 🔴 That is a PROPERTY OF THE ORDERING, not of a guard. An earlier version of this
// wrote `currentToken != "" && requested == currentToken` and a comment claiming the
// first conjunct forced the re-resolve. It did nothing — the blank check above had
// already made it unreachable — and a mutant deleting it survived, being exactly
// behaviour-equivalent. The nil guard in the CALLERS is the real mechanism and is
// still needed; it is what makes currentToken "" instead of dereferencing nil.
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
	if requested == currentToken {
		return "", false, nil
	}
	return requested, true, nil
}

// resolveProfileRef is resolveRequiredTypeRef plus the database hop, for the three
// profile-scoped definition families — metric, command and detection rule — that all
// hang off DeviceProfile through a NOT NULL FK.
//
// It returns the profile to re-parent onto, or nil for "the caller did not ask to
// move it". Both the decision and the hop happen BEFORE any field is folded, so an
// unknown profile token refuses the WHOLE update; a caller who retries has not
// half-applied the first attempt.
//
// The nil `current` guard is the one in the callers of resolveRequiredTypeRef made
// reusable: a dangling FK preloads as nil, and passing "" makes the token comparison
// fail rather than dereferencing nothing, so a dangling reference is repaired by
// naming a valid token instead of being compared against nothing and silently kept.
func (api *Api) resolveProfileRef(ctx context.Context, field dcgraphql.OptionalString,
	current *DeviceProfile) (*DeviceProfile, error) {
	currentToken := ""
	if current != nil {
		currentToken = current.Token
	}
	requested, needsResolve, err := resolveRequiredTypeRef(field, currentToken, "deviceProfileToken")
	if err != nil {
		return nil, err
	}
	if !needsResolve {
		return nil, nil
	}
	profiles, err := api.DeviceProfilesByToken(ctx, []string{requested})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return profiles[0], nil
}
