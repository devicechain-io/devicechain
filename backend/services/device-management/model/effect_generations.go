// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Effect generations are write history, not authority. GEN-1: old pods can leave
// them unchanged during a roll. Future readers must pass activation in the same
// transaction and compare a generation the operation established under enforcement;
// a pre-activation head is never proof. No request is refused on these counters.
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

func bumpStaticMembershipTx(tx *gorm.DB, edge *EntityRelationship) (sql.NullInt64, error) {
	if edge.SourceType != "group" {
		return sql.NullInt64{}, nil
	}
	var rt EntityRelationshipType
	if err := tx.First(&rt, edge.RelationshipTypeId).Error; err != nil {
		return sql.NullInt64{}, err
	}
	if rt.Token != MembershipRelationshipType {
		return sql.NullInt64{}, nil
	}
	var group EntityGroup
	if err := tx.First(&group, edge.SourceId).Error; err != nil {
		return sql.NullInt64{}, err
	}
	if group.MembershipMode != string(MembershipStatic) {
		return sql.NullInt64{}, nil
	}
	head := StaticMembershipGeneration{
		GroupId:    edge.SourceId,
		MemberType: edge.TargetType,
		MemberId:   edge.TargetId,
		Generation: 1,
	}
	err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "group_id"}, {Name: "member_type"}, {Name: "member_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"generation": gorm.Expr("? + 1", clause.Column{Table: clause.CurrentTable, Name: "generation"}),
		}),
	}, clause.Returning{}).Create(&head).Error
	return sql.NullInt64{Int64: head.Generation, Valid: err == nil}, err
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
	ids := make([]uint, 0, len(edges))
	for i := range edges {
		if _, err := bumpStaticMembershipTx(tx, &edges[i]); err != nil {
			return 0, err
		}
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
