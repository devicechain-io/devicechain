// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package model holds the event-processing service's persistent state: the DETECT
// engine's Postgres snapshot store (ADR-051). This is deliberately NOT a
// tenant-scoped entity store — the DETECT engine is a singleton per Instance that
// holds every tenant's rules and window/timer state in one process (tenant is
// carried on the rule id), so its checkpoint is one row of opaque engine state per
// partition, not per tenant.
package model

import (
	"time"
)

// DetectSnapshot is one partition's durable checkpoint of the DETECT engine
// (ADR-051). It couples three things that MUST be made durable together for
// correct replay-after-restart: the serialized engine state (Payload — windows,
// timers, watermark), and the highest JetStream stream sequence whose effect is
// captured in that state (StreamSeq). Because they are a single row, one upsert
// commits them atomically — the ack-on-checkpoint contract: a message is acked
// only after the snapshot containing its effect has committed, so a crash
// redelivers from at-or-before StreamSeq and the engine's idempotent
// Seq <= lastSeq guard drops the already-applied prefix. Watermark is denormalized
// out of the blob purely so the operations surface (Slice 8) can read watermark
// lag without deserializing the payload.
//
// It is not tenant-scoped (no TenantId), so the tenant-scope callbacks pass it
// through, and it carries no Token, so the token-grammar callbacks ignore it.
type DetectSnapshot struct {
	// PartitionId identifies the single-writer partition this checkpoint belongs to.
	// GA ships one active DETECT per Instance (a single partition); the column exists
	// so a future tenant-sharded fleet (post-GA) checkpoints per shard without a schema
	// change.
	PartitionId string `gorm:"primaryKey;size:128;not null"`
	// StreamSeq is the highest applied JetStream stream sequence captured in Payload.
	// Stored as a signed bigint (JetStream sequences start at 1 and never approach the
	// int64 ceiling); the engine's uint64 is range-safe across the conversion.
	StreamSeq int64 `gorm:"not null"`
	// Watermark is the engine's logical time at checkpoint, denormalized from Payload
	// for the operations surface (watermark-lag metric) — the payload remains the
	// source of truth restored into the engine.
	Watermark time.Time `gorm:"not null"`
	// Payload is the engine's serialized state (core.Engine.Snapshot()), opaque here.
	Payload []byte `gorm:"not null"`
	// UpdatedAt is gorm-managed; it records when this partition last checkpointed.
	UpdatedAt time.Time
}

// AuditExempt opts the snapshot store out of the audit journal (ADR-019): it is
// high-frequency derived engine state re-committed on every checkpoint, not a
// control-plane entity mutation — the same rationale as the device-state and
// latest-measurement projections.
func (DetectSnapshot) AuditExempt() bool { return true }
