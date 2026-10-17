// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Restoring and copying a device's configuration.
//
// 🔑 BOTH ARE SHARED-ATTRIBUTE WRITES, NOT REVISION WRITES. A device's configuration document
// is DERIVED: the declaration frozen into its profile's active version, applied to the
// device's SHARED attributes (computeDeviceConfiguration). A revision that was minted with
// someone else's content while the attributes stayed as they were would be contradicted by
// the very next read (`stale`) and overwritten by the next SHARED write, which recomputes
// from the attributes. So a restore or a copy writes the SHARED values of the device's
// declared keys to the target document — setting the keys the document carries, removing
// the declared keys it does not — and then mints exactly as an attribute write does, once,
// in the same transaction. History is never rewritten: the result is a NEW revision whose
// content equals the target (or nothing at all, when the device already holds it).

// CodeConfigurationKeyUndeclared: a restore or copy carries a key the target device's active
// profile version does not declare as configuration, so the target could not hold it.
const CodeConfigurationKeyUndeclared = "CONFIGURATION_KEY_UNDECLARED"

// CodeConfigurationConflict: a restore or copy wrote the device's declared keys but the
// document they now produce is not the one asked for, because another write to the same
// device's configuration committed while it ran. Nothing was applied; retrying is safe.
const CodeConfigurationConflict = "CONFIGURATION_CONFLICT"

// RestoreDeviceConfiguration makes the device's desired configuration equal to the content
// of one of its own past revisions, by minting a new revision. An unknown device or
// revision is gorm.ErrRecordNotFound.
func (api *Api) RestoreDeviceConfiguration(ctx context.Context, deviceToken string, revision int64) (*DeviceConfiguration, error) {
	deviceId, err := api.deviceIdByToken(ctx, deviceToken)
	if err != nil {
		return nil, err
	}
	rev, err := deviceConfigurationRevision(api.RDB.DB(ctx), deviceId, revision)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, fmt.Errorf("device %q has no configuration revision %d: %w", deviceToken, revision, gorm.ErrRecordNotFound)
	}
	if err := api.applyConfigurationDocument(ctx, deviceToken, deviceId, rev.Document); err != nil {
		return nil, err
	}
	return api.DeviceConfigurationByToken(ctx, deviceToken)
}

// deviceConfigurationRevision is one revision OF THIS DEVICE by number, or nil when the
// device has none with that number. The device predicate is what keeps another device's
// revision with the same number out of reach.
func deviceConfigurationRevision(db *gorm.DB, deviceId uint, revision int64) (*DeviceConfigurationRevision, error) {
	var rows []DeviceConfigurationRevision
	if err := db.Where("device_id = ? AND revision = ?", deviceId, revision).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// CopyDeviceConfiguration makes the target device's desired configuration equal to one of
// the source device's revisions: fromRevision when it is given, else the source's latest.
// A caller that previewed the copy passes the revision it previewed, so a source that
// changes between the preview and the copy cannot make the copy apply something other
// than what was shown. Both devices are resolved in the caller's tenant, so a device of
// another tenant is simply not found. A source with no revision (or without fromRevision)
// has nothing to copy and is refused as not found rather than copied as an empty
// document, which would clear every declared key on the target.
func (api *Api) CopyDeviceConfiguration(ctx context.Context, fromToken, toToken string, fromRevision *int64) (*DeviceConfiguration, error) {
	fromId, err := api.deviceIdByToken(ctx, fromToken)
	if err != nil {
		return nil, fmt.Errorf("source device %q: %w", fromToken, err)
	}
	toId, err := api.deviceIdByToken(ctx, toToken)
	if err != nil {
		return nil, fmt.Errorf("target device %q: %w", toToken, err)
	}
	var source *DeviceConfigurationRevision
	if fromRevision != nil {
		source, err = deviceConfigurationRevision(api.RDB.DB(ctx), fromId, *fromRevision)
	} else {
		source, err = latestConfigurationRevision(api.RDB.DB(ctx), fromId)
	}
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("source device %q has no configuration revision to copy: %w", fromToken, gorm.ErrRecordNotFound)
	}
	if err := api.applyConfigurationDocument(ctx, toToken, toId, source.Document); err != nil {
		return nil, err
	}
	return api.DeviceConfigurationByToken(ctx, toToken)
}

// configurationWrite is one SHARED attribute change a restore or copy makes, kept for the
// post-commit emits.
type configurationWrite struct {
	request *EntityAttributeSetRequest
	removed bool
	at      time.Time
}

// applyConfigurationDocument writes the device's declared SHARED attributes so that its
// configuration document becomes exactly document (a stored revision's canonical JSON),
// and mints the revision in the same transaction. It is all or nothing: a key the device
// does not declare, a value its declared type cannot carry, or a document over the size
// cap refuses the whole change with the configuration refusal codes and writes nothing.
//
// The order inside the transaction is the order a single attribute write uses
// (writeAttrThenRecompute, then reconcileDeviceConfigurationOnTx): the attribute rows, then
// the group-membership recompute, then the device's configuration lock, then the document.
// Taking the configuration lock in the same place as every other writer keeps the two locks
// in one order, so a restore cannot deadlock against a concurrent attribute write.
func (api *Api) applyConfigurationDocument(ctx context.Context, deviceToken string, deviceId uint, document string) error {
	target, err := parseConfigurationDocument(document)
	if err != nil {
		return err
	}
	deviceType := string(entity.TypeDevice)
	shared := string(AttributeScopeShared)
	writes := make([]configurationWrite, 0)
	var minted *DeviceConfigurationRevision
	var touched membershipChange

	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		declared, _, err := activeConfigurationDeclaration(tx, deviceId)
		if err != nil {
			return err
		}
		byKey := make(map[string]ConfigurationKey, len(declared))
		for _, k := range declared {
			byKey[k.Key] = k
		}
		undeclared := make([]string, 0)
		for key := range target {
			if _, ok := byKey[key]; !ok {
				undeclared = append(undeclared, key)
			}
		}
		if len(undeclared) > 0 {
			sort.Strings(undeclared)
			return &ConfigurationRefusal{Code: CodeConfigurationKeyUndeclared, Message: fmt.Sprintf(
				"device %q does not declare %s as configuration", deviceToken, quotedList(undeclared))}
		}

		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		now := time.Now()
		changed := make([]string, 0, len(keys))
		for _, key := range keys {
			decl := byKey[key]
			var existing []EntityAttribute
			if err := tx.Where("entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
				deviceType, deviceId, shared, key).Limit(1).Find(&existing).Error; err != nil {
				return err
			}
			raw, present := target[key]
			if !present {
				if len(existing) == 0 {
					continue
				}
				if err := tx.Where("entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
					deviceType, deviceId, shared, key).Delete(&EntityAttribute{}).Error; err != nil {
					return err
				}
				writes = append(writes, configurationWrite{request: &EntityAttributeSetRequest{
					EntityType: deviceType, Entity: deviceToken, Scope: shared, AttrKey: key}, removed: true, at: now})
			} else {
				text, err := configurationAttributeText(decl.ValueType, raw)
				if err != nil {
					return &ConfigurationRefusal{Code: CodeConfigurationValueType, Message: fmt.Sprintf(
						"the value of %q cannot be carried as the %s device %q declares for it", key, decl.ValueType, deviceToken)}
				}
				if len(existing) == 1 && existing[0].ValueType == decl.ValueType &&
					existing[0].Value.Valid && existing[0].Value.String == text {
					continue
				}
				if len(existing) == 1 {
					row := existing[0]
					row.ValueType = decl.ValueType
					row.Value = rdb.NullStrOf(&text)
					row.LastUpdated = now
					if err := tx.Save(&row).Error; err != nil {
						return err
					}
				} else {
					row := &EntityAttribute{EntityType: deviceType, EntityId: deviceId, Scope: shared,
						AttrKey: key, ValueType: decl.ValueType, Value: rdb.NullStrOf(&text), LastUpdated: now}
					if err := tx.Create(row).Error; err != nil {
						return err
					}
				}
				value := text
				writes = append(writes, configurationWrite{request: &EntityAttributeSetRequest{
					EntityType: deviceType, Entity: deviceToken, Scope: shared, AttrKey: key,
					ValueType: decl.ValueType, Value: &value}, at: now})
			}
			changed = append(changed, key)
		}
		// Group membership is recomputed once every attribute row is written, not between
		// them: a recompute between two row writes would hold membership rows while waiting
		// for the next attribute row, the reverse of the order a concurrent single attribute
		// write takes them in (its row, then membership), which is a lock cycle.
		if membershipScopeEligible(deviceType, shared) {
			for _, key := range changed {
				c, err := api.recomputeMembershipForAttr(ctx, tx, deviceType, deviceId, key)
				touched = touched.or(c)
				if err != nil {
					return err
				}
			}
		}

		if err := lockDeviceConfiguration(ctx, tx, deviceId); err != nil {
			return err
		}
		doc, versionId, err := computeDeviceConfiguration(tx, deviceId)
		if err != nil {
			return err
		}
		if len(doc.Canonical) > MaxConfigurationDocumentBytes {
			return &ConfigurationRefusal{Code: CodeConfigurationTooLarge, Message: fmt.Sprintf(
				"the device's configuration document would be %d bytes, over the %d byte limit",
				len(doc.Canonical), MaxConfigurationDocumentBytes)}
		}
		// The document now built must BE the target, or a revision minted from it would be a
		// restore that restored something else. The one way it differs in practice is a
		// concurrent write to one of the device's declared keys committing between this
		// transaction's reads and its configuration lock; that is refused with a code of its
		// own, which a caller can retry, rather than as an internal error.
		if !bytes.Equal(doc.Canonical, []byte(document)) {
			return &ConfigurationRefusal{Code: CodeConfigurationConflict, Message: fmt.Sprintf(
				"the configuration of device %q changed while it was being written; nothing was applied, try again",
				deviceToken)}
		}
		rev, isNew, err := mintConfigurationRevision(tx, deviceId, doc, versionId, configurationActor(ctx))
		if err != nil {
			return err
		}
		if isNew {
			minted = rev
		}
		return queueAccessChangeOnTx(tx, deviceType, deviceId, touched)
	})
	if err != nil {
		return err
	}
	api.publishMintedConfiguration(ctx, deviceId, minted)
	api.afterMembershipRecompute(ctx, deviceType, deviceId, touched)
	for _, w := range writes {
		if w.removed {
			api.emitDeviceAttribute(ctx, &DeviceAttributeEvent{DeviceToken: deviceToken, AttrKey: w.request.AttrKey,
				Scope: shared, Removed: true, UpdatedAt: w.at.UTC()})
			continue
		}
		api.emitDeviceAttributeForSet(ctx, w.request, w.at)
	}
	return nil
}

// parseConfigurationDocument reads a stored revision's document into its keys and raw values.
func parseConfigurationDocument(document string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(document), &out); err != nil {
		return nil, fmt.Errorf("a stored configuration document did not decode: %w", err)
	}
	return out, nil
}

// configurationAttributeText is the inverse of canonicalConfigurationValue: the stored SHARED
// attribute text that renders, as valueType, to exactly raw. It is checked by rendering it
// back, so a value that would come out different is refused rather than written.
func configurationAttributeText(valueType string, raw json.RawMessage) (string, error) {
	var text string
	switch AttributeValueType(valueType) {
	case AttributeValueString:
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", errConfigurationValue
		}
	case AttributeValueLong, AttributeValueDouble, AttributeValueBoolean, AttributeValueJson:
		text = string(raw)
	default:
		return "", errConfigurationValue
	}
	normalized, err := normalizeAttributeValue(valueType, &text)
	if err != nil {
		return "", errConfigurationValue
	}
	rendered, err := canonicalConfigurationValue(valueType, valueType, *normalized)
	if err != nil || !bytes.Equal(rendered, raw) {
		return "", errConfigurationValue
	}
	return *normalized, nil
}

func quotedList(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = strconv.Quote(k)
	}
	return strings.Join(q, ", ")
}
