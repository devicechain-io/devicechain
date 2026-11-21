// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// FacetValueImportRow addresses one declared facet on an existing entity by token.
// Values are always SHARED and use the declaration's type, never a client-supplied type.
// Text is trimmed like the single-attribute writer; blank values are refused.
// Clearing attributes remains a separate operation.
type FacetValueImportRow struct {
	MemberType string
	Token      string
	Key        string
	Value      string
}

// FacetValueImportRowResult contains exactly one outcome per input row, in input order.
type FacetValueImportRowResult struct {
	Row        int32
	MemberType string
	Token      string
	Key        string
	Outcome    string
	Code       *string
	Message    *string
}

// ImportFacetValues upserts up to 1000 facet values, with one independent transaction
// per changed row through shared attribute reconciliation (including membership and
// device configuration). An identical normalized value is EXISTS and is not
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
		res.MemberType, res.Token, res.Key = row.MemberType, row.Token, row.Key
		if !ValidGroupMemberType(entity.Type(row.MemberType)) || row.Token == "" || row.Key == "" ||
			len(row.Key) > MaxAttributeKeyBytes || len(row.Value) > MaxAttributeValueBytes {
			res.fail("INVALID", "INVALID_VALUE", "expected a member family, entity token, facet key and a value within the attribute bounds")
			continue
		}
		key := [3]string{row.MemberType, row.Token, row.Key}
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
	var created *EntityAttribute
	var change membershipChange
	var minted *DeviceConfigurationRevision
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockFacetKey(ctx, tx, row.MemberType, row.Key); err != nil {
			return err
		}
		var err error
		created, err = api.importFacetValueOnTx(tx, row, res, dryRun)
		if err != nil || created == nil || dryRun {
			return err
		}
		if err := upsertAttributeOnTx(tx, created); err != nil {
			return err
		}
		change, minted, err = api.reconcileAttributeWriteOnTx(ctx, tx, created.EntityType, created.EntityId, created.AttrKey, created.Scope)
		return err
	})
	if err != nil {
		res.refuse(err)
		return
	}
	if created != nil && !dryRun {
		api.publishMintedConfiguration(ctx, created.EntityId, minted)
		api.afterMembershipRecompute(ctx, created.EntityType, created.EntityId, change)
		api.emitDeviceAttributeForSet(ctx, &EntityAttributeSetRequest{
			EntityType: created.EntityType, Entity: row.Token, Scope: created.Scope,
			AttrKey: created.AttrKey, ValueType: created.ValueType, Value: &created.Value.String,
		}, created.LastUpdated)
	}
}

// refuse exposes only allowlisted, typed refusals and never raw database errors.
func (r *FacetValueImportRowResult) refuse(err error) {
	var refusal *ConfigurationRefusal
	if errors.As(err, &refusal) {
		switch refusal.Code {
		case CodeConfigurationValueType:
			r.fail("REFUSED", refusal.Code, "facet value conflicts with the device's active configuration type; correct the facet or configuration declaration")
			return
		case CodeConfigurationTooLarge:
			r.fail("REFUSED", refusal.Code, "facet value would exceed the device configuration document limit; reduce the configuration values")
			return
		}
	}
	r.fail("REFUSED", "IMPORT_FAILED", "facet value was not written; re-run this row")
}

// importFacetValueOnTx reads the declaration and current value under the same
// transaction-scoped lock as SetFacetKey and DeleteFacetKey. The caller keeps the
// lock until the attribute and its derived state commit together.
func (api *Api) importFacetValueOnTx(tx *gorm.DB, row *FacetValueImportRow, res *FacetValueImportRowResult, dryRun bool) (*EntityAttribute, error) {
	var facet FacetKey
	err := tx.Where("member_type = ? AND attr_key = ?", row.MemberType, row.Key).First(&facet).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		res.fail("INVALID", "NOT_FOUND", "facet key is not declared for this member family")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if facet.Source != string(FacetSourceAttribute) || !AttributeValueType(facet.ValueType).Valid() {
		res.fail("INVALID", "INVALID_VALUE", "facet does not have a writable attribute source and value type")
		return nil, nil
	}
	value, err := normalizeAttributeValue(facet.ValueType, &row.Value)
	if err != nil || (facet.ValueType == string(AttributeValueJson) && !json.Valid([]byte(row.Value))) {
		res.fail("INVALID", "INVALID_VALUE", "value cannot be stored under the facet's declared type")
		return nil, nil
	}
	// The single-attribute writer stores text through sqlnull.Text. Compare its
	// canonical form too, or a padded string would be written again on every re-run.
	// Refuse a blank value explicitly rather than report a successful NULL write.
	canonical := strings.TrimSpace(*value)
	if canonical == "" {
		res.fail("INVALID", "INVALID_VALUE", "value must not be blank; clear attributes with deleteEntityAttribute")
		return nil, nil
	}
	value = &canonical
	member, _, _ := memberModelAndTable(row.MemberType)
	var owner struct{ ID uint }
	err = tx.Model(member).Select("id").Where("token = ?", row.Token).Take(&owner).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		res.fail("INVALID", "NOT_FOUND", "entity token was not found")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored EntityAttribute
	err = tx.Where("entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
		row.MemberType, owner.ID, string(AttributeScopeShared), row.Key).First(&stored).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil && stored.ValueType == facet.ValueType && stored.Value.Valid && stored.Value.String == *value {
		res.Outcome = "EXISTS"
		return nil, nil
	}
	created := &EntityAttribute{
		EntityType: row.MemberType, EntityId: owner.ID, Scope: string(AttributeScopeShared),
		AttrKey: row.Key, ValueType: facet.ValueType, Value: rdb.NullStrOf(value), LastUpdated: time.Now(),
	}
	if dryRun {
		if err := previewConfigurationAttribute(tx, created); err != nil {
			return nil, err
		}
	}
	res.Outcome = "SET"
	return created, nil
}
