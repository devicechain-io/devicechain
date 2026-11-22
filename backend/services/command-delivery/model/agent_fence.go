// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

// AgentActuationFence is local safety bookkeeping; the policy author audits the change.
type AgentActuationFence struct {
	TenantId           string    `gorm:"primaryKey;not null;size:128"`
	Enabled            bool      `gorm:"not null"`
	KilledAtGeneration int       `gorm:"not null"`
	Epoch              int64     `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

func (AgentActuationFence) AuditExempt() bool { return true }
