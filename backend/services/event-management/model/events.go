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
)

// Event with token references resolved and info from assignment merged.
type Event struct {
	DeviceId           uint
	EventType          esmodel.EventType
	OccurredTime       time.Time
	Source             string
	AltId              sql.NullString
	RelDeviceId        *uint
	RelDeviceGroupId   *uint
	RelCustomerId      *uint
	RelCustomerGroupId *uint
	RelAreaId          *uint
	RelAreaGroupId     *uint
	RelAssetId         *uint
	RelAssetGroupId    *uint
	ProcessedTime      time.Time
}

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

// Information required to create a location event.
type LocationEventCreateRequest struct {
	Event
	Latitude  *float64
	Longitude *float64
	Elevation *float64
}

// Measurement event fields.
type MeasurementEvent struct {
	DeviceId     uint            `gorm:"not null"`
	OccurredTime time.Time       `gorm:"not null"`
	Event        Event           `gorm:"foreignKey:DeviceId,OccurredTime;References:DeviceId,OccurredTime"`
	Latitude     sql.NullFloat64 `gorm:"type:decimal(10,8);"`
	Longitude    sql.NullFloat64 `gorm:"type:decimal(11,8);"`
	Elevation    sql.NullFloat64 `gorm:"type:decimal(10,8);"`
}

// Information required to create a measurement event.
type MeasurementEventCreateRequest struct {
	Event
	Latitude  *float64
	Longitude *float64
	Elevation *float64
}
