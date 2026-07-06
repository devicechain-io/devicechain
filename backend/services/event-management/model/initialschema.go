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
			// The payload tables carry no foreign key into events: they are hypertables
			// in their own right (converted below), and an FK referencing a hypertable
			// blocks drop_chunks on the parent (ADR-026 amd). They relate to the base
			// event by the natural key (device_id, event_type, occurred_time).

			// Location event fields.
			type LocationEvent struct {
				rdb.TenantScoped
				DeviceToken  string            `gorm:"type:varchar(128);not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				// Elevation is metres (decimal(12,4)), not degrees — see events.go.
				Latitude  sql.NullFloat64 `gorm:"type:decimal(10,8);"`
				Longitude sql.NullFloat64 `gorm:"type:decimal(11,8);"`
				Elevation sql.NullFloat64 `gorm:"type:decimal(12,4);"`
			}

			// Measurement event fields.
			type MeasurementEvent struct {
				rdb.TenantScoped
				DeviceToken  string            `gorm:"type:varchar(128);not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				Name         string            `gorm:"not null"`
				Value        sql.NullFloat64   `gorm:"type:decimal(20,8);"`
				Classifier   *uint
			}

			// Alert event fields.
			type AlertEvent struct {
				rdb.TenantScoped
				DeviceToken  string            `gorm:"type:varchar(128);not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				Type         string            `gorm:"not null"`
				Level        uint32            `gorm:"not null"`
				Message      string
				Source       string
			}

			// Base event fields. The originating device is keyed by its token
			// (ADR-044). The relationship target is denormalized as a single uniform
			// (anchor_type, anchor_id) pair here (ADR-013), superseded by the
			// event_anchors set table (these two columns are dropped there).
			type Event struct {
				rdb.TenantScoped
				DeviceToken   string            `gorm:"primaryKey;type:varchar(128)"`
				EventType     esmodel.EventType `gorm:"primaryKey"`
				OccurredTime  time.Time         `gorm:"primaryKey"`
				Source        string
				AltId         sql.NullString
				AnchorType    *string
				AnchorId      *uint
				ProcessedTime time.Time
			}

			err := tx.AutoMigrate(&Event{}, &LocationEvent{}, &MeasurementEvent{}, &AlertEvent{})
			if err != nil {
				return err
			}

			// Convert the base events table to a hypertable.
			err = tx.Raw("SELECT create_hypertable('event-management.events', 'occurred_time');").Row().Err()
			if err != nil {
				return err
			}

			// The three payload tables are hypertables in their own right, partitioned
			// on occurred_time alongside events, so their chunks age out with the parent
			// (ADR-026 amd). They carry no FK into events (an FK into a hypertable blocks
			// drop_chunks on the parent); the payload↔event relationship is the natural
			// key (device_id, event_type, occurred_time) joined at the app layer.
			for _, table := range []string{"location_events", "measurement_events", "alert_events"} {
				err = tx.Raw("SELECT create_hypertable('event-management." + table +
					"', 'occurred_time', if_not_exists => TRUE);").Row().Err()
				if err != nil {
					return err
				}
			}

			// The device is keyed by its token (ADR-044). The token is opaque and only
			// ever exact-matched, so force C collation on every device_token column:
			// bytewise memcmp comparisons (no locale overhead) and smaller, faster
			// B-trees than the default collation on these high-volume hypertables. The
			// index below inherits the column's collation.
			for _, t := range []string{"events", "location_events", "measurement_events", "alert_events"} {
				tx.Exec("ALTER TABLE \"event-management\".\"" + t +
					"\" ALTER COLUMN device_token TYPE varchar(128) COLLATE \"C\";")
				if tx.Error != nil {
					return tx.Error
				}
			}

			// Add index on device token.
			tx.Exec("CREATE INDEX ON \"event-management\".\"events\" (device_token, occurred_time DESC);")
			if tx.Error != nil {
				return tx.Error
			}

			// Add composite index for tenant-scoped time-series queries.
			tx.Exec("CREATE INDEX ON \"event-management\".\"events\" (tenant_id, occurred_time DESC);")
			if tx.Error != nil {
				return tx.Error
			}

			// Add the relationship-anchor index for anchor-filtered time-series
			// queries (the uniform (anchor_type, anchor_id) replaces the eight
			// previously-unindexed Rel* columns, ADR-013).
			tx.Exec("CREATE INDEX ON \"event-management\".\"events\" (anchor_type, anchor_id, occurred_time DESC);")
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
