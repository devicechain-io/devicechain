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

// DetectRule is the durable projection of one published detection rule (ADR-051 slice
// 4b-3). It is what makes the rule set survive a restart: the published-rule fact stream
// is a LIVE delta channel with finite retention (7-day DiscardOld, ADR-023), so it cannot
// be the source of truth on its own — a rule published and then left untouched past
// retention would vanish from a stream replay. Instead the fact consumer upserts each
// rule into this table BEFORE acking the fact (at-least-once persistence), and the engine's
// rule set is rebuilt from this table at startup. The fact stream then only has to deliver
// each fact once to a live consumer; durability lives here.
//
// Like DetectSnapshot it is NOT tenant-scoped (the engine is a per-Instance singleton and
// the tenant is carried on the rule id / stored as a plain column), and it is audit-exempt
// (a derived projection, not a control-plane mutation). Retain-superseded means rows are
// only upserted, never deleted, in this slice; bounding row growth is the deferred
// governance concern (ADR-023/052), the same as the fact-stream version-count growth.
type DetectRule struct {
	// RuleId is the composed runtime id "{tenant}/{profileVersionToken}/{ruleToken}" —
	// globally unique and the exact key the registry and engine use, so it is the natural
	// primary key: a re-published/redelivered fact upserts the same row in place.
	RuleId string `gorm:"primaryKey;size:512;not null"`
	// Tenant, ProfileVersionToken, and RuleToken are the components the rebuild recomposes
	// the rule from (via runtime.CompilePublishedRules), stored denormalized so LoadAll needs
	// no id parsing. They are functionally determined by RuleId, so a conflict updates only
	// the definition.
	Tenant              string `gorm:"size:256;not null;index"`
	ProfileVersionToken string `gorm:"size:256;not null"`
	RuleToken           string `gorm:"size:256;not null"`
	// Definition is the opaque rules.Rule JSON, compiled by event-processing's DETECT
	// compiler on load — the same document device-management stored and the publish gate
	// validated.
	Definition string `gorm:"not null"`
	// UpdatedAt is gorm-managed; it records when this rule was last (re)published.
	UpdatedAt time.Time
}

// AuditExempt opts the rule projection out of the audit journal (ADR-019): it is a derived
// mirror of device-management's authored rules, re-materialized from published-rule facts,
// not a control-plane entity mutation of its own.
func (DetectRule) AuditExempt() bool { return true }
