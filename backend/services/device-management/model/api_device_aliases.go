// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/devicealias"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"gorm.io/gorm"
)

func invalidDeviceAlias(format string, args ...any) error {
	return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf(format, args...))
}

// aliasListOf reads an optional alias list as it arrives on a create or an import row: omitted
// and null are both "no aliases".
func aliasListOf(p *[]DeviceAliasInput) []DeviceAliasInput {
	if p == nil {
		return nil
	}
	return *p
}

// normalizeAliasInputs validates every alias of one device and returns them in their canonical
// form, in the order given. It refuses a scheme or value outside the vocabulary, a second value
// for one scheme (a device has one per scheme), and more than devicealias.MaxPerDevice.
//
// 🔴 NORMALIZATION IS ON THE WRITE, NOT ON THE READ. The stored value is the canonical spelling,
// so a lookup is plain equality on an indexed column, and two spellings of one DevEUI are the
// same alias — which is what makes "a different-case duplicate" a collision the unique index
// refuses rather than two rows a sender might reach either of.
func normalizeAliasInputs(in []DeviceAliasInput) ([]DeviceAliasInput, error) {
	if len(in) > devicealias.MaxPerDevice {
		return nil, limit.Exceeded("aliases on one device", len(in), devicealias.MaxPerDevice)
	}
	out := make([]DeviceAliasInput, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, a := range in {
		value, err := devicealias.Normalize(a.Scheme, a.Value)
		if err != nil {
			return nil, invalidDeviceAlias("%s", err.Error())
		}
		if seen[a.Scheme] {
			return nil, invalidDeviceAlias("a device has one alias per scheme, and %s appears twice", a.Scheme)
		}
		seen[a.Scheme] = true
		out = append(out, DeviceAliasInput{Scheme: a.Scheme, Value: value})
	}
	return out, nil
}

// sortedAliases returns the aliases ordered by (scheme, value). Every writer of the alias table
// takes its row and index locks in that order, so two transactions writing overlapping sets
// queue behind each other instead of each holding what the other needs.
//
// The inversion that remains is inherent: two devices SWAPPING values (A takes B's, B takes A's)
// each need a value the other still holds until it commits, and no order of writes can avoid
// that. One of them loses to the unique index with a CONFLICT (or the database's deadlock
// detector); it is a retry, never a corrupt row.
func sortedAliases(in []DeviceAliasInput) []DeviceAliasInput {
	out := append([]DeviceAliasInput(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scheme != out[j].Scheme {
			return out[i].Scheme < out[j].Scheme
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// clearSightingsOnTx removes the unregistered-device sightings of values that now name a device,
// in every integration of the tenant: a sighting is the invitation to create the device, and the
// device now exists. Without it the list would never empty and its cap would become permanent
// after a large onboarding.
func clearSightingsOnTx(tx *gorm.DB, aliases []DeviceAliasInput) error {
	for _, a := range aliases {
		if err := tx.Where("scheme = ? AND value = ?", a.Scheme, a.Value).Delete(&IntegrationUnregisteredDevice{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// insertAliasesOnTx writes the (already normalized) aliases of a device that has none, in
// (scheme, value) order. A value another device in the tenant holds under the scheme violates the
// unique index, which the caller's transaction surfaces as a CONFLICT. The values' sightings are
// cleared with it.
func insertAliasesOnTx(tx *gorm.DB, deviceId uint, aliases []DeviceAliasInput) error {
	// One statement per alias, not one batch: the audit journal labels a row only when a single
	// struct is written, and a device carries at most devicealias.MaxPerDevice of them.
	ordered := sortedAliases(aliases)
	for _, a := range ordered {
		row := DeviceAlias{DeviceId: deviceId, Scheme: a.Scheme, Value: a.Value}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return clearSightingsOnTx(tx, ordered)
}

// replaceAliasesOnTx makes a device's alias set exactly aliases (already normalized). Deletes
// come first, then the updates and inserts in (scheme, value) order (see sortedAliases): rows
// whose scheme is not in the set are removed, a scheme whose value changed is updated in place,
// a new scheme is inserted, and a row that already holds the right value is not written at all.
func replaceAliasesOnTx(tx *gorm.DB, deviceId uint, aliases []DeviceAliasInput) error {
	var current []DeviceAlias
	if err := tx.Where("device_id = ?", deviceId).Order("scheme ASC, value ASC").Find(&current).Error; err != nil {
		return err
	}
	want := make(map[string]string, len(aliases))
	for _, a := range aliases {
		want[a.Scheme] = a.Value
	}
	have := make(map[string]DeviceAlias, len(current))
	for _, c := range current {
		have[c.Scheme] = c
		if _, keep := want[c.Scheme]; !keep {
			if err := tx.Delete(&DeviceAlias{}, c.ID).Error; err != nil {
				return err
			}
		}
	}
	var written []DeviceAliasInput
	for _, a := range sortedAliases(aliases) {
		existing, ok := have[a.Scheme]
		switch {
		case !ok:
			row := DeviceAlias{DeviceId: deviceId, Scheme: a.Scheme, Value: a.Value}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			written = append(written, a)
		case existing.Value != a.Value:
			// The loaded row is handed to the statement, so the journal names the row it changed.
			if err := tx.Model(&existing).Update("value", a.Value).Error; err != nil {
				return err
			}
			written = append(written, a)
		}
	}
	return clearSightingsOnTx(tx, written)
}

// deleteAliasesOnTx removes every alias of a device. The foreign key cascades in PostgreSQL;
// this is the explicit statement, so the delete does not depend on the database being the one
// that enforces it.
func deleteAliasesOnTx(tx *gorm.DB, deviceId uint) error {
	return tx.Where("device_id = ?", deviceId).Delete(&DeviceAlias{}).Error
}

// DeviceAliasesByDevice returns the aliases of each of the devices, keyed by device id. A device
// with none has no entry.
func (api *Api) DeviceAliasesByDevice(ctx context.Context, deviceIds []uint) (map[uint][]DeviceAlias, error) {
	out := make(map[uint][]DeviceAlias, len(deviceIds))
	if len(deviceIds) == 0 {
		return out, nil
	}
	for start := 0; start < len(deviceIds); start += aliasLookupChunk {
		end := min(start+aliasLookupChunk, len(deviceIds))
		var rows []DeviceAlias
		if err := api.RDB.DB(ctx).Where("device_id IN ?", deviceIds[start:end]).Order("scheme ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.DeviceId] = append(out[r.DeviceId], r)
		}
	}
	return out, nil
}

// aliasLookupChunk bounds the key list of one alias read, so an import of a thousand devices is
// a handful of statements with bounded parameter lists.
const aliasLookupChunk = 500

// aliasHolders returns the device that holds each of the (already normalized) aliases, keyed by
// aliasKey. An alias nobody holds has no entry.
func (api *Api) aliasHolders(ctx context.Context, aliases []DeviceAliasInput) (map[string]uint, error) {
	out := make(map[string]uint, len(aliases))
	byScheme := make(map[string][]string)
	for _, a := range aliases {
		byScheme[a.Scheme] = append(byScheme[a.Scheme], a.Value)
	}
	for scheme, values := range byScheme {
		for start := 0; start < len(values); start += aliasLookupChunk {
			end := min(start+aliasLookupChunk, len(values))
			var rows []DeviceAlias
			if err := api.RDB.DB(ctx).Where("scheme = ? AND value IN ?", scheme, values[start:end]).Find(&rows).Error; err != nil {
				return nil, err
			}
			for _, r := range rows {
				out[aliasKey(r.Scheme, r.Value)] = r.DeviceId
			}
		}
	}
	return out, nil
}

// DeviceHoldsAlias reports whether the device holds an alias under scheme whose value is the
// canonical form of value, read from the database as it is now. A value the scheme refuses is
// not held by anyone.
func (api *Api) DeviceHoldsAlias(ctx context.Context, deviceId uint, scheme, value string) (bool, error) {
	canonical, err := devicealias.Normalize(scheme, value)
	if err != nil {
		return false, nil
	}
	var n int64
	if err := api.RDB.DB(ctx).Model(&DeviceAlias{}).
		Where("device_id = ? AND scheme = ? AND value = ?", deviceId, scheme, canonical).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// aliasKey is the map key of a (scheme, value) pair.
func aliasKey(scheme, value string) string { return scheme + "\x00" + value }

// DeviceAliases returns one device's aliases, ordered by scheme.
func (api *Api) DeviceAliases(ctx context.Context, deviceId uint) ([]DeviceAlias, error) {
	byDevice, err := api.DeviceAliasesByDevice(ctx, []uint{deviceId})
	if err != nil {
		return nil, err
	}
	return byDevice[deviceId], nil
}

// ------------------------------------------------------------------------------------------
// Resolution for an inbound integration
// ------------------------------------------------------------------------------------------

// ResolveIntegrationDevice is the one call an ingest route makes to turn a sender's identifier
// into a device. It authenticates the integration, finds the device the alias names, checks the
// device's type is in the integration's scope, and, when no device in scope carries the alias,
// records the sighting.
//
// The caller is a SERVICE (the resolver authorizes that before calling), and the answer is a
// value rather than an error for every outcome a sender can cause:
//
//   - UNAUTHENTICATED: no such integration, or the token did not match (one answer to both).
//   - DISABLED: the token matched an integration an operator switched off. It is reported
//     apart from a credential failure so a caller does not charge it to the bad-credential
//     budget, and nothing is resolved or recorded.
//   - UNREGISTERED: no device in scope carries the alias. A device that carries it but is OUT of
//     scope reads identically, so a token that leaks, or a sender that is wrong about an id,
//     learns nothing about a device it may not reach.
//   - RESOLVED: the device's token and type token.
//
// A scheme other than the one the integration's identity names, or a value the scheme refuses,
// is an INVALID_VALUE error: the sender's request is malformed, which is not an outcome.
//
// 🔴 THE SCOPE IS CHECKED HERE, AGAINST THE ROW AS IT IS NOW, and is checked again when the
// event reaches the resolver. A cache in front of this call can be stale; neither check trusts
// it.
func (api *Api) ResolveIntegrationDevice(ctx context.Context, integrationToken, presented, scheme, value string) (*IntegrationDeviceResolution, error) {
	integration, err := api.AuthenticateIntegration(ctx, integrationToken, presented)
	switch {
	case err == nil:
	case errors.Is(err, ErrIntegrationDisabled):
		return &IntegrationDeviceResolution{Outcome: IntegrationDeviceDisabled}, nil
	case errors.Is(err, ErrIntegrationAuthFailed):
		return &IntegrationDeviceResolution{Outcome: IntegrationDeviceUnauthenticated}, nil
	default:
		return nil, err
	}
	want := integrationIdentityScheme(integration.Identity)
	if want == "" || scheme != want {
		return nil, invalidDeviceAlias("this integration resolves devices by %s, not %q", want, clipScheme(scheme))
	}
	canonical, err := devicealias.Normalize(scheme, value)
	if err != nil {
		return nil, invalidDeviceAlias("%s", err.Error())
	}

	var alias DeviceAlias
	err = api.RDB.DB(ctx).Where("scheme = ? AND value = ?", scheme, canonical).First(&alias).Error
	switch {
	case err == nil:
		devices, err := api.DevicesById(ctx, []uint{alias.DeviceId})
		if err != nil {
			return nil, err
		}
		if len(devices) == 1 && integration.ScopeHas(devices[0].DeviceTypeId) {
			typeToken := ""
			if devices[0].DeviceType != nil {
				typeToken = devices[0].DeviceType.Token
			}
			// The device exists now, so a sighting of this value is stale: drop it.
			if err := api.RDB.DB(ctx).Where("integration_id = ? AND scheme = ? AND value = ?",
				integration.ID, scheme, canonical).Delete(&IntegrationUnregisteredDevice{}).Error; err != nil {
				return nil, err
			}
			return &IntegrationDeviceResolution{
				Outcome: IntegrationDeviceResolved, DeviceToken: devices[0].Token, DeviceTypeToken: typeToken,
			}, nil
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return nil, err
	}

	// A value some device already holds (one outside this integration's scope) is not an
	// unregistered device: nothing is recorded for it.
	if err == nil {
		return &IntegrationDeviceResolution{Outcome: IntegrationDeviceUnregistered}, nil
	}
	overflow, err := api.recordUnregistered(ctx, integration, scheme, canonical)
	if err != nil {
		return nil, err
	}
	return &IntegrationDeviceResolution{Outcome: IntegrationDeviceUnregistered, Overflow: overflow}, nil
}

func clipScheme(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// recordUnregistered upserts the sighting of a sender-named device the tenant has no device in
// scope for: a known (integration, scheme, value) gets its last-seen time and count refreshed;
// a new one is inserted unless the integration already records MaxUnregisteredPerIntegration
// distinct devices, in which case nothing is stored and overflow is true.
//
// 🔴 THE CAP IS EXACT, NOT APPROXIMATE. A new row is counted and inserted under a per-integration
// advisory lock (PostgreSQL), so two sightings racing at the cap cannot both land. The refresh
// of a known row takes no lock: it writes one row and cannot grow the table.
func (api *Api) recordUnregistered(ctx context.Context, integration *Integration, scheme, value string) (overflow bool, err error) {
	now := time.Now().UTC()
	refresh := func(tx *gorm.DB) (bool, error) {
		res := tx.Model(&IntegrationUnregisteredDevice{}).
			Where("integration_id = ? AND scheme = ? AND value = ?", integration.ID, scheme, value).
			Updates(map[string]any{"last_seen": now, "count": gorm.Expr("count + 1")})
		return res.RowsAffected > 0, res.Error
	}
	if known, err := refresh(api.RDB.DB(ctx)); err != nil || known {
		return false, err
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockUnregisteredInserts(tx, integration.ID); err != nil {
			return err
		}
		// Another sighting of the same value may have landed while this one waited.
		if known, err := refresh(tx); err != nil || known {
			return err
		}
		var n int64
		if err := notHeldByAnAlias(tx, tx.Model(&IntegrationUnregisteredDevice{})).
			Where("integration_id = ?", integration.ID).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxUnregisteredPerIntegration {
			overflow = true
			return nil
		}
		return tx.Create(&IntegrationUnregisteredDevice{
			IntegrationId: integration.ID, Scheme: scheme, Value: value, FirstSeen: now, LastSeen: now, Count: 1,
		}).Error
	})
	return overflow, err
}

// lockUnregisteredInserts serializes the inserts of one integration's sightings for the rest of
// the transaction, so the count the cap is checked against is the count the insert lands on.
// Anywhere but PostgreSQL (the SQLite unit-test database, which has one writer at a time) there
// is nothing to take.
func lockUnregisteredInserts(tx *gorm.DB, integrationId uint) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
		fmt.Sprintf("unregistered-devices:%d", integrationId)).Error
}

// UnregisteredIntegrationDevices pages the sender-named devices an integration has seen and the
// tenant has no device in scope for, most recently seen first. An unknown integration is an
// empty page.
func (api *Api) UnregisteredIntegrationDevices(ctx context.Context, criteria UnregisteredDeviceSearchCriteria) (*UnregisteredDeviceSearchResults, error) {
	found, err := api.IntegrationsByToken(ctx, []string{criteria.Integration})
	if err != nil {
		return nil, err
	}
	results := make([]IntegrationUnregisteredDevice, 0)
	db, pag := api.RDB.ListOf(ctx, &IntegrationUnregisteredDevice{}, func(result *gorm.DB) *gorm.DB {
		if len(found) == 0 {
			return result.Where("1 = 0")
		}
		return notHeldByAnAlias(api.RDB.DB(ctx), result).Where("integration_id = ?", found[0].ID)
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	for i := range results {
		results[i].IntegrationToken = criteria.Integration
	}
	return &UnregisteredDeviceSearchResults{Results: results, Pagination: pag}, nil
}

// notHeldByAnAlias narrows a statement over integration_unregistered_devices to the values no
// device holds as an alias. A value some device carries is not "unregistered", whatever a stale
// row says, so it is neither listed nor counted against the cap.
func notHeldByAnAlias(db *gorm.DB, stmt *gorm.DB) *gorm.DB {
	return stmt.Where("NOT EXISTS (?)", db.Session(&gorm.Session{NewDB: true}).Model(&DeviceAlias{}).Select("1").
		Where("device_aliases.scheme = integration_unregistered_devices.scheme AND device_aliases.value = integration_unregistered_devices.value"))
}

// ClearUnregisteredIntegrationDevices removes every sighting an integration has recorded and
// returns how many rows went. The sightings are observations, so an operator who has dealt with
// them (or wants the cap's room back) clears them; ones still being sent are recorded again. An
// unknown integration clears nothing.
func (api *Api) ClearUnregisteredIntegrationDevices(ctx context.Context, integrationToken string) (int64, error) {
	found, err := api.IntegrationsByToken(ctx, []string{integrationToken})
	if err != nil || len(found) == 0 {
		return 0, err
	}
	res := api.RDB.DB(ctx).Where("integration_id = ?", found[0].ID).Delete(&IntegrationUnregisteredDevice{})
	return res.RowsAffected, res.Error
}
