// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"fmt"

	"gorm.io/datatypes"
)

// ASSET PROPERTIES (ADR-072): the type contract, and the two gates over it.
//
// An asset type declares WHAT an asset of that type carries — a []ParameterSpec
// property schema, the same descriptor a command definition uses for its arguments.
// An asset carries a property DOCUMENT filling that contract. The relationship
// between the two is exactly the one between a command payload and its command
// definition's parameter schema, so it is checked by the same validator rather than
// by a second one that would drift.
//
// There are two gates, and they run at different moments for different reasons:
//
//	DECLARATION  validateAssetPropertySchemaInput — when the DRAFT schema is written.
//	             Is this contract coherent at all? (unique names, known datatypes,
//	             bounds that can be satisfied, a default that fits its own enum)
//	INSTANCE     validateAssetProperties — when an asset's document is written.
//	             Does this document satisfy the type's ACTIVE PUBLISHED contract?
//
// 🔴 THE INSTANCE GATE IS STRICT, AND THAT IS A DECISION, NOT AN INHERITANCE. The
// two existing gates in this area sit on opposite sides of it and the reason is
// stated in each: measurement validation is LENIENT because a device wrote the value
// and a profile misconfiguration must not reject a fleet's data; command validation
// is STRICT because a command is an actuation and a mis-keyed argument must never be
// silently delivered. An asset property is written by an OPERATOR through the API,
// and a misspelled property that is quietly accepted produces an asset that reads as
// correctly described and is not — the same failure the graphql-go fork exists to
// stop. So: strict, on the command side of the line.

// validateAssetPropertySchemaInput checks a draft property schema on its way in: it
// must be a JSON array of descriptors with no unrecognized keys (so a typo'd
// constraint like "maximum" is refused rather than dropped), and the contract it
// declares must be internally coherent. A nil or blank document is accepted and
// means the type declares no contract.
//
// It returns the column value to store, so the one place that validates the input is
// the one place that encodes it — a caller cannot store an unvalidated schema
// without visibly not calling this.
func validateAssetPropertySchemaInput(raw *string) (*datatypes.JSON, error) {
	if raw == nil {
		return nil, nil
	}
	if len(bytes.TrimSpace([]byte(*raw))) == 0 {
		return nil, nil
	}
	specs, err := decodeSpecsStrict([]byte(*raw))
	if err != nil {
		return nil, fmt.Errorf("propertySchema: %w", err)
	}
	if err := assetPropertySpecs.validateSchema(specs); err != nil {
		return nil, err
	}
	stored := datatypes.JSON([]byte(*raw))
	return &stored, nil
}

// validateAssetProperties checks an asset's property document against the ACTIVE
// PUBLISHED contract of the type it belongs to. It is total over four cases, and
// each of them is a decision:
//
//	the type has NO published version   — a document is refused outright; there is
//	                                      nothing to check it against, and accepting
//	                                      it would store values under a contract that
//	                                      does not exist yet. An ABSENT document is
//	                                      fine: the asset simply carries none.
//	the published contract is EMPTY     — the author has said assets of this type
//	                                      carry nothing, so any non-empty document is
//	                                      refused. 🔴 This case is handled HERE rather
//	                                      than by the shared validator, whose empty
//	                                      schema means "free-form, accept anything" —
//	                                      the right reading for a command definition
//	                                      that predates schemas and the exactly wrong
//	                                      one for a contract an author just published.
//	the document is ABSENT              — validated as an empty object, so a contract
//	                                      with a REQUIRED property refuses it. Sending
//	                                      nothing is not a way around required-ness,
//	                                      which is the same call ValidateCommandPayload
//	                                      makes for an absent payload.
//	otherwise                           — the shared strict document check.
//
// 🔴 A consequence worth stating plainly, because it will be met before it is read:
// publishing a contract with a REQUIRED property makes every existing asset of that
// type non-conformant, and the next write to any of them — including one that only
// renames it — is refused until the property is supplied. That is what "required"
// means; the escape hatch is to roll the type back to a version that did not demand
// it, which is instant and non-destructive.
func (api *Api) validateAssetProperties(ctx context.Context, assetType *AssetType, document *datatypes.JSON) error {
	raw := []byte(nil)
	if document != nil {
		raw = []byte(*document)
	}
	absent := len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"

	// A NIL TYPE IS THE DANGLING-FK CASE, and the ordering here matters. The preload
	// comes back nil for an asset whose asset_type_id names nothing — a state the
	// Postgres foreign key makes unreachable, which is why UpdateAsset already carries
	// a nil guard for it rather than dereferencing. Refusing unconditionally would mean
	// this gate newly blocked an ordinary RENAME of such an asset, which is a
	// regression it has no business causing: there is no document to check, so there is
	// nothing for it to say. A document, on the other hand, has nothing to be checked
	// against, and that is refused for the same reason an unpublished type is.
	if assetType == nil {
		if absent {
			return nil
		}
		return fmt.Errorf("asset has no asset type, so there is no contract its properties could satisfy")
	}

	if !assetType.ActiveVersion.Valid {
		if absent {
			return nil
		}
		return fmt.Errorf(
			"asset type %q has no published property schema, so its assets can carry no properties "+
				"(publish the asset type first)", assetType.Token)
	}

	version, err := api.assetTypeVersionByNumber(ctx, assetType.ID, assetType.ActiveVersion.Int32)
	if err != nil {
		return err
	}
	specs, err := decodeSpecsStrict(version.PropertySchema)
	if err != nil {
		return fmt.Errorf("asset type %q version %d: %w", assetType.Token, version.Version, err)
	}

	if len(specs) == 0 {
		if absent {
			return nil
		}
		obj, err := decodeObject(raw)
		if err != nil {
			return err
		}
		if len(obj) > 0 {
			return fmt.Errorf("asset type %q version %d declares no properties", assetType.Token, version.Version)
		}
		return nil
	}
	return assetPropertySpecs.validateDocument(specs, raw)
}
