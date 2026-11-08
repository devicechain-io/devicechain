// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The entity families a position can be installed on.
const (
	PositionFamilyDevice = "DEVICE"
	PositionFamilyAsset  = "ASSET"
)

// ParsePositionFamily maps the wire spelling to a family and refuses anything else.
func ParsePositionFamily(s string) (string, error) {
	switch f := strings.ToUpper(strings.TrimSpace(s)); f {
	case PositionFamilyDevice, PositionFamilyAsset:
		return f, nil
	}
	return "", fmt.Errorf("%w: family %q is not DEVICE or ASSET", ErrInvalidInstalledPosition, s)
}

// SetInstalledPosition records where a device or an asset was installed, and returns the stored
// position. The position is validated BEFORE the database is touched. The entity is found through
// the caller's scope, so one outside it is NOT_FOUND, indistinguishable from one that is not there.
//
// The write is one statement naming exactly the installed columns (the rest of the row is never
// written, and a concurrent edit of the entity's other fields is not overwritten); the cached
// copies of a device are dropped after it commits.
func (api *Api) SetInstalledPosition(ctx context.Context, family, token string, in InstalledPositionInput) (*InstalledPosition, error) {
	family, err := ParsePositionFamily(family)
	if err != nil {
		return nil, err
	}
	source, err := in.validate()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := api.writeInstalledColumns(ctx, family, token, in.installedColumns(source, now)); err != nil {
		return nil, err
	}
	// The answer is what THIS call wrote, never a reread (see written).
	return in.written(source, now), nil
}

// ClearInstalledPosition removes the installed position. Clearing an entity that has none is not an
// error: the caller asked for the state that already holds.
func (api *Api) ClearInstalledPosition(ctx context.Context, family, token string) error {
	family, err := ParsePositionFamily(family)
	if err != nil {
		return err
	}
	return api.writeInstalledColumns(ctx, family, token, clearedInstalledColumns())
}

func (api *Api) writeInstalledColumns(ctx context.Context, family, token string, cols map[string]any) error {
	switch family {
	case PositionFamilyDevice:
		found, err := api.DevicesByToken(ctx, []string{token})
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return gorm.ErrRecordNotFound
		}
		d := found[0]
		if err := refuseScopedWrite(ctx); err != nil {
			return err
		}
		if err := writeInstalledOnTx(api.RDB.DB(ctx), &Device{}, d.ID, cols); err != nil {
			return err
		}
		api.evictDeviceCredentials(ctx, d.TenantId, d.ID)
		api.evictDeviceByToken(ctx, d.Token)
		return nil
	case PositionFamilyAsset:
		found, err := api.AssetsByToken(ctx, []string{token})
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return gorm.ErrRecordNotFound
		}
		a := found[0]
		if err := refuseScopedWrite(ctx); err != nil {
			return err
		}
		return writeInstalledOnTx(api.RDB.DB(ctx), &Asset{}, a.ID, cols)
	}
	panic("writeInstalledColumns: family " + family + " was not parsed")
}

// refuseScopedWrite refuses a caller whose access is limited to part of the tenant. It runs AFTER
// the entity is looked up through the scope, so an entity outside it is NOT_FOUND (indistinguishable
// from a missing one) and only an entity the caller can see reaches this refusal. A scoped member
// reads positions; the stores refuse a scoped update of these tables, and this says so by name
// instead of letting the statement fail.
func refuseScopedWrite(ctx context.Context) error {
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		return auth.ErrScopeUnsupported
	}
	return nil
}

// writeInstalledOnTx is the ONE statement that writes the installed columns. They are `<-:false`
// on the model, which gorm honours by dropping them from every struct Save and from a map Update
// alike (a map update filters on the same permission), so the SET list is supplied as a clause: it
// is used as given, and the tenant and scope predicates still apply because the statement is a
// Model(...) update that runs through the registered callbacks.
func writeInstalledOnTx(tx *gorm.DB, model any, id uint, cols map[string]any) error {
	names := make([]string, 0, len(cols)+1)
	for name := range cols {
		names = append(names, name)
	}
	sort.Strings(names)
	set := make(clause.Set, 0, len(names)+1)
	for _, name := range names {
		set = append(set, clause.Assignment{Column: clause.Column{Name: name}, Value: cols[name]})
	}
	set = append(set, clause.Assignment{Column: clause.Column{Name: "updated_at"}, Value: time.Now()})
	res := tx.Model(model).Where("id = ?", id).Clauses(set).Updates(map[string]any{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		// The entity was found a moment ago; if no row took the write it was deleted or moved out of
		// the caller's scope in between, which reads as a miss, never as a stored position.
		return gorm.ErrRecordNotFound
	}
	return nil
}
