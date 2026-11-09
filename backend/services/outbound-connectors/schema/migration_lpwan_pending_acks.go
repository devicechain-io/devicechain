// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// lpwanPendingAck is the table the LoRaWAN acknowledgement feed keeps its pending confirmed
// downlinks in, as it was when NewLpwanPendingAcksSchema was written. Private to this migration
// and NOT the live model (lpwan.LpwanPendingAck): the live one is free to grow columns in later
// migrations without rewriting this one.
//
// No TableName, on purpose, for the reason baseline_snapshot.go gives: gorm derives
// "lpwan_pending_acks" from the type name and applies the area's table prefix to a derived name only.
type lpwanPendingAck struct {
	ID        uint `gorm:"primaryKey"`
	CreatedAt time.Time

	TenantId      string    `gorm:"not null;size:128;uniqueIndex:uix_lpwan_pending_acks_item,priority:1"`
	Integration   string    `gorm:"not null;size:128"`
	DeviceToken   string    `gorm:"not null;size:128"`
	DevEUI        string    `gorm:"column:dev_eui;not null;size:16"`
	CommandToken  string    `gorm:"not null;size:128"`
	DispatchNonce string    `gorm:"not null;size:128"`
	QueueItemID   string    `gorm:"not null;size:64;uniqueIndex:uix_lpwan_pending_acks_item,priority:2"`
	DeadlineAt    time.Time `gorm:"not null;index"`
	TransmittedAt *time.Time
	// SettlingUntil is the lease a settler holds while it answers the command: NULL when nobody is.
	SettlingUntil *time.Time
}

// NewLpwanPendingAcksSchema adds the table behind confirmed LoRaWAN downlinks: one row per
// downlink the network server accepted and the device has not yet acknowledged, deleted when its
// acknowledgement (or its deadline) answers the command.
//
// A NEW table, so a replica of the previous version, which neither reads nor writes it, shares the
// database with a new one safely during a rolling upgrade. Re-runnable: AutoMigrate creates only
// what is missing.
func NewLpwanPendingAcksSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261109120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&lpwanPendingAck{})
		},
		// Nothing is dropped: a rollback undoes a failed forward migration rather than erasing rows
		// that were written after it.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
