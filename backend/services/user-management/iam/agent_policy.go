// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AgentPolicy is the HEAD of a tenant's agent policy: its working draft, the version in force,
// and the tenant kill switch and operator suspension. The published documents themselves are
// AgentPolicyVersion rows. One head per tenant, created on first write.
//
// 🔴 NO HEAD, VERSION 0, OR A SUSPENDED HEAD ALL MEAN AGENT ACTUATION IS OFF in this tenant.
type AgentPolicy struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId           string  `gorm:"not null;size:128;uniqueIndex:idx_iam_agent_policies_tenant"`
	Draft              *string `gorm:"type:jsonb"`
	DraftUpdatedAt     *time.Time
	DraftUpdatedBy     *string `gorm:"size:256"`
	PublishedVersion   int     `gorm:"not null;default:0"`
	TenantActuationOff bool    `gorm:"not null;default:false"`
	ActuationOffAt     *time.Time
	ActuationOffBy     *string `gorm:"size:256"`
	OperatorSuspended  bool    `gorm:"not null;default:false"`
	SuspendedAt        *time.Time
	SuspendedBy        *string `gorm:"size:256"`
}

func (AgentPolicy) TableName() string { return "iam_agent_policies" }

// AuditLabel names the head by its tenant.
func (p AgentPolicy) AuditLabel() string { return p.TenantId }

// Origins of a published version: how it came to be published.
const (
	// AgentPolicyOriginPublish publishes the draft.
	AgentPolicyOriginPublish = "publish"
	// AgentPolicyOriginRollback republishes an earlier version's document as a new version.
	AgentPolicyOriginRollback = "rollback"
	// AgentPolicyOriginActuation is a re-enable of a disabled published document.
	AgentPolicyOriginActuation = "actuation"
)

// AgentPolicyVersion is one published version. Append-only: a rollback publishes a NEW version, so
// the number only grows and "a decision made under version N" identifies its document.
type AgentPolicyVersion struct {
	ID          uint `gorm:"primarykey"`
	CreatedAt   time.Time
	TenantId    string `gorm:"not null;size:128;uniqueIndex:idx_iam_agent_policy_versions_tenant_version,priority:1"`
	Version     int    `gorm:"not null;uniqueIndex:idx_iam_agent_policy_versions_tenant_version,priority:2"`
	Document    string `gorm:"type:jsonb;not null"`
	Origin      string `gorm:"not null;size:32"`
	PublishedBy string `gorm:"not null;size:256"`
}

func (AgentPolicyVersion) TableName() string { return "iam_agent_policy_versions" }

// AuditLabel names the version.
func (v AgentPolicyVersion) AuditLabel() string { return fmt.Sprintf("%s v%d", v.TenantId, v.Version) }

// AgentPolicyHead returns a tenant's head, or (nil, nil) when it has none.
func (s *Store) AgentPolicyHead(ctx context.Context, tenant string) (*AgentPolicy, error) {
	var p AgentPolicy
	err := s.sys(ctx).Where("tenant_id = ?", tenant).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PublishedAgentPolicy returns the head and the version in force, either nil when absent. A head
// whose published version row is missing is an inconsistency and is returned as an error, never as
// "no policy" with the head's suspension silently lost.
func (s *Store) PublishedAgentPolicy(ctx context.Context, tenant string) (*AgentPolicy, *AgentPolicyVersion, error) {
	head, err := s.AgentPolicyHead(ctx, tenant)
	if err != nil || head == nil || head.PublishedVersion == 0 {
		return head, nil, err
	}
	v, err := s.AgentPolicyVersionAt(ctx, tenant, head.PublishedVersion)
	if err != nil {
		return head, nil, fmt.Errorf("agent policy v%d of %s: %w", head.PublishedVersion, tenant, err)
	}
	return head, v, nil
}

// AgentPolicyVersionAt returns one version, or gorm.ErrRecordNotFound.
func (s *Store) AgentPolicyVersionAt(ctx context.Context, tenant string, version int) (*AgentPolicyVersion, error) {
	var v AgentPolicyVersion
	if err := s.sys(ctx).Where("tenant_id = ? AND version = ?", tenant, version).First(&v).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// AgentPolicyVersions lists a tenant's published versions, newest first.
func (s *Store) AgentPolicyVersions(ctx context.Context, tenant string, limit int) ([]AgentPolicyVersion, error) {
	var out []AgentPolicyVersion
	err := s.sys(ctx).Where("tenant_id = ?", tenant).Order("version DESC").Limit(limit).Find(&out).Error
	return out, err
}

// lockAgentPolicyHead returns the tenant's head locked FOR UPDATE inside tx, creating it first if
// absent, so every write to one tenant's policy serializes on one row.
func lockAgentPolicyHead(tx *gorm.DB, tenant string) (*AgentPolicy, error) {
	var existing AgentPolicy
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ?", tenant).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// First write for this tenant. ON CONFLICT DO NOTHING covers a concurrent first write on
	// another replica; either way the row is then read back under the lock.
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}}, DoNothing: true}).
		Create(&AgentPolicy{TenantId: tenant}).Error; err != nil {
		return nil, err
	}
	var p AgentPolicy
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ?", tenant).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// SaveAgentPolicyDraft stores a tenant's working draft. doc must already be validated.
func (s *Store) SaveAgentPolicyDraft(ctx context.Context, tenant, doc, by string) (*AgentPolicy, error) {
	var out *AgentPolicy
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := lockAgentPolicyHead(tx, tenant)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		head.Draft, head.DraftUpdatedAt, head.DraftUpdatedBy = &doc, &now, &by
		if err := tx.Save(head).Error; err != nil {
			return err
		}
		out = head
		return nil
	})
	return out, err
}

// AgentPolicyPublish is what PublishAgentPolicy needs from its caller, decided UNDER the head lock
// so two publishes cannot both classify against the same predecessor.
//
// Next is given the head and the version in force (nil when none), and returns the document to
// publish (already validated and canonical) and the origin, or an error that aborts the publish.
// clearDraft says whether the draft is consumed. An empty doc saves only the head,
// allowing a flag change validated under the same lock without publishing a version;
// the callback may update head flags, and errors roll back every change.
type AgentPolicyPublish func(head *AgentPolicy, current *AgentPolicyVersion) (doc, origin string, clearDraft bool, err error)

// PublishAgentPolicy appends a new version and makes it the one in force, in one transaction
// under the head lock.
func (s *Store) PublishAgentPolicy(ctx context.Context, tenant, by string, next AgentPolicyPublish) (*AgentPolicyVersion, error) {
	var out *AgentPolicyVersion
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := lockAgentPolicyHead(tx, tenant)
		if err != nil {
			return err
		}
		var current *AgentPolicyVersion
		if head.PublishedVersion > 0 {
			var v AgentPolicyVersion
			if err := tx.Where("tenant_id = ? AND version = ?", tenant, head.PublishedVersion).First(&v).Error; err != nil {
				return fmt.Errorf("agent policy v%d of %s: %w", head.PublishedVersion, tenant, err)
			}
			current = &v
		}
		doc, origin, clearDraft, err := next(head, current)
		if err != nil {
			return err
		}
		// An empty document requests a head-only update under the same lock.
		if doc != "" {
			// Always advance past the highest published version, including after rollback.
			var max int
			if err := tx.Model(&AgentPolicyVersion{}).Where("tenant_id = ?", tenant).Select("COALESCE(MAX(version), 0)").Scan(&max).Error; err != nil {
				return err
			}
			v := &AgentPolicyVersion{TenantId: tenant, Version: max + 1, Document: doc, Origin: origin, PublishedBy: by}
			if err := tx.Create(v).Error; err != nil {
				return err
			}
			head.PublishedVersion = v.Version
			out = v
		}
		if clearDraft {
			head.Draft, head.DraftUpdatedAt, head.DraftUpdatedBy = nil, nil, nil
		}
		if err := tx.Save(head).Error; err != nil {
			return err
		}
		return nil
	})
	return out, err
}

// SetAgentPolicySuspended records the operator's per-tenant override.
func (s *Store) SetAgentPolicySuspended(ctx context.Context, tenant string, suspended bool, by string) (*AgentPolicy, error) {
	var out *AgentPolicy
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := lockAgentPolicyHead(tx, tenant)
		if err != nil {
			return err
		}
		head.OperatorSuspended = suspended
		if suspended {
			now := time.Now().UTC()
			head.SuspendedAt, head.SuspendedBy = &now, &by
		} else {
			head.SuspendedAt, head.SuspendedBy = nil, nil
		}
		if err := tx.Save(head).Error; err != nil {
			return err
		}
		out = head
		return nil
	})
	return out, err
}

// SetAgentPolicyActuationOff records the tenant kill switch.
func (s *Store) SetAgentPolicyActuationOff(ctx context.Context, tenant string, off bool, by string) (*AgentPolicy, error) {
	var out *AgentPolicy
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := lockAgentPolicyHead(tx, tenant)
		if err != nil {
			return err
		}
		head.TenantActuationOff = off
		if off {
			now := time.Now().UTC()
			head.ActuationOffAt, head.ActuationOffBy = &now, &by
		} else {
			head.ActuationOffAt, head.ActuationOffBy = nil, nil
		}
		if err := tx.Save(head).Error; err != nil {
			return err
		}
		out = head
		return nil
	})
	return out, err
}

// compile-time: both models label themselves in the journal.
var (
	_ rdb.AuditLabeler = AgentPolicy{}
	_ rdb.AuditLabeler = AgentPolicyVersion{}
)

// ApproverCandidates answers, for a set of identity ids, which are ENABLED identities with an
// ACTIVE second factor: the people who could step up to approve. Two queries for the whole set,
// whatever its size.
func (s *Store) ApproverCandidates(ctx context.Context, identityIDs []uint) (map[uint]bool, error) {
	out := map[uint]bool{}
	if len(identityIDs) == 0 {
		return out, nil
	}
	var enabled []uint
	if err := s.sys(ctx).Model(&Identity{}).Where("id IN ? AND enabled = ?", identityIDs, true).
		Pluck("id", &enabled).Error; err != nil {
		return nil, err
	}
	if len(enabled) == 0 {
		return out, nil
	}
	var withFactor []uint
	if err := s.sys(ctx).Model(&MfaFactor{}).Where("identity_id IN ? AND kind = ? AND state = ?", enabled, MfaKindTotp, MfaStateActive).
		Pluck("identity_id", &withFactor).Error; err != nil {
		return nil, err
	}
	for _, id := range withFactor {
		out[id] = true
	}
	return out, nil
}
