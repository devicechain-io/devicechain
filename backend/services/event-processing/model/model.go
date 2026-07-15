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
	// EntityGroupToken / EntityGroupVersion are the rule's OPTIONAL group scope (ADR-062 S4):
	// the published dynamic entity-group version whose members the rule fires for. They MUST be
	// persisted here (not only threaded from the live fact) because this projection — not the
	// finite-retention fact stream — is the restart source of truth: a rule rebuilt from a row
	// missing its scope would silently reload profile-wide, firing for every device and never
	// descoping, which is also a restart-determinism break. Empty token / version 0 ⇒ unscoped.
	EntityGroupToken   string `gorm:"size:256;not null;default:''"`
	EntityGroupVersion int32  `gorm:"not null;default:0"`
	// UpdatedAt is gorm-managed; it records when this rule was last (re)published.
	UpdatedAt time.Time
}

// AuditExempt opts the rule projection out of the audit journal (ADR-019): it is a derived
// mirror of device-management's authored rules, re-materialized from published-rule facts,
// not a control-plane entity mutation of its own.
func (DetectRule) AuditExempt() bool { return true }

// DeviceRoster is the durable projection of device-management's device-roster facts (ADR-051
// slice 4c-2): the set of devices EXPECTED to report. It is what lets DETECT arm a dead-man
// absence timer for a device that has NEVER reported — the reported-once-then-silent case is
// already covered by the heartbeat-armed timer, but a never-seen device produces no event to
// arm one, so without this roster its absence rule is inert. The roster consumer upserts each
// fact here BEFORE acking it (at-least-once persistence), and the engine's dead-man arming is
// rebuilt from this table at startup — so a never-reported device survives a restart
// independent of the finite-retention fact stream, exactly like the rule projection.
//
// Unlike DetectRule this projection IS live-mutable: a device delete TOMBSTONES its row (Deleted
// = true) so the arming for a decommissioned device is cancelled rather than firing a false
// absence forever. ProfileToken is the STABLE profile token (not the "{profileToken}@{version}"
// token) so a re-publish never staleness-orphans the entry; the arming path cross-references the
// active version at arming time (ProfileActive). It is empty when the device's type has no
// profile (a roster entry with no resolvable rules, retained so a later re-type re-homes it).
//
// ORDERING / TOMBSTONE (ADR-051 slice 4c-2b-1, hardened after review). Roster upserts arrive on
// one stream and deletes on another (entity-deleted), through two independent durable consumers
// with no relative ordering, and either can be redelivered (a fact stays pending until its
// persist-before-ack commits, and a failed ack is tolerated). Two hazards follow: a stale
// redelivered upsert could RESURRECT a deleted device, and a stale redelivered delete could ERASE
// a legitimately re-created (token-reused) device — both permanent, since neither self-heals. The
// row is therefore never hard-deleted: a delete flips Deleted and every mutation is gated on a
// single monotonic lifecycle clock, LastEventAt. A device's real lifecycle (create < re-type <
// delete < re-create) increases in wall time, so applying a fact only when it is strictly newer
// than LastEventAt makes the row converge to the latest lifecycle event regardless of delivery
// order. LoadAll returns only live (Deleted=false) rows. Tombstones are retained (bounding their
// growth is the deferred governance concern, ADR-023/052, like the rule projection's).
//
// Like the other event-processing projections it is NOT tenant-scoped (the engine is a
// per-Instance singleton; tenant is a plain column, keyed into the composite primary key so a
// device token that repeats across tenants stays distinct) and it is audit-exempt (a derived
// mirror, not a control-plane mutation).
type DeviceRoster struct {
	// Tenant + DeviceToken are the composite primary key: a device token is unique only per
	// tenant (ADR-042), so both are needed to identify a row, and a re-type fact for the same
	// device upserts in place.
	Tenant      string `gorm:"primaryKey;size:256;not null"`
	DeviceToken string `gorm:"primaryKey;size:256;not null"`
	// ProfileToken is the stable profile token the device's type adopts (ADR-045), or empty
	// when the type has no profile. Indexed so the arming path can fan out to every device on a
	// profile when that profile's rules change (ADR-051 slice 4c-2b's publish trigger). Frozen at
	// its last live value on a tombstone (irrelevant there — tombstones are excluded from LoadAll).
	ProfileToken string `gorm:"size:256;not null;index"`
	// ExpectedSince is the base of the dead-man clock for a never-reported device — the device's
	// creation time (or the moment it began its current type membership on a re-type).
	ExpectedSince time.Time `gorm:"not null"`
	// Deleted tombstones a removed device. The row is retained rather than dropped so a stale
	// redelivered upsert cannot resurrect it (the tombstone's LastEventAt fences it out) and a
	// stale redelivered delete cannot erase a newer re-creation. LoadAll filters it out.
	Deleted bool `gorm:"not null"`
	// LastEventAt is the device-lifecycle time of the most recent APPLIED fact: ExpectedSince for
	// a create/re-type, the entity-deleted fact's DeletedTime for a delete. It is the single
	// monotonic axis every mutation is gated on (apply iff strictly newer), so redelivery, cross-
	// stream reordering, or consumer lag can never regress the row or flip it against real order.
	LastEventAt time.Time `gorm:"not null"`
	// UpdatedAt is gorm-managed; it records when this roster entry was last (re)emitted.
	UpdatedAt time.Time
}

// AuditExempt opts the roster projection out of the audit journal (ADR-019): it is a derived
// mirror of device-management's device lifecycle, not a control-plane mutation of its own.
func (DeviceRoster) AuditExempt() bool { return true }

// ProfileActive is the durable projection of which published version is ACTIVE for a stable
// profile token, plus when that version was published (ADR-051 slice 4c-2). It is maintained
// last-fact-wins from the SAME detection-rules-published facts that feed the rule projection:
// every publish (and every rollback re-emit) names the now-active "{profileToken}@{version}"
// token and its publish time, so the latest fact for a profile token names its active version.
// Delivery is single-consumer and in commit order (one durable reader on a per-tenant subject),
// so last-received is last-published; a redelivery cannot reorder past an acked predecessor.
//
// 4c-2b's dead-man arming cross-references it to turn a roster entry's STABLE profile token into
// the active version's absence rules (the roster deliberately does not pin a version), and uses
// PublishedAt as the grace-period base: the never-reported deadline is max(ExpectedSince,
// PublishedAt)+Timeout, so a newly published absence rule gives existing quiet devices one
// timeout of grace instead of firing a fleet-wide burst the instant it lands.
//
// Like the other event-processing projections it is NOT tenant-scoped (tenant is a plain column
// in the composite key) and audit-exempt.
type ProfileActive struct {
	// Tenant + ProfileToken are the composite primary key: a profile token is unique per tenant
	// (ADR-042), and each publish for a profile upserts its single active-version row in place.
	Tenant       string `gorm:"primaryKey;size:256;not null"`
	ProfileToken string `gorm:"primaryKey;size:256;not null"`
	// ActiveVersionToken is the "{profileToken}@{version}" token of the currently active
	// published version — the scope key the rule registry files that version's rules under.
	ActiveVersionToken string `gorm:"size:256;not null"`
	// PublishedAt is when that version was published (the grace-period base). A rollback
	// re-emits with a fresh publish time, so last-fact-wins keeps this pointing at the version
	// that is active NOW, not the highest version number ever published.
	PublishedAt time.Time `gorm:"not null"`
	// UpdatedAt is gorm-managed; it records when this profile last published.
	UpdatedAt time.Time
}

// AuditExempt opts the profile-active projection out of the audit journal (ADR-019): it is a
// derived mirror of device-management's publish lifecycle, not a control-plane mutation.
func (ProfileActive) AuditExempt() bool { return true }

// DeviceAttribute is the durable projection of a numeric, platform-set device attribute (ADR-051
// slice 4c-3): the current value of one device attribute, so a detection rule can resolve a
// DYNAMIC threshold from it ("m[metric] <op> attr[key]" reads the device's own attribute instead
// of a compile-time literal, so ONE published rule enforces a per-device limit). It is maintained
// from device-management's device-attribute facts (ADR-051 slice 4c-3a), which are emitted only
// for numeric (DOUBLE/LONG) attributes in the two platform-set scopes (SHARED/SERVER) — CLIENT
// (device-reported) is excluded so a device cannot set the limit it is checked against. The
// consumer upserts each fact here BEFORE acking (at-least-once persistence), so the value survives
// a restart independent of the finite-retention fact stream, exactly like the rule/roster
// projections. This slice lands and maintains the projection; the engine eval that reads it (the
// CEL "attr" var) is slice 4c-3b-2, so it is engine-inert here.
//
// SCOPE IS PART OF THE KEY. An attribute's natural key in device-management is (entity, scope,
// key), so a device may hold the same key in BOTH eligible scopes at once; the projection keys on
// (tenant, device, scope, key) so a write/delete in one scope never annihilates the other's value.
// The rule language stays scope-less (attr["key"]); slice 4c-3b-2's eval flattens the two scopes
// with a fixed precedence (SERVER over SHARED) when it builds the CEL input.
//
// ORDERING / TOMBSTONE (the DeviceRoster precedent, ADR-051 slice 4c-2b-1). Value upserts and
// per-attribute removals arrive on one stream (device-attribute) and DEVICE deletions on another
// (entity-deleted), through independent durable consumers with no relative ordering, and either can
// be redelivered. So every per-row mutation is gated on a single monotonic clock, LastEventAt (the
// fact's write time): a mutation applies only when STRICTLY newer, so a redelivered or lagging
// older fact is a no-op and can neither regress a value nor resurrect a removed one. A per-attribute
// REMOVAL (the attribute was deleted, or overwritten with a non-numeric value/type) tombstones the
// row (Deleted=true) rather than dropping it, so a stale older set cannot resurrect the value.
// LoadAll returns only live (Deleted=false) rows.
//
// A DEVICE deletion is handled out-of-band, because the fact carries no attribute keys: the
// entity-deleted consumer PURGES every one of the device's attribute rows AND records a
// device-level deletion fence (DeviceAttributeDeletion) so a straggler set/removal fact for the
// dead device — reordered after the deletion, for a key that had no row to tombstone — cannot
// resurrect a phantom value a reused token would then inherit. See DeviceAttributeStore.
//
// Like the other event-processing projections it is NOT tenant-scoped (the engine is a per-Instance
// singleton; tenant is a plain column keyed into the composite primary key so a device token that
// repeats across tenants stays distinct) and it is audit-exempt (a derived mirror).
type DeviceAttribute struct {
	// Tenant + DeviceToken + Scope + AttrKey are the composite primary key: a device token is
	// unique only per tenant (ADR-042), and a device holds an attribute per (scope, key), so all
	// four identify a row and a re-set of the same (device, scope, key) upserts in place.
	Tenant      string `gorm:"primaryKey;size:256;not null"`
	DeviceToken string `gorm:"primaryKey;size:256;not null"`
	Scope       string `gorm:"primaryKey;size:64;not null"`
	AttrKey     string `gorm:"primaryKey;size:256;not null"`
	// Value is the current numeric value (parsed from a DOUBLE/LONG attribute). Meaningful only on a
	// live row; frozen at its last live value on a tombstone (never read there — tombstones are
	// excluded from LoadAll).
	Value float64 `gorm:"not null"`
	// Deleted tombstones a REMOVED attribute (the row is retained rather than dropped so a stale
	// older set cannot resurrect the value). LoadAll filters it out. A device deletion does not
	// tombstone — it hard-purges the rows and fences the device (DeviceAttributeDeletion).
	Deleted bool `gorm:"not null"`
	// LastEventAt is the fact write time of the most recent APPLIED mutation — the single monotonic
	// axis every mutation is gated on (apply iff strictly newer), so redelivery or cross-stream
	// reordering can never regress the value or flip a set/removal against real order.
	LastEventAt time.Time `gorm:"not null"`
	// UpdatedAt is gorm-managed; it records when this attribute was last (re)projected.
	UpdatedAt time.Time
}

// AuditExempt opts the device-attribute projection out of the audit journal (ADR-019): it is a
// derived mirror of device-management's attributes, not a control-plane mutation of its own.
func (DeviceAttribute) AuditExempt() bool { return true }

// DeviceAttributeDeletion is the per-device resurrection fence for the attribute projection (ADR-051
// slice 4c-3). A DEVICE deletion arrives on the entity-deleted stream carrying no attribute keys, so
// the consumer cannot pre-tombstone the individual attribute rows that a not-yet-projected straggler
// set fact would later create. This row records "device D was deleted at T": the attribute store
// consults it on every set/removal and DROPS any fact whose write time is at or before DeletedAt, so
// a stale fact reordered after the deletion cannot insert a phantom row that a reused token would
// inherit. It is monotonic (DeletedAt only advances) so a redelivered deletion is idempotent and a
// re-deletion after token reuse advances the fence; a genuine post-reuse fact (write time strictly
// after DeletedAt) is not fenced and applies normally. Rows accumulate one per deleted device and
// are never pruned — the same unbounded-tombstone posture the roster projection accepts, with
// bounding deferred to the governance concern (ADR-023/052).
//
// Not tenant-scoped (tenant in the composite key) and audit-exempt, like the projection it guards.
type DeviceAttributeDeletion struct {
	// Tenant + DeviceToken are the composite primary key: the fence is per device, per tenant.
	Tenant      string `gorm:"primaryKey;size:256;not null"`
	DeviceToken string `gorm:"primaryKey;size:256;not null"`
	// DeletedAt is the device's deletion time (the entity-deleted fact's DeletedTime). It only
	// advances; a set/removal fact at or before it is fenced out.
	DeletedAt time.Time `gorm:"not null"`
	// UpdatedAt is gorm-managed.
	UpdatedAt time.Time
}

// AuditExempt opts the device-attribute deletion fence out of the audit journal (ADR-019).
func (DeviceAttributeDeletion) AuditExempt() bool { return true }
