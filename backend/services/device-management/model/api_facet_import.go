// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/limit"
	"gorm.io/gorm"
)

// FacetValueImportRow addresses one declared facet on an existing entity by token.
// Values are always SHARED and use the declaration's type, never a client-supplied type.
// Text is trimmed like the single-attribute writer; blank values are refused.
// Clearing attributes remains a separate operation.
type FacetValueImportRow struct {
	EntityType string
	Token      string
	Key        string
	Value      string
}

// FacetValueImportRowResult contains exactly one outcome per input row, in input order.
type FacetValueImportRowResult struct {
	Row        int32
	EntityType string
	Token      string
	Key        string
	Outcome    string
	Code       *string
	Message    *string
}

// ImportFacetValues upserts up to 1000 facet values, with one independent transaction
// per changed row through the single-attribute writer (including membership and device
// configuration reconciliation). An identical normalized value is EXISTS and is not
// written again. The natural key is (entity type, entity token, facet key), tenant-scoped.
// Later duplicates in a call are INVALID; a dry run uses the same validation and reads
// without writing. Database failures are explicit REFUSED rows, including after earlier
// commits, so a caller always receives a report for the whole batch and can re-run it.
func (api *Api) ImportFacetValues(ctx context.Context, rows []*FacetValueImportRow, dryRun bool) ([]*FacetValueImportRowResult, error) {
	if _, err := ctxTenant(ctx); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("rows must not be empty")
	}
	if len(rows) > MaxDeviceImportRows {
		return nil, limit.Exceeded("facet values in one import", len(rows), MaxDeviceImportRows)
	}
	results := make([]*FacetValueImportRowResult, 0, len(rows))
	seen := map[[3]string]bool{}
	for i, row := range rows {
		res := &FacetValueImportRowResult{Row: int32(i + 1)}
		results = append(results, res)
		if row == nil {
			res.fail("INVALID", "INVALID_VALUE", "row must not be null")
			continue
		}
		res.EntityType, res.Token, res.Key = row.EntityType, row.Token, row.Key
		if !ValidGroupMemberType(entity.Type(row.EntityType)) || row.Token == "" || row.Key == "" ||
			len(row.Key) > MaxAttributeKeyBytes || len(row.Value) > MaxAttributeValueBytes {
			res.fail("INVALID", "INVALID_VALUE", "expected a member family, entity token, facet key and a value within the attribute bounds")
			continue
		}
		key := [3]string{row.EntityType, row.Token, row.Key}
		if seen[key] {
			res.fail("INVALID", "DUPLICATE_IN_FILE", "this entity token and facet key already appear in the batch")
			continue
		}
		seen[key] = true
		api.importFacetValue(ctx, row, res, dryRun)
	}
	return results, nil
}

func (r *FacetValueImportRowResult) fail(outcome, code, message string) {
	r.Outcome, r.Code, r.Message = outcome, &code, &message
}

func (api *Api) importFacetValue(ctx context.Context, row *FacetValueImportRow, res *FacetValueImportRowResult, dryRun bool) {
	var facet FacetKey
	err := api.RDB.DB(ctx).Where("member_type = ? AND attr_key = ?", row.EntityType, row.Key).First(&facet).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		res.fail("INVALID", "NOT_FOUND", "facet key is not declared for this member family")
		return
	}
	if err != nil {
		res.fail("REFUSED", "IMPORT_FAILED", "could not read the facet declaration; re-run this row")
		return
	}
	if facet.Source != string(FacetSourceAttribute) || !AttributeValueType(facet.ValueType).Valid() {
		res.fail("INVALID", "INVALID_VALUE", "facet does not have a writable attribute source and value type")
		return
	}
	value, err := normalizeAttributeValue(facet.ValueType, &row.Value)
	if err != nil || (facet.ValueType == string(AttributeValueJson) && !json.Valid([]byte(row.Value))) {
		res.fail("INVALID", "INVALID_VALUE", "value cannot be stored under the facet's declared type")
		return
	}
	// The single-attribute writer stores text through sqlnull.Text. Compare its
	// canonical form too, or a padded string would be written again on every re-run.
	// Refuse a blank value explicitly rather than report a successful NULL write.
	canonical := strings.TrimSpace(*value)
	if canonical == "" {
		res.fail("INVALID", "INVALID_VALUE", "value must not be blank; clear attributes with deleteEntityAttribute")
		return
	}
	value = &canonical
	id, err := api.ResolveEntityToken(ctx, row.EntityType, row.Token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		res.fail("INVALID", "NOT_FOUND", "entity token was not found")
		return
	}
	if err != nil {
		res.fail("REFUSED", "IMPORT_FAILED", "could not resolve the entity token; re-run this row")
		return
	}
	var stored EntityAttribute
	err = api.RDB.DB(ctx).Where("entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
		row.EntityType, id, string(AttributeScopeShared), row.Key).First(&stored).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		res.fail("REFUSED", "IMPORT_FAILED", "could not read the current facet value; re-run this row")
		return
	}
	if err == nil && stored.ValueType == facet.ValueType && stored.Value.Valid && stored.Value.String == *value {
		res.Outcome = "EXISTS"
		return
	}
	if !dryRun {
		_, err = api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
			EntityType: row.EntityType, Entity: row.Token, Scope: string(AttributeScopeShared),
			AttrKey: row.Key, ValueType: facet.ValueType, Value: value,
		})
		if err != nil {
			// Never expose raw database errors (which may include attribute contents).
			res.fail("REFUSED", "IMPORT_FAILED", "facet value was not written; re-run this row")
			return
		}
	}
	res.Outcome = "SET"
}
