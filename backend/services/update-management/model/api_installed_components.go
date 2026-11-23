// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm/clause"
)

// Sources of an installed-version observation.
const (
	InstalledSourceInventory = "INVENTORY"
	InstalledSourceUpdated   = "UPDATED"
)

// DeviceInstalledComponent is the version a device last said it was running for one component, for
// one incarnation of the device. It is the data a fleet-wide "which firmware are we on" query reads;
// nothing serves it yet.
type DeviceInstalledComponent struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"not null;size:128;uniqueIndex:uix_installed_component,priority:1;index:idx_installed_component_version,priority:1"`

	DeviceToken       string `gorm:"not null;size:128;uniqueIndex:uix_installed_component,priority:2;index"`
	DeviceIncarnation int64  `gorm:"not null;type:bigint;uniqueIndex:uix_installed_component,priority:3"`
	Component         string `gorm:"not null;size:64;uniqueIndex:uix_installed_component,priority:4;index:idx_installed_component_version,priority:2"`

	Version    string    `gorm:"not null;size:128;index:idx_installed_component_version,priority:3"`
	Digest     string    `gorm:"not null;size:71;default:''"`
	ObservedAt time.Time `gorm:"not null"`
	Source     string    `gorm:"not null;size:16;check:chk_installed_component_source,source IN ('INVENTORY','UPDATED')"`
}

func (DeviceInstalledComponent) TableName() string { return "device_installed_components" }

// AuditExempt: an observation a device made, one row rewritten per report, is a data-plane fact.
func (DeviceInstalledComponent) AuditExempt() bool { return true }

// recordInstalled stores what a device reported running. The newer observation wins, so a report that
// arrives late cannot move the version back; an equal timestamp keeps the row as it is. A replaced
// device (another incarnation) has its own rows and starts with none.
func (api *Api) recordInstalled(ctx context.Context, deviceToken string, incarnation uint64, component, version, digest string, observed time.Time, source string) error {
	if incarnation == 0 {
		return errors.New("an installed version needs the device's incarnation")
	}
	if _, ok := core.TenantFromContext(ctx); !ok {
		return errors.New("an installed version needs a tenant")
	}
	row := &DeviceInstalledComponent{DeviceToken: deviceToken, DeviceIncarnation: int64(incarnation), Component: component,
		Version: version, Digest: digest, ObservedAt: observed, Source: source}
	return api.RDB.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "device_incarnation"}, {Name: "component"}},
		DoUpdates: clause.AssignmentColumns([]string{"version", "digest", "observed_at", "source", "updated_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "device_installed_components.observed_at < excluded.observed_at"}}},
	}).Create(row).Error
}

// InstalledComponents lists what a device last reported, newest incarnation first.
func (api *Api) InstalledComponents(ctx context.Context, deviceToken string) ([]DeviceInstalledComponent, error) {
	var rows []DeviceInstalledComponent
	err := api.RDB.DB(ctx).Where("device_token = ?", deviceToken).Order("device_incarnation DESC, component ASC").Find(&rows).Error
	return rows, err
}

// forgetInstalled removes what a deleted device reported: every incarnation up to the one deleted, or,
// for a deletion fact that names none, every observation made before the deletion.
func (api *Api) forgetInstalled(ctx context.Context, token string, incarnation uint64, deleted time.Time) error {
	q := api.RDB.DB(ctx).Where("device_token = ?", token)
	if incarnation > 0 {
		q = q.Where("device_incarnation <= ?", incarnation)
	} else {
		q = q.Where("observed_at <= ?", deleted)
	}
	return q.Delete(&DeviceInstalledComponent{}).Error
}
