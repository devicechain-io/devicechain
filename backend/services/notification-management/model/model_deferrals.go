// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"gorm.io/datatypes"
)

// NotificationDeferral is one notification a policy's quiet hours held: the alarm, the
// policy that held it, and when the quiet stretch ends. The escalation scheduler releases
// it at ReleaseAt — delivers it through the policy's rules — unless the alarm was
// acknowledged or cleared after Event's occurred time, in which case it is dropped.
//
// There is at most ONE row per (alarm, policy). A later RAISED or ESCALATED for the same
// alarm that the same policy also holds REPLACES the row's event, so the page that goes out
// at the end of quiet hours describes the alarm as it last was, not as it first was, and a
// flapping alarm overnight costs one page in the morning rather than one per flap.
//
// Rows are hard-deleted: claiming a row for release IS deleting it (a compare-and-swap on
// id and updated_at, see Api.ClaimNotificationDeferral), which is what keeps the release
// single-delivery when every replica runs the scheduler.
//
// Event is the alarm state-change event as received, stored opaque. This package must not
// import device-management's model (see ruleSeverities), and it does not need to look
// inside: the dispatcher decodes it to render the notification it would have sent.
type NotificationDeferral struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// TenantId is spelled out rather than embedded from rdb.TenantScoped so it can lead the
	// unique index; the tenant-scope callback matches the field by this name either way.
	TenantId string `gorm:"index;not null;size:128;uniqueIndex:uix_notification_deferrals_tenant_alarm_policy,priority:1"`

	AlarmToken string `gorm:"not null;size:128;uniqueIndex:uix_notification_deferrals_tenant_alarm_policy,priority:2"`
	PolicyId   uint   `gorm:"not null;index;uniqueIndex:uix_notification_deferrals_tenant_alarm_policy,priority:3"`
	AlarmKey   string `gorm:"not null;size:256"`
	Severity   string `gorm:"size:16"`
	// OccurredTime is the held event's occurred time. An acknowledgement or clear stamped
	// AFTER it means the alarm this deferral describes is no longer worth paging about.
	OccurredTime time.Time
	// ReleaseAt is when the quiet stretch that held the notification ends.
	ReleaseAt time.Time `gorm:"not null;index"`
	// Attempts counts releases that reached no channel and were put back for another try.
	Attempts int
	Event    datatypes.JSON `gorm:"not null"`
}
