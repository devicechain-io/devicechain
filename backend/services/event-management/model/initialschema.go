// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Creates the initial schema migration for this functional area.
func NewInitialSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20220420000000",
		Migrate: func(tx *gorm.DB) error {
			// Location event fields.
			type LocationEvent struct {
				rdb.TenantScoped
				DeviceId     uint              `gorm:"not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				Event        Event             `gorm:"foreignKey:DeviceId,EventType,OccurredTime;References:DeviceId,EventType,OccurredTime"`
				Latitude     sql.NullFloat64   `gorm:"type:decimal(10,8);"`
				Longitude    sql.NullFloat64   `gorm:"type:decimal(11,8);"`
				Elevation    sql.NullFloat64   `gorm:"type:decimal(10,8);"`
			}

			// Measurement event fields.
			type MeasurementEvent struct {
				rdb.TenantScoped
				DeviceId     uint              `gorm:"not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				Event        Event             `gorm:"foreignKey:DeviceId,EventType,OccurredTime;References:DeviceId,EventType,OccurredTime"`
				Name         string            `gorm:"not null"`
				Value        sql.NullFloat64   `gorm:"type:decimal(20,8);"`
				Classifier   *uint
			}

			// Alert event fields.
			type AlertEvent struct {
				rdb.TenantScoped
				DeviceId     uint              `gorm:"not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				Event        Event             `gorm:"foreignKey:DeviceId,EventType,OccurredTime;References:DeviceId,EventType,OccurredTime"`
				Type         string            `gorm:"not null"`
				Level        uint32            `gorm:"not null"`
				Message      string
				Source       string
			}

			// Base event fields.
			type Event struct {
				rdb.TenantScoped
				DeviceId        uint              `gorm:"primaryKey"`
				EventType       esmodel.EventType `gorm:"primaryKey"`
				OccurredTime    time.Time         `gorm:"primaryKey"`
				AssignmentId    uint              `gorm:"not null"`
				Source          string
				AltId           sql.NullString
				DeviceGroupId   sql.NullInt64
				CustomerId      *uint
				CustomerGroupId *uint
				AreaId          *uint
				AreaGroupId     *uint
				AssetId         *uint
				AssetGroupId    *uint
				ProcessedTime   time.Time
			}

			err := tx.AutoMigrate(&Event{}, &LocationEvent{}, &MeasurementEvent{}, &AlertEvent{})
			if err != nil {
				return err
			}

			// Convert to a hypertable.
			err = tx.Raw("SELECT create_hypertable('event-management.events', 'occurred_time');").Row().Err()
			if err != nil {
				return err
			}

			// Add index on device id.
			tx.Exec("CREATE INDEX ON \"event-management\".\"events\" (device_id, occurred_time DESC);")
			if tx.Error != nil {
				return tx.Error
			}

			// Add composite index for tenant-scoped time-series queries.
			tx.Exec("CREATE INDEX ON \"event-management\".\"events\" (tenant_id, occurred_time DESC);")
			if tx.Error != nil {
				return tx.Error
			}

			// Add composite tenant-scoped time-series indexes on the typed event tables.
			tx.Exec("CREATE INDEX ON \"event-management\".\"location_events\" (tenant_id, occurred_time DESC);")
			if tx.Error != nil {
				return tx.Error
			}
			tx.Exec("CREATE INDEX ON \"event-management\".\"measurement_events\" (tenant_id, occurred_time DESC);")
			if tx.Error != nil {
				return tx.Error
			}
			tx.Exec("CREATE INDEX ON \"event-management\".\"alert_events\" (tenant_id, occurred_time DESC);")
			if tx.Error != nil {
				return tx.Error
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}
}
