/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package model

import (
	"database/sql"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
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
				DeviceId     uint              `gorm:"not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				Event        Event             `gorm:"foreignKey:DeviceId,EventType,OccurredTime;References:DeviceId,EventType,OccurredTime"`
				Latitude     sql.NullFloat64   `gorm:"type:decimal(10,8);"`
				Longitude    sql.NullFloat64   `gorm:"type:decimal(11,8);"`
				Elevation    sql.NullFloat64   `gorm:"type:decimal(10,8);"`
			}

			// Base event fields.
			type Event struct {
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

			err := tx.AutoMigrate(&Event{}, &LocationEvent{})
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

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}
}
