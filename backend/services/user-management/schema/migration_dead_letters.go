// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// deadLetterSnapshot is the queryable record of work a consumer accepted and then gave up
// on (ADR-024), as this migration creates it.
//
// 🔴 IT IS THIS MIGRATION'S OWN SNAPSHOT, not the live model, and it must never be
// replaced by one. When the live model gains a field, the column arrives in a NEW
// migration appended after this one and this struct stays exactly as it is.
//
// 🔑 THE TENANT COLUMN IS NAMED `tenant_id`, AND THAT IS LOAD-BEARING RATHER THAN
// COSMETIC. The ADR-077 purge classifies a table from the catalog, and it recognises a
// tenant by that column name — so naming it anything else would leave a table full of a
// deleted tenant's payloads that no purge could reach, with the coverage gate green
// because the classifier would simply have no idea the table held anyone's data.
//
// The three indexes are the three questions an operator actually asks: what has this
// tenant lost, what has this KIND been losing, and what happened around the time of an
// incident. Each of the first two is a COMPOSITE ending in occurred_time DESC, because
// every list is newest-first — an index on the filter column alone gets used for the
// filter and then leaves the sort to a heap. The schema gate is what caught that: the
// dumped index read `(kind)` under a name that said `kind_time`.
type deadLetterSnapshot struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"not null;size:128;index:idx_dead_letters_tenant_time,priority:1"`

	OccurredTime time.Time `gorm:"not null;index:idx_dead_letters_tenant_time,priority:2,sort:desc;index:idx_dead_letters_time,sort:desc;index:idx_dead_letters_kind_time,priority:2,sort:desc"`

	Kind   string `gorm:"not null;size:64;index:idx_dead_letters_kind_time,priority:1"`
	Reason string `gorm:"not null;size:32"`
	Source string `gorm:"not null;size:64"`

	Summary string `gorm:"not null"`
	Detail  string

	Attempts    int
	Subject     string `gorm:"size:512"`
	Sequence    uint64
	Correlation string `gorm:"size:128"`
	Reference   string `gorm:"size:256"`

	// StreamSeq is the dead-letter stream's own sequence, which is what makes the
	// consumer idempotent: a redelivery re-stores the same row rather than a second one.
	StreamSeq uint64 `gorm:"not null;uniqueIndex:idx_dead_letters_stream_seq"`

	Payload []byte
}

func (deadLetterSnapshot) TableName() string { return "dead_letters" }

// NewDeadLettersMigration creates the dead-letter store.
//
// Re-runnable by construction: AutoMigrate creates what is missing and drops nothing,
// which matters because migrations run with UseTransaction:false and replay from the top
// after a failure.
func NewDeadLettersMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260904120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&deadLetterSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&deadLetterSnapshot{})
		},
	}
}
