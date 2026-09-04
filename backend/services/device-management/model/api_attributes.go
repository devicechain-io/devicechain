// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file holds the current-state EntityAttribute API (ADR-012). Attributes
// are addressed by uniform (entity_type, entity_id) (ADR-013) and resolved via
// the entity-type registry; unlike events, a write to the same (entity, scope,
// key) upserts the existing row.

// SetEntityAttribute upserts a single current-state attribute. The owner is
// resolved from (type, token) to a row id via the entity-type registry, which
// validates both the type and the entity's existence (ADR-012).
func (api *Api) SetEntityAttribute(ctx context.Context,
	request *EntityAttributeSetRequest) (*EntityAttribute, error) {
	// Validate the scope and value-type vocabularies.
	if !AttributeScope(request.Scope).Valid() {
		return nil, fmt.Errorf("invalid attribute scope: %s", request.Scope)
	}
	if !AttributeValueType(request.ValueType).Valid() {
		return nil, fmt.Errorf("invalid attribute value type: %s", request.ValueType)
	}

	// Normalize the stored value so the value column is always well-formed for its
	// value_type (ADR-061 G3 §3.4), and REFUSE a present value the declared type cannot
	// hold. See normalizeAttributeValue for why refusing beats coercing here.
	normalized, err := normalizeAttributeValue(request.ValueType, request.Value)
	if err != nil {
		return nil, err
	}
	request.Value = normalized

	// Resolve and validate the owner entity reference.
	entityId, err := api.ResolveEntityToken(ctx, request.EntityType, request.Entity)
	if err != nil {
		return nil, err
	}

	// Upsert: update an existing (entity, scope, key) row or create a new one.
	existing := &EntityAttribute{}
	result := api.RDB.DB(ctx).Where(
		"entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
		request.EntityType, entityId, request.Scope, request.AttrKey).First(existing)
	if result.Error == nil {
		existing.ValueType = request.ValueType
		existing.Value = rdb.NullStrOf(request.Value)
		existing.LastUpdated = time.Now()
		// Same-txn membership recompute (ADR-062 S2): a SHARED-scope facet change can move
		// this entity in/out of a rule-scoped group, so the read-model is updated in the
		// write's transaction (a no-op when no rule references the changed key).
		if err := api.writeAttrThenRecompute(ctx, request.EntityType, entityId, request.AttrKey, request.Scope,
			func(db *gorm.DB) error { return db.Save(existing).Error }); err != nil {
			return nil, err
		}
		api.emitDeviceAttributeForSet(ctx, request, existing.LastUpdated)
		return existing, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, result.Error
	}

	created := &EntityAttribute{
		EntityType:  request.EntityType,
		EntityId:    entityId,
		Scope:       request.Scope,
		AttrKey:     request.AttrKey,
		ValueType:   request.ValueType,
		Value:       rdb.NullStrOf(request.Value),
		LastUpdated: time.Now(),
	}
	if err := api.writeAttrThenRecompute(ctx, request.EntityType, entityId, request.AttrKey, request.Scope,
		func(db *gorm.DB) error { return db.Create(created).Error }); err != nil {
		return nil, err
	}
	api.emitDeviceAttributeForSet(ctx, request, created.LastUpdated)
	return created, nil
}

// emitDeviceAttributeForSet emits a device-attribute fact for a just-committed attribute
// upsert (ADR-051 slice 4c-3), when the attribute is a numeric, platform-set attribute
// of a device that event-processing tracks as a dynamic-threshold source. A numeric,
// parseable value emits an upsert carrying that value; a non-numeric value/type emits a
// removal so a previously-projected numeric value never goes stale. Non-device entities
// and CLIENT-scope attributes are ignored (a device must not set the limit it is checked
// against). Best-effort, post-commit; a nil publisher (tests / pre-wiring) is a no-op.
func (api *Api) emitDeviceAttributeForSet(ctx context.Context, request *EntityAttributeSetRequest, updatedAt time.Time) {
	if api.DeviceAttributePublisher == nil || !dynamicThresholdEligible(request.EntityType, request.Scope) {
		return
	}
	event := &DeviceAttributeEvent{
		DeviceToken: request.Entity, // the owner token, event-processing's device series key
		AttrKey:     request.AttrKey,
		Scope:       request.Scope, // part of the projection key: (device, scope, key)
		UpdatedAt:   updatedAt,
	}
	if v, ok := numericAttributeValue(request.ValueType, request.Value); ok {
		event.Value = v
	} else {
		// A non-numeric value/type overwriting a (possibly) numeric one: drop it from the
		// projection rather than leave a stale number behind.
		event.Removed = true
	}
	api.emitDeviceAttribute(ctx, event)
}

// dynamicThresholdEligible reports whether an attribute write is one event-processing
// tracks as a dynamic-threshold source (ADR-051 slice 4c-3): a DEVICE attribute in a
// platform-set scope (SHARED or SERVER). CLIENT (device-reported) is excluded so a
// device cannot define the threshold it is evaluated against.
func dynamicThresholdEligible(entityType, scope string) bool {
	if entity.Type(entityType) != entity.TypeDevice {
		return false
	}
	switch AttributeScope(scope) {
	case AttributeScopeShared, AttributeScopeServer:
		return true
	default:
		return false
	}
}

// normalizeAttributeValue enforces the storage-form invariant the facet-selector lowering
// (ADR-061 G3 §3.4) relies on: a stored value is always well-formed for its value_type, so
// the lowering's ea.value::numeric cast never faults and a bool facet leaf always matches a
// fixed literal. A LONG/DOUBLE is canonicalized to plain decimal, a BOOLEAN to
// 'true'/'false'. A nil value stays unset, and STRING/JSON pass through verbatim.
//
// 🔴 IT REFUSES A PRESENT VALUE ITS TYPE CANNOT HOLD; IT USED TO COERCE ONE TO UNSET, AND
// THAT WAS A SUCCESSFUL WRITE THAT STORED NOTHING. `setEntityAttribute` was the write that
// made a classification facet authorable from the console, and coercion made the failure
// mode of the whole feature invisible: the mutation returned the row, the caller reported
// "saved", and Browse matched nothing — with the panel still showing the text on screen
// because nothing told it the value had not been stored. There is no compensating benefit,
// because nothing depended on the coercion: SetEntityAttribute has exactly one non-test
// caller (graphql/mutations_attributes.go), so no ingest, projection or REACT path was
// relying on a lenient write to keep flowing.
//
// A nil value stays legal and still emits a dynamic-threshold removal downstream — that is
// how a caller CLEARS a numeric attribute, and it is a different act from writing text that
// does not parse. The "no stale number survives in the projection" guarantee is stronger
// under refusal than it was under coercion: the previous value stands, and the caller is
// told its write did not happen instead of being handed a silent removal.
//
// 🔴 THIS IS WHERE THE CLIENT'S REGEX AND THE SERVER'S PARSER DISAGREE, which is why the
// console's check is a friendly message and this is the gate. `1e400` matches any sane
// decimal pattern and ParseFloat returns +Inf with ErrRange; `12345678901234567890` is all
// digits and overflows int64. Both used to store something other than what was typed —
// nothing, and a different number, respectively.
func normalizeAttributeValue(valueType string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	switch AttributeValueType(valueType) {
	case AttributeValueLong, AttributeValueDouble:
		// Canonicalize, don't pass through: Go's ParseFloat accepts spellings Postgres
		// numeric rejects (hex floats like "0x1p2", and any future Go-syntax drift), so a
		// verbatim store would leave a LONG/DOUBLE row that faults ea.value::numeric at
		// resolve. Re-serializing the parsed number to plain decimal removes the whole class
		// and keeps LONG integer precision via a dedicated int path.
		return canonicalNumericText(valueType, value)
	case AttributeValueBoolean:
		b, err := strconv.ParseBool(*value)
		if err != nil {
			return nil, fmt.Errorf(
				"attribute value %q is not a BOOLEAN (expected true or false)", *value)
		}
		canonical := strconv.FormatBool(b)
		return &canonical, nil
	default:
		return value, nil
	}
}

// canonicalNumericText re-serializes a numeric attribute value to a plain-decimal form that
// Postgres numeric accepts, and refuses one that does not survive the round trip. A LONG
// integer goes through int64 to keep full precision; everything else (a LONG written as
// "3.0", a DOUBLE, a hex float) goes through the float path and emits a plain-decimal string
// ('f' format — no exponent, no hex, no NaN/Inf, which numericAttributeValue rejects).
//
// 🔴 A LONG THAT OVERFLOWS int64 IS REFUSED RATHER THAN FALLING THROUGH TO THE FLOAT PATH,
// which is what it used to do. "12345678901234567890" parses as a float perfectly well and
// re-serializes as 12345678901234567168 — a DIFFERENT NUMBER, stored with no error and no
// indication the value moved. Losing the low digits of an identifier-shaped LONG silently is
// worse than refusing it. A LONG spelled with a fractional part ("3.0") is still accepted,
// as it always was, because that loses nothing; the guard is on the range, not the spelling.
func canonicalNumericText(valueType string, value *string) (*string, error) {
	f, ok := numericAttributeValue(valueType, value)
	if !ok {
		return nil, fmt.Errorf(
			"attribute value %q is not a %s (a finite number)", *value, valueType)
	}
	if AttributeValueType(valueType) != AttributeValueLong {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		return &s, nil
	}
	// A LONG round-trips through int64 to keep full precision. Take the exact integer
	// whenever the text IS one, so a 19-digit value inside the range keeps every digit that
	// float64 could not hold; otherwise fall back to the float, which must be integral and
	// in range for the conversion to be lossless.
	if i, err := strconv.ParseInt(*value, 10, 64); err == nil {
		s := strconv.FormatInt(i, 10)
		return &s, nil
	}
	if f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
		return nil, fmt.Errorf(
			"attribute value %q is not a LONG (a whole number that fits in 64 bits)", *value)
	}
	s := strconv.FormatInt(int64(f), 10)
	return &s, nil
}

// numericAttributeValue parses an attribute value into a float64 when its declared type
// is numeric (DOUBLE or LONG) and the value is present and finite. A BOOLEAN, STRING, or
// JSON attribute, a nil value, or an unparseable/non-finite number yields ok=false — the
// caller then emits a removal so no stale numeric value survives in the projection.
func numericAttributeValue(valueType string, value *string) (float64, bool) {
	switch AttributeValueType(valueType) {
	case AttributeValueDouble, AttributeValueLong:
	default:
		return 0, false
	}
	if value == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(*value, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// EntityAttributes searches attributes. The owner is matched by the resolved
// (EntityType, EntityId); a caller-supplied Entity token is resolved first, and
// if it names no entity the search yields zero rows.
func (api *Api) EntityAttributes(ctx context.Context,
	criteria EntityAttributeSearchCriteria) (*EntityAttributeSearchResults, error) {
	// Resolve the owner token to an id up front (mirrors EntityRelationships).
	entityId := criteria.EntityId
	if criteria.Entity != nil && criteria.EntityType != nil {
		id, err := api.ResolveEntityToken(ctx, *criteria.EntityType, *criteria.Entity)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// No such entity: the search yields zero rows.
				return &EntityAttributeSearchResults{
					Results:    make([]EntityAttribute, 0),
					Pagination: rdb.SearchResultsPagination{},
				}, nil
			}
			return nil, err
		}
		entityId = &id
	}

	results := make([]EntityAttribute, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityAttribute{}, func(result *gorm.DB) *gorm.DB {
		if criteria.EntityType != nil {
			result = result.Where("entity_type = ?", *criteria.EntityType)
		}
		if entityId != nil {
			result = result.Where("entity_id = ?", *entityId)
		}
		if criteria.Scope != nil {
			result = result.Where("scope = ?", *criteria.Scope)
		}
		if criteria.AttrKeys != nil && len(*criteria.AttrKeys) > 0 {
			result = result.Where("attr_key in ?", *criteria.AttrKeys)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityAttributeSearchResults{Results: results, Pagination: pag}, nil
}

// DeleteEntityAttribute removes a single current-state attribute by its natural
// key (entity, scope, key). Returns whether a row was deleted. The delete is a
// gorm soft-delete (DeletedAt).
func (api *Api) DeleteEntityAttribute(ctx context.Context, entityType string,
	entity string, scope string, attrKey string) (bool, error) {
	entityId, err := api.ResolveEntityToken(ctx, entityType, entity)
	if err != nil {
		return false, err
	}
	// Stamp the removal time BEFORE the delete statement (not after), so a concurrent
	// re-create that commits later carries a strictly-later set-stamp and wins in the
	// projection's monotonic ordering rather than losing to a removal stamped after the
	// fact. This is a narrowing, not a linearization: single-process set/delete interleaves
	// on the same key can still stamp out of commit order (they self-heal on the next write
	// — the accepted monotonic-clock posture, as for the roster/profile-active projections).
	removedAt := time.Now().UTC()
	var rowsAffected int64
	// Same-txn membership recompute (ADR-062 S2): removing a SHARED-scope facet can drop
	// this entity out of a rule-scoped group, so the delete + the read-model recompute
	// commit together (a recompute failure rolls the delete back). Only wrap in a
	// transaction when the delete can affect membership; a CLIENT-scope delete runs bare.
	membershipTouched := false
	if membershipScopeEligible(entityType, scope) {
		err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Where(
				"entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
				entityType, entityId, scope, attrKey).Delete(&EntityAttribute{})
			if result.Error != nil {
				return result.Error
			}
			rowsAffected = result.RowsAffected
			if rowsAffected > 0 {
				t, err := api.recomputeMembershipForAttr(ctx, tx, entityType, entityId, attrKey)
				membershipTouched = t
				return err
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		// Evict the entity's membership cache post-commit (ADR-062) — only if a
		// rule-referenced group existed for the key.
		if membershipTouched {
			api.evictMemberships(ctx, entityType, []uint{entityId})
		}
	} else {
		result := api.RDB.DB(ctx).Where(
			"entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
			entityType, entityId, scope, attrKey).Delete(&EntityAttribute{})
		if result.Error != nil {
			return false, result.Error
		}
		rowsAffected = result.RowsAffected
	}

	if rowsAffected > 0 && dynamicThresholdEligible(entityType, scope) {
		// A tracked device attribute was actually removed: drop it from event-processing's
		// dynamic-threshold projection (ADR-051 slice 4c-3), scoped to the (device, scope, key)
		// this delete targeted so the sibling scope's value is untouched. The old value type is
		// not loaded (the delete is by natural key); a removal for a never-numeric key is a
		// harmless projection no-op.
		api.emitDeviceAttribute(ctx, &DeviceAttributeEvent{
			DeviceToken: entity,
			AttrKey:     attrKey,
			Scope:       scope,
			Removed:     true,
			UpdatedAt:   removedAt,
		})
	}
	return rowsAffected > 0, nil
}

// EntityAttributesByEntity loads all attributes for an entity (optionally scoped)
// without pagination. This is the direct loader the future device-state /
// shared-attribute-push path uses (ADR-012).
func (api *Api) EntityAttributesByEntity(ctx context.Context, entityType string,
	entityId uint, scope *string) ([]*EntityAttribute, error) {
	found := make([]*EntityAttribute, 0)
	result := api.RDB.DB(ctx).Where("entity_type = ? AND entity_id = ?", entityType, entityId)
	if scope != nil {
		result = result.Where("scope = ?", *scope)
	}
	if err := result.Find(&found).Error; err != nil {
		return nil, err
	}
	return found, nil
}
