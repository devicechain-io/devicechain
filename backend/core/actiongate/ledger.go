// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Record is the ledger row's columns, embedded by each area's agent_proposals model. The area
// adds gorm.Model and rdb.TenantScoped around it and declares its own migration snapshot: this
// type is the LIVE shape, and a migration never points at it.
//
// 🔴 EVERY FIELD AN APPROVER RELIES ON IS STORED, NEVER RE-SUPPLIED. The payload and target set
// are the canonical action as the server normalized it at proposal time, and the digest covers
// them; an approval names the digest, so what executes is what was displayed.
type Record struct {
	// Token is server-generated (NewToken).
	Token string `gorm:"size:64;not null;index"`
	// RequestId is the agent's idempotency key, unique per (tenant, client, principal). A reuse
	// with the same digest returns the original proposal; with another digest it is a CONFLICT.
	RequestId string `gorm:"size:128;not null"`
	// EffectToken is the server-generated idempotency token the effect will be created under.
	EffectToken string `gorm:"size:64;not null"`
	Kind        string `gorm:"size:32;not null"`
	State       string `gorm:"size:16;not null;index"`
	// Payload is the canonical action (Canonical).
	Payload datatypes.JSON `gorm:"not null"`
	// TargetSet is the canonical, frozen set of targets.
	TargetSet datatypes.JSON `gorm:"not null"`
	// Dependencies is what the action's MEANING depends on, re-checked at execution.
	Dependencies datatypes.JSON `gorm:"not null"`
	Digest       string         `gorm:"size:80;not null"`
	Impact       string         `gorm:"size:16;not null"`
	TargetCount  int            `gorm:"not null"`
	// ProposerSub is claims.Username: the same key the audit journal's Actor uses.
	ProposerSub    string  `gorm:"size:320;not null;index"`
	ProposerClient string  `gorm:"size:255;not null;index"`
	ClientAttested bool    `gorm:"not null"`
	ProposerSid    *string `gorm:"size:64"`
	// Provenance is the session's taint summary; NULL is "unknown", which reads as maximal.
	Provenance *datatypes.JSON
	// Rationale is the agent's own explanation: UNTRUSTED text, stored and rendered as data.
	Rationale         *string `gorm:"size:2048"`
	PolicyVersion     int     `gorm:"not null"`
	RequiredApprovals int     `gorm:"not null"`
	// Approvals is the JSON list of Approval, distinct principals.
	Approvals datatypes.JSON `gorm:"not null"`
	// Revision counts approval writes: every approval is a compare-and-set on it.
	Revision int `gorm:"not null"`
	// Reasons are the platform-authored codes of the last decision about the proposal.
	Reasons    datatypes.JSON `gorm:"not null"`
	ExpiresAt  time.Time      `gorm:"not null;index"`
	ResultRef  *string        `gorm:"size:128"`
	DecidedAt  *time.Time
	DecidedBy  *string `gorm:"size:320"`
	Note       *string `gorm:"size:512"`
	ExecutedAt *time.Time
}

// Approval is one person's approval of a proposal. It stands only while its sign-in is open
// under the identity's current epoch. An empty Sid predates binding and never counts.
type Approval struct {
	Sid     string     `json:"sid,omitempty"`
	Sub     string     `json:"sub"`
	Amr     []string   `json:"amr,omitempty"`
	MfaTime *time.Time `json:"mfaTime,omitempty"`
	At      time.Time  `json:"at"`
}

// ParseApprovals reads a stored approvals list. A list that cannot be read is an error, never an
// empty list: reading it as empty would let the next approval stand alone.
func ParseApprovals(raw []byte) ([]Approval, error) {
	if len(raw) == 0 {
		return nil, errors.New("actiongate: the stored approvals are missing")
	}
	var out []Approval
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("actiongate: the stored approvals cannot be read: %w", err)
	}
	return out, nil
}

// Principals returns the distinct principals of approvals, in order.
func Principals(approvals []Approval) []string {
	var out []string
	for _, a := range approvals {
		if a.Sub != "" && !slices.Contains(out, a.Sub) {
			out = append(out, a.Sub)
		}
	}
	return out
}

// ApproverSessions returns distinct principals' recorded sign-ins in order, skipping empty sids.
func ApproverSessions(approvals []Approval) []ApproverSession {
	out := []ApproverSession{}
	seen := map[string]bool{}
	for _, a := range approvals {
		if a.Sub != "" && a.Sid != "" && !seen[a.Sub] {
			out = append(out, ApproverSession{Principal: a.Sub, Sid: a.Sid})
			seen[a.Sub] = true
		}
	}
	return out
}

// Tally counts the approvals that stand: distinct principals that user-management still answers
// as valid approvers (valid), never the proposer when two approvals are required or self-confirm is
// not allowed. A principal approving twice counts once, and an approval made by someone who has
// since lost the role, the authority or the membership does not count at all: EVERY counted
// approval is re-validated, not only the latest. An approval whose second factor is older than
// the CURRENT policy's step-up at the moment of the tally does not count. An empty Sid never counts.
func Tally(approvals []Approval, valid []string, proposer string, required int, selfConfirm bool, su auth.StepUp, now time.Time) int {
	n := 0
	for _, p := range Principals(approvals) {
		if !slices.Contains(valid, p) {
			continue
		}
		if p == proposer && (required > 1 || !selfConfirm) {
			continue
		}
		for _, a := range approvals {
			if a.Sub == p && a.Sid != "" && auth.StepUpFreshAt(a.Amr, a.MfaTime, su, now) {
				n++
				break
			}
		}
	}
	return n
}

// ErrNotPending is the refusal of a transition whose row is no longer in the state it was read
// in, or whose approval window has closed: a concurrent writer got there first, or the proposal
// expired. Nothing was written.
var ErrNotPending = gateErr(CodeNotPending, nil, "the proposal is no longer pending")

// Transition moves the proposal token of model from -> to, setting the other columns in set, as
// ONE conditional update: it applies only if the row is still in from (and, leaving PROPOSED for
// anything but EXPIRED, still inside its expiry), and it reports ErrNotPending when nothing
// matched.
//
// 🔴 IT GOES THROUGH THE ORM, NEVER RAW SQL. The audit journal records gorm statements only
// (raw Exec is never journaled), so every ledger transition is written this way and leaves its
// journal row in the same transaction.
//
// model is a pointer to the area's zero-value proposal model, which gorm reads for the table. The
// tenant predicate is the tenant-scope callback's, as for every tenant-scoped statement.
func Transition(tx *gorm.DB, model any, token string, from, to State, now time.Time, set map[string]any) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("actiongate: %s -> %s is not a transition", from, to)
	}
	updates := map[string]any{}
	for k, v := range set {
		updates[k] = v
	}
	updates["state"] = string(to)
	q := tx.Model(model).Where("token = ? AND state = ?", token, string(from))
	if from == StateProposed && to != StateExpired {
		// Every move out of PROPOSED but expiry itself happens only inside the window, so a late
		// reaper can never let a stale approval through.
		q = q.Where("expires_at > ?", now)
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotPending
	}
	return nil
}

// RecordApproval stores approvals, with a new entry appended or the principal's stale entry
// replaced, on the proposal token as a compare-and-set on its revision, with requiredApprovals
// as the current policy states
// it: it applies only if the row is still PROPOSED, inside its expiry and at revision.
// ErrNotPending means another approval (or a transition) landed first; the caller re-reads and
// decides again.
func RecordApproval(tx *gorm.DB, model any, token string, revision int, approvals []Approval, requiredApprovals int, now time.Time) error {
	raw, err := json.Marshal(approvals)
	if err != nil {
		return err
	}
	res := tx.Model(model).
		Where("token = ? AND state = ? AND revision = ? AND expires_at > ?", token, string(StateProposed), revision, now).
		Updates(map[string]any{"approvals": datatypes.JSON(raw), "revision": revision + 1, "required_approvals": requiredApprovals})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotPending
	}
	return nil
}

// ExpireDue moves every PROPOSED row of model whose expiry has passed to EXPIRED, across tenants
// (ctx must be a system context). It is idempotent and HA-safe: two replicas running it at once
// each move what the other has not, and the conditional update cannot move a row twice. The
// approval paths enforce the expiry themselves, so a reaper that is late, or not running, never
// lets a stale approval through: this only makes the ledger say so.
func ExpireDue(ctx context.Context, db *gorm.DB, model any, now time.Time) (int64, error) {
	res := db.WithContext(ctx).Model(model).
		Where("state = ? AND expires_at <= ?", string(StateProposed), now).
		Updates(map[string]any{"state": string(StateExpired), "decided_at": now})
	return res.RowsAffected, res.Error
}
