// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	SuppressionOneShot   = "ONE_SHOT"
	SuppressionRecurring = "RECURRING"
)

type AlarmSuppressionTarget struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// AlarmSuppressionWindow is an operator-declared paging overlay (ADR-085).
// Targets and keys are whole JSON documents; no SQL predicate inspects their contents.
type AlarmSuppressionWindow struct {
	gorm.Model
	TenantId string `gorm:"index;not null;size:128;index:idx_suppression_tenant_ended,priority:1"`
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
	Reason         string                   `gorm:"type:text;not null"`
	Targets        []AlarmSuppressionTarget `gorm:"serializer:json;type:jsonb;not null"`
	AlarmKeys      []string                 `gorm:"serializer:json;type:jsonb"`
	BypassSeverity sql.NullString           `gorm:"size:16"`
	Kind           string                   `gorm:"size:16;not null"`
	StartTime      *time.Time
	EndTime        *time.Time
	Timezone       sql.NullString `gorm:"size:64"`
	Slots          datatypes.JSON
	ValidFrom      *time.Time
	ValidUntil     *time.Time
	EndedAt        *time.Time `gorm:"index:idx_suppression_tenant_ended,priority:2"`
	EndedBy        sql.NullString
	CreatedBy      sql.NullString
	ScopeAnchors   rdb.Anchors `gorm:"type:text[]"`
	CreatorScope   rdb.Anchors `gorm:"type:text[]"`
	// Unscoped creators also store the whole-target intersection for visibility.
	// CreatorScope holds the immutable full creator scope; this bit marks its restriction.
	CreatorScoped bool  `gorm:"not null;default:false"`
	Version       int64 `gorm:"not null;default:1"`
}

// AlarmCycleSuppression is immutable cycle history; deleting a window preserves it.
type AlarmCycleSuppression struct {
	ID           uint `gorm:"primarykey"`
	CreatedAt    time.Time
	TenantId     string         `gorm:"not null;size:128;uniqueIndex:uix_alarm_cycle_suppressions_cycle,priority:1"`
	AlarmId      uint           `gorm:"not null;uniqueIndex:uix_alarm_cycle_suppressions_cycle,priority:2;index:idx_alarm_cycle_suppressions_alarm"`
	RaisedTime   time.Time      `gorm:"not null;uniqueIndex:uix_alarm_cycle_suppressions_cycle,priority:3"`
	AlarmToken   string         `gorm:"not null;size:255"`
	WindowToken  string         `gorm:"not null;size:128"`
	WindowName   sql.NullString `gorm:"size:128"`
	ScopeAnchors rdb.Anchors    `gorm:"type:text[]"`
}

func (AlarmSuppressionWindow) DefaultOrder() string {
	return "alarm_suppression_windows.created_at DESC, alarm_suppression_windows.token ASC"
}

// AlarmSuppressionUpdateRequest carries omitted, null and value separately.
// Identity, anchors, version and end stamps are never caller input.
type AlarmSuppressionUpdateRequest struct {
	Name           dcgraphql.OptionalString
	Description    dcgraphql.OptionalString
	Metadata       dcgraphql.OptionalString
	Reason         dcgraphql.OptionalString
	Targets        OptionalAlarmSuppressionTargets
	AlarmKeys      dcgraphql.OptionalStringList
	BypassSeverity dcgraphql.OptionalString
	Kind           OptionalAlarmSuppressionKind
	StartTime      dcgraphql.OptionalString
	EndTime        dcgraphql.OptionalString
	Timezone       dcgraphql.OptionalString
	Slots          dcgraphql.OptionalString
	ValidFrom      dcgraphql.OptionalString
	ValidUntil     dcgraphql.OptionalString
}
