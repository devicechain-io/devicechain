// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"sort"

	"github.com/devicechain-io/dc-microservice/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Effect generations are write history, not authority. GEN-1: old pods can leave
// them unchanged during a roll. Future readers must pass activation in the same
// transaction and compare a generation the operation established under enforcement;
// a pre-activation head is never proof. No request is refused on these counters.
//
// NULL, and a static membership with no head row, both read as generation 0: nothing
// generation-aware has written that state since the column existed (a device or edge a
// v1.9 pod wrote, and nothing since). No writer ever stores 0. A create stores 1 and a
// bump stores COALESCE(head, 0) + 1, so the first aware write after an unaware one always
// moves the head. Whether a 0 read is evidence is the GEN-1 question above, decided by
// WHEN it was read and never by the value: a 0 read after activation that is still 0 at
// the check is unchanged, because every writer able to change it by then bumps.
func newStateGeneration() sql.NullInt64 { return sql.NullInt64{Int64: 1, Valid: true} }

// StaticMembershipGeneration survives hard removal of the relationship row.
// The key is the logical edge, independent of the caller's relationship token.
// No foreign keys: old-pod deletes must succeed. Tenant purge discovers tenant_id
// and classifies this table as direct tenant data, including retained heads.
type StaticMembershipGeneration struct {
	ID         uint   `gorm:"primaryKey"`
	TenantId   string `gorm:"not null;size:128;uniqueIndex:uix_static_membership_generation,priority:1"`
	GroupId    uint   `gorm:"type:bigint;not null;uniqueIndex:uix_static_membership_generation,priority:2"`
	MemberType string `gorm:"not null;size:32;uniqueIndex:uix_static_membership_generation,priority:3"`
	MemberId   uint   `gorm:"type:bigint;not null;uniqueIndex:uix_static_membership_generation,priority:4"`
	Generation int64  `gorm:"not null"`
}

// staticMembershipKey is the logical edge a head is kept for.
type staticMembershipKey struct {
	groupId    uint
	memberType string
	memberId   uint
}

// staticMembershipClassifier decides whether an edge is a static-group membership. It
// memoizes both lookups: a group's deletion removes every member edge in one call, and
// each edge would otherwise ask the same two questions of the same two rows.
//
// Both reads are Unscoped and tolerate a missing row. A group's mode never changes after
// creation, so a soft-deleted group still answers it; a row that is gone altogether leaves
// a dangling edge no live group can be targeted through, and refusing to remove it would
// make the edge, and the device or group that cascades to it, undeletable.
type staticMembershipClassifier struct {
	tx     *gorm.DB
	member map[uint]bool
	static map[uint]bool
}

func newStaticMembershipClassifier(tx *gorm.DB) *staticMembershipClassifier {
	return &staticMembershipClassifier{tx: tx, member: map[uint]bool{}, static: map[uint]bool{}}
}

func (c *staticMembershipClassifier) keyOf(edge *EntityRelationship) (staticMembershipKey, bool, error) {
	key := staticMembershipKey{groupId: edge.SourceId, memberType: edge.TargetType, memberId: edge.TargetId}
	if edge.SourceType != string(entity.TypeGroup) {
		return key, false, nil
	}
	isMember, known := c.member[edge.RelationshipTypeId]
	if !known {
		var types []EntityRelationshipType
		if err := c.tx.Unscoped().Select("id", "token").Where("id = ?", edge.RelationshipTypeId).
			Limit(1).Find(&types).Error; err != nil {
			return key, false, err
		}
		isMember = len(types) == 1 && types[0].Token == MembershipRelationshipType
		c.member[edge.RelationshipTypeId] = isMember
	}
	if !isMember {
		return key, false, nil
	}
	isStatic, known := c.static[edge.SourceId]
	if !known {
		var groups []EntityGroup
		if err := c.tx.Unscoped().Select("id", "membership_mode").Where("id = ?", edge.SourceId).
			Limit(1).Find(&groups).Error; err != nil {
			return key, false, err
		}
		isStatic = len(groups) == 1 && groups[0].MembershipMode == string(MembershipStatic)
		c.static[edge.SourceId] = isStatic
	}
	return key, isStatic, nil
}

// staticMembershipHeadsPerStatement bounds one upsert's rows (five binds each) well
// below PostgreSQL's bind-parameter limit.
const staticMembershipHeadsPerStatement = 500

// advanceStaticMembershipHeadsTx advances each logical edge's head by the number of times
// it occurs in edges (an edge row's add or removal is one write of its logical edge), in
// one multi-row upsert per chunk rather than one statement per edge. Keys are written in
// a fixed order, so two transactions that touch overlapping heads take their row locks in
// the same order.
func advanceStaticMembershipHeadsTx(tx *gorm.DB, c *staticMembershipClassifier, edges []EntityRelationship) error {
	counts := map[staticMembershipKey]int64{}
	keys := make([]staticMembershipKey, 0)
	for i := range edges {
		key, ok, err := c.keyOf(&edges[i])
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if counts[key] == 0 {
			keys = append(keys, key)
		}
		counts[key]++
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.groupId != b.groupId {
			return a.groupId < b.groupId
		}
		if a.memberType != b.memberType {
			return a.memberType < b.memberType
		}
		return a.memberId < b.memberId
	})
	for start := 0; start < len(keys); start += staticMembershipHeadsPerStatement {
		chunk := keys[start:min(start+staticMembershipHeadsPerStatement, len(keys))]
		heads := make([]StaticMembershipGeneration, 0, len(chunk))
		for _, key := range chunk {
			heads = append(heads, StaticMembershipGeneration{GroupId: key.groupId, MemberType: key.memberType,
				MemberId: key.memberId, Generation: counts[key]})
		}
		if err := tx.Clauses(staticMembershipHeadUpsert(true)).Create(&heads).Error; err != nil {
			return err
		}
	}
	return nil
}

// staticMembershipHeadUpsert inserts a head at the row's own Generation, or advances an
// existing head by it. byExcluded=false adds one instead (the single-edge write).
func staticMembershipHeadUpsert(byExcluded bool) clause.OnConflict {
	by := any(1)
	if byExcluded {
		by = clause.Column{Table: "excluded", Name: "generation"}
	}
	return clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "group_id"}, {Name: "member_type"}, {Name: "member_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"generation": gorm.Expr("? + ?", clause.Column{Table: clause.CurrentTable, Name: "generation"}, by),
		}),
	}
}

// bumpStaticMembershipTx advances one edge's head and returns the generation it now
// holds, for the add to stamp onto its row. An edge that is not a static membership
// returns NULL: nothing guards it.
func bumpStaticMembershipTx(tx *gorm.DB, edge *EntityRelationship) (sql.NullInt64, error) {
	key, ok, err := newStaticMembershipClassifier(tx).keyOf(edge)
	if err != nil || !ok {
		return sql.NullInt64{}, err
	}
	head := StaticMembershipGeneration{GroupId: key.groupId, MemberType: key.memberType, MemberId: key.memberId, Generation: 1}
	if err := tx.Clauses(staticMembershipHeadUpsert(false), clause.Returning{}).Create(&head).Error; err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: head.Generation, Valid: true}, nil
}

func createRelationshipWithGenerationTx(tx *gorm.DB, edge *EntityRelationship) error {
	generation, err := bumpStaticMembershipTx(tx, edge)
	if err != nil {
		return err
	}
	edge.MembershipGeneration = generation
	return tx.Create(edge).Error
}

// The selected rows are locked until their heads and hard deletions commit.
// Delete only those rows: a concurrent insert must retain its own add evidence.
func removeRelationshipsWithGenerationTx(tx *gorm.DB, predicate string, args ...any) (int64, error) {
	var edges []EntityRelationship
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(predicate, args...).Order("id ASC").Find(&edges).Error; err != nil {
		return 0, err
	}
	if err := advanceStaticMembershipHeadsTx(tx, newStaticMembershipClassifier(tx), edges); err != nil {
		return 0, err
	}
	ids := make([]uint, 0, len(edges))
	for i := range edges {
		ids = append(ids, edges[i].ID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// Cascades are not bounded by the explicit bulk-remove API's lookup ceiling.
	// Keep each statement below PostgreSQL's bind-parameter limit.
	var removed int64
	for start := 0; start < len(ids); start += 1000 {
		result := tx.Unscoped().Where("id IN ?", ids[start:min(start+1000, len(ids))]).Delete(&EntityRelationship{})
		if result.Error != nil {
			return 0, result.Error
		}
		removed += result.RowsAffected
	}
	return removed, nil
}
