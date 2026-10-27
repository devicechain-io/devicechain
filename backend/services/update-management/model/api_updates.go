// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Refusal codes of the assignment plane (extensions.code on the GraphQL plane).
const (
	CodeDeviceNotFound          = "DEVICE_NOT_FOUND"
	CodeIncompatibleProfile     = "INCOMPATIBLE_PROFILE"
	CodeDeviceLookupUnavailable = "DEVICE_LOOKUP_UNAVAILABLE"
	CodeDeviceChannelUnwired    = "DEVICE_CHANNEL_UNAVAILABLE"
	CodeArtifactNotAvailable    = "ARTIFACT_NOT_AVAILABLE"
	CodeUnverifiedRefused       = "UNVERIFIED_ARTIFACT_REFUSED"
	CodeUpdateInProgress        = "UPDATE_IN_PROGRESS"
	CodeAssignmentNotFound      = "ASSIGNMENT_NOT_FOUND"
	CodeAttemptTerminal         = "UPDATE_ALREADY_FINISHED"
	CodeBeyondRecall            = "UPDATE_BEYOND_RECALL"
	CodeInvalidAssignment       = "INVALID_ASSIGNMENT"
	CodeNotUnknown              = "UPDATE_NOT_UNKNOWN"
	CodeInvalidReason           = "INVALID_REASON"
	// CodeDeviceDisabled: an operator has disabled the device (quarantined it), so nothing can be
	// assigned to it. The same code device-management gives a command to a disabled device.
	CodeDeviceDisabled = "DEVICE_DISABLED"
)

// Verdicts the platform adds to the reducer's own: a report that names no assignment this device
// has, which the reducer never sees because there is no attempt to give it.
const (
	VerdictUnknownAssignment ota.Verdict = "UNKNOWN_ASSIGNMENT"
)

// casRetries bounds how many times a write that lost a compare-and-set re-reads and tries again.
// The contenders are one device's reports, a deadline sweep and an operator's cancel, so more than
// a couple of losses in a row means something is wrong rather than busy.
const casRetries = 6

// DeviceFacts is what update-management asks device-management about a device.
type DeviceFacts struct {
	// Profile is the token of the device-profile the device resolves through, empty for a device
	// whose type has none.
	Profile string
	// Disabled is true while an operator has the device disabled (quarantined).
	Disabled bool
	// Anchors are the device's scope anchors (ADR-081): one "type:token" per tracked
	// relationship, empty for a device nothing owns. An assignment is stamped with them.
	Anchors []string
}

// DeviceLookup resolves a device in device-management. found is false for a device the tenant does
// not have; an error means the answer could not be obtained, which callers treat as a refusal and
// never as "compatible" or "enabled".
type DeviceLookup interface {
	DeviceOf(ctx context.Context, tenant, deviceToken string) (facts DeviceFacts, found bool, err error)
}

// ErrDeviceDisabled is returned by the device-facing operations (a report, a sync answer, a
// download grant) for a device an operator has disabled. It is permanent for as long as the device
// stays disabled, so nothing that receives it should retry.
var ErrDeviceDisabled = errors.New("the device is disabled")

// DisabledCacheTTL bounds how stale the disabled answer behind the device-facing operations can be.
// A disable therefore reaches update-management within this long (an enable likewise). It is
// short on purpose: it exists to spare device-management one lookup per message in a burst (a sync
// storm after a reconnect, a download's progress reports), not to remember a device.
const DisabledCacheTTL = 5 * time.Second

// maxDisabledCache bounds the cache's memory; past it, expired entries are swept and, if it is
// still full, the answer is simply not remembered (the next message asks again).
const maxDisabledCache = 50_000

type disabledEntry struct {
	disabled bool
	at       time.Time
}

// disabledCache remembers, per (tenant, device), whether the device was disabled when last asked.
// Both answers are kept for DisabledCacheTTL; a failed lookup is never kept.
type disabledCache struct {
	mu sync.Mutex
	m  map[string]disabledEntry
}

func (c *disabledCache) get(key string, now time.Time) (disabled, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, hit := c.m[key]
	if !hit || now.Sub(e.at) >= DisabledCacheTTL || now.Before(e.at) {
		return false, false
	}
	return e.disabled, true
}

func (c *disabledCache) put(key string, disabled bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]disabledEntry{}
	}
	if len(c.m) >= maxDisabledCache {
		for k, e := range c.m {
			if now.Sub(e.at) >= DisabledCacheTTL {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxDisabledCache {
			return
		}
	}
	c.m[key] = disabledEntry{disabled: disabled, at: now}
}

func (c *disabledCache) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// DeviceRosterChanged reacts to device-management's device-roster fact for a device, which it
// emits whenever an operator disables or enables the device (and on a create or a re-type).
//
// It always drops the device's cached disabled answer, so the change is felt by the very next
// device-facing message instead of up to DisabledCacheTTL later. The fact is only a prompt to
// ask again, never the answer: facts are at-most-once and may arrive out of order, so a stale one
// can do no more than cost a lookup. (The cache is per replica and the fact reaches one of them;
// the others keep the TTL bound.)
//
// Only a fact that marks a disabled-to-enabled TOGGLE (enabled=true) also re-offers the device what
// it should be working on, exactly as a sync would be answered. That is the ENABLE half: a sync the
// device made while disabled was refused without a word, so it cannot tell that from "nothing
// assigned" and would otherwise wait for its next boot. The re-offer asks device-management afresh
// (the cache was just dropped), so a fact that claims an enable for a device that is in truth still
// disabled re-offers nothing. A create, a re-type or a type re-point fact (which a campaign can
// fan out to thousands of devices) and a disable fact drop the cache and cost no lookup; a producer
// that predates the field sends enabled=false, and such a device is caught up by its next sync. An
// error means the re-offer could not be decided and the caller retries; ErrDeviceDisabled never is one.
func (api *Api) DeviceRosterChanged(ctx context.Context, deviceToken string, enabled bool) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return errors.New("a device roster change requires a tenant")
	}
	api.disabled.forget(tenant + "/" + deviceToken)
	if !enabled {
		return nil
	}
	views, err := api.CurrentUpdates(ctx, deviceToken)
	if errors.Is(err, ErrDeviceDisabled) {
		return nil
	}
	if err != nil {
		return err
	}
	api.PublishSyncAnswer(ctx, deviceToken, views)
	return nil
}

// refuseIfDisabled is the quarantine gate of the device-facing operations. A device an operator
// has disabled keeps whatever session it had open, so the broker cannot be what cuts it off here:
// every report, sync answer and download grant asks device-management (through a cache no staler
// than DisabledCacheTTL) and refuses with ErrDeviceDisabled. Enabling takes effect within the same
// bound, because only the answer is remembered, never a decision. A device device-management does
// not have is not refused here: it has no assignment, and the operation answers that on its own.
//
// It FAILS CLOSED everywhere it cannot answer: a lookup that errors (not cached) and a service
// with no lookup wired both return an error and the message is retried, then dead-lettered. Assignments
// outlive a restart, so "nothing is assigned without a lookup" is not a premise this can lean on.
func (api *Api) refuseIfDisabled(ctx context.Context, deviceToken string) error {
	if api.Devices == nil {
		return refuse(CodeDeviceLookupUnavailable,
			"device-facing update traffic is refused: devices cannot be looked up, because the device-management endpoint or service secret is not configured")
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return errors.New("a device-facing update operation requires a tenant")
	}
	key := tenant + "/" + deviceToken
	if disabled, hit := api.disabled.get(key, api.now()); hit {
		if disabled {
			return ErrDeviceDisabled
		}
		return nil
	}
	facts, found, err := api.Devices.DeviceOf(ctx, tenant, deviceToken)
	if err != nil {
		return fmt.Errorf("checking whether device %q is disabled: %w", deviceToken, err)
	}
	disabled := found && facts.Disabled
	api.disabled.put(key, disabled, api.now())
	if disabled {
		return ErrDeviceDisabled
	}
	return nil
}

// DesiredPublisher puts one encoded message on a device's desired-state channel. The tenant comes
// from ctx. Delivery is at-most-once and best effort: a message that does not arrive is repaired by
// the device's next sync, so a failure never fails the write that caused it.
type DesiredPublisher interface {
	PublishDesired(ctx context.Context, deviceToken string, body []byte) error
}

func (api *Api) now() time.Time {
	if api.Clock != nil {
		return api.Clock().UTC()
	}
	return time.Now().UTC()
}

// newToken is a random wire identifier: the prefix, then 128 bits of hex. Unguessable on purpose:
// an assignment id on the wire is not a capability, but an id a device could enumerate would turn
// every wrong guess into a free probe.
func newToken(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b[:]), nil
}

func actorOf(ctx context.Context) string {
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		return firstNonEmpty(claims.Username, claims.Email)
	}
	return ""
}

// assignmentByToken returns the device's assignment with the given token, current or superseded.
// Both the tenant and the device are part of the lookup: a token alone does not name an assignment
// a different device could read.
func (api *Api) assignmentByToken(ctx context.Context, deviceToken, token string) (*UpdateAssignment, error) {
	var found []UpdateAssignment
	if err := api.RDB.DB(ctx).Where("device_token = ? AND token = ?", deviceToken, token).
		Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

// AssignmentByToken returns the tenant's assignment with the given token and its attempt and
// artifact, or nil when there is none.
func (api *Api) AssignmentByToken(ctx context.Context, token string) (*UpdateAssignmentView, error) {
	var found []UpdateAssignment
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", []string{token}); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	views, err := api.viewsOf(ctx, found)
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

func (api *Api) attemptOf(ctx context.Context, assignmentID uint) (*UpdateAttempt, error) {
	var found []UpdateAttempt
	if err := api.RDB.DB(ctx).Where("assignment_id = ?", assignmentID).Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

func (api *Api) artifactByID(ctx context.Context, id uint) (*Artifact, error) {
	var found []Artifact
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "id", []uint{id}); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	if err := api.fillTrust(ctx, &found[0]); err != nil {
		return nil, err
	}
	return &found[0], nil
}

// viewsOf joins assignments to their attempts and artifacts with two reads, however many rows.
//
// The joins run under joinedReadContext: asg was read through the scope predicate, so for a
// member limited to part of the tenant these are the attempts and artifacts of assignments it
// may already see, and nothing else.
func (api *Api) viewsOf(ctx context.Context, asg []UpdateAssignment) ([]UpdateAssignmentView, error) {
	ctx = joinedReadContext(ctx)
	ids := make([]uint, 0, len(asg))
	artIDs := make([]uint, 0, len(asg))
	for _, a := range asg {
		ids = append(ids, a.ID)
		artIDs = append(artIDs, a.ArtifactId)
	}
	var attempts []UpdateAttempt
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &attempts, "assignment_id", ids); err != nil {
		return nil, err
	}
	var artifacts []Artifact
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &artifacts, "id", artIDs); err != nil {
		return nil, err
	}
	byAssignment := make(map[uint]UpdateAttempt, len(attempts))
	for _, at := range attempts {
		byAssignment[at.AssignmentId] = at
	}
	ptrs := make([]*Artifact, len(artifacts))
	for i := range artifacts {
		ptrs[i] = &artifacts[i]
	}
	if err := api.fillTrust(ctx, ptrs...); err != nil {
		return nil, err
	}
	byID := make(map[uint]Artifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}
	pins, err := api.pinsOf(api.RDB.DB(ctx), asg)
	if err != nil {
		return nil, err
	}
	out := make([]UpdateAssignmentView, 0, len(asg))
	for _, a := range asg {
		at, ok := byAssignment[a.ID]
		if !ok {
			return nil, fmt.Errorf("update assignment %q has no attempt row", a.Token)
		}
		v := UpdateAssignmentView{Assignment: a, Attempt: at, Artifact: byID[a.ArtifactId]}
		if a.SignatureId.Valid {
			if p, ok := pins[a.ID]; ok {
				v.Pin = &p
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// pinsOf reads the pinned signatures of the assignments, and the signer row of each, with two
// reads however many, keyed by assignment id. A pin whose signature or signer row cannot be found,
// or whose signature belongs to a different artifact than the assignment names, is simply absent
// from the result: the caller tells that apart from "pins nothing" by the assignment's SignatureId.
func (api *Api) pinsOf(db *gorm.DB, asg []UpdateAssignment) (map[uint]SignaturePin, error) {
	ids := make([]uint, 0, len(asg))
	for _, a := range asg {
		if a.SignatureId.Valid {
			ids = append(ids, uint(a.SignatureId.Int64))
		}
	}
	out := make(map[uint]SignaturePin, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var sigs []ArtifactSignature
	if err := rdb.FindByKeys(db, &sigs, "id", ids); err != nil {
		return nil, err
	}
	signerIDs := make([]uint, 0, len(sigs))
	for _, s := range sigs {
		signerIDs = append(signerIDs, s.SignerId)
	}
	var signers []ArtifactSigner
	if len(signerIDs) > 0 {
		if err := rdb.FindByKeys(db, &signers, "id", signerIDs); err != nil {
			return nil, err
		}
	}
	byID := make(map[uint]ArtifactSigner, len(signers))
	for _, s := range signers {
		byID[s.ID] = s
	}
	sigByID := make(map[uint]ArtifactSignature, len(sigs))
	for _, s := range sigs {
		sigByID[s.ID] = s
	}
	for _, a := range asg {
		if !a.SignatureId.Valid {
			continue
		}
		s, ok := sigByID[uint(a.SignatureId.Int64)]
		if !ok || s.ArtifactId != a.ArtifactId {
			continue
		}
		if signer, ok := byID[s.SignerId]; ok {
			out[a.ID] = SignaturePin{Signature: s, Signer: signer}
		}
	}
	return out, nil
}

// UpdateAssignments searches the tenant's assignments, newest first.
func (api *Api) UpdateAssignments(ctx context.Context, c UpdateAssignmentCriteria) (*UpdateAssignmentResults, error) {
	results := make([]UpdateAssignment, 0)
	db, pag := api.RDB.ListOf(ctx, &UpdateAssignment{}, func(q *gorm.DB) *gorm.DB {
		if c.DeviceToken != nil {
			q = q.Where("device_token = ?", *c.DeviceToken)
		}
		if c.Component != nil {
			q = q.Where("component = ?", *c.Component)
		}
		if c.CurrentOnly {
			q = q.Where("superseded_at IS NULL")
		}
		return q
	}, c.Pagination)
	if err := db.Find(&results).Error; err != nil {
		return nil, err
	}
	views, err := api.viewsOf(ctx, results)
	if err != nil {
		return nil, err
	}
	return &UpdateAssignmentResults{Results: views, Pagination: pag}, nil
}

// requireUpdatePlane refuses an operation whose collaborators are not wired. An assignment that
// could not reach its device, or whose device could not be checked, is worse than no assignment.
func (api *Api) requireUpdatePlane() error {
	if api.Devices == nil {
		return refuse(CodeDeviceLookupUnavailable,
			"devices cannot be looked up: the device-management endpoint or service secret is not configured")
	}
	if api.Desired == nil {
		return refuse(CodeDeviceChannelUnwired, "the device channel is not available, so an update cannot be delivered")
	}
	return nil
}

// AssignArtifact tells a device to install an artifact.
//
// The artifact must be AVAILABLE, its trust acceptable under the instance's fixed
// updates.allowUnverifiedArtifacts, and the device's profile one it is compatible with. A device
// is working toward one target per component: assigning while the previous attempt is still live
// is refused (cancel it first), assigning the same artifact again is idempotent, and assigning
// after the previous attempt finished supersedes it without deleting it.
func (api *Api) AssignArtifact(ctx context.Context, deviceToken, artifactToken string) (*UpdateAssignmentView, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("assigning an artifact requires a tenant")
	}
	if err := api.requireUpdatePlane(); err != nil {
		return nil, err
	}
	if err := core.ValidateToken(deviceToken); err != nil {
		return nil, refuse(CodeInvalidAssignment, "deviceToken: %v", err)
	}
	artifact, err := api.ArtifactByToken(ctx, artifactToken)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, ErrNotFound(artifactToken)
	}
	if artifact.State != StateAvailable {
		return nil, refuse(CodeArtifactNotAvailable, "artifact %q is %s; only an %s artifact can be assigned",
			artifact.Token, artifact.State, StateAvailable)
	}
	// The gate reads the DERIVED trust (ArtifactByToken filled it), never the stored column.
	if artifact.Trust != TrustSigned && !api.AllowUnverified {
		return nil, refuse(CodeUnverifiedRefused,
			"artifact %q is UNVERIFIED and this instance does not allow assigning unverified artifacts "+
				"(updates.allowUnverifiedArtifacts)", artifact.Token)
	}
	facts, found, err := api.Devices.DeviceOf(ctx, tenant, deviceToken)
	if err != nil {
		return nil, refuse(CodeDeviceLookupUnavailable, "the device could not be looked up in device-management: %v", err)
	}
	if !found {
		return nil, refuse(CodeDeviceNotFound, "no device with token %q", deviceToken)
	}
	if facts.Disabled {
		return nil, refuse(CodeDeviceDisabled, "device %q is disabled; enable it before assigning an update", deviceToken)
	}
	var compatible []string
	if err := json.Unmarshal(artifact.CompatibleProfiles, &compatible); err != nil {
		return nil, fmt.Errorf("artifact %q has an unreadable compatibleProfiles document: %w", artifact.Token, err)
	}
	if facts.Profile == "" || !slices.Contains(compatible, facts.Profile) {
		return nil, refuse(CodeIncompatibleProfile,
			"device %q (profile %q) is not on artifact %q's compatible profile list", deviceToken, facts.Profile, artifact.Token)
	}

	var view *UpdateAssignmentView
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		v, err := api.assignInTx(ctx, tx, deviceToken, artifact, facts.Anchors)
		view = v
		return err
	})
	if err != nil {
		return nil, err
	}
	api.publishAssignment(ctx, view)
	return view, nil
}

func (api *Api) assignInTx(ctx context.Context, tx *gorm.DB, deviceToken string, artifact *Artifact,
	anchors []string) (*UpdateAssignmentView, error) {
	now := api.now()
	var current []UpdateAssignment
	if err := tx.Where("device_token = ? AND component = ? AND superseded_at IS NULL", deviceToken, artifact.Component).
		Limit(1).Find(&current).Error; err != nil {
		return nil, err
	}
	if len(current) == 1 {
		cur := current[0]
		var at []UpdateAttempt
		if err := tx.Where("assignment_id = ?", cur.ID).Limit(1).Find(&at).Error; err != nil {
			return nil, err
		}
		if len(at) == 0 {
			return nil, fmt.Errorf("update assignment %q has no attempt row", cur.Token)
		}
		if !ota.State(at[0].State).Terminal() {
			if cur.ArtifactId == artifact.ID {
				v := &UpdateAssignmentView{Assignment: cur, Attempt: at[0], Artifact: *artifact}
				if cur.SignatureId.Valid {
					pins, err := api.pinsOf(tx, []UpdateAssignment{cur})
					if err != nil {
						return nil, err
					}
					if p, ok := pins[cur.ID]; ok {
						v.Pin = &p
					}
				}
				return v, nil
			}
			return nil, refuse(CodeUpdateInProgress,
				"device %q is already working on update %q (%s); cancel it before assigning another",
				deviceToken, cur.Token, at[0].State)
		}
		res := tx.Model(&UpdateAssignment{ID: cur.ID, Token: cur.Token}).Where("id = ? AND superseded_at IS NULL", cur.ID).
			Update("superseded_at", now)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, refuse(CodeUpdateInProgress, "device %q was assigned an update at the same moment", deviceToken)
		}
	}

	asgTok, err := newToken("asg")
	if err != nil {
		return nil, err
	}
	attTok, err := newToken("att")
	if err != nil {
		return nil, err
	}
	target := ota.Target{Version: artifact.Version, Digest: artifact.Digest, RequiresReboot: artifact.Kind == KindFirmware}
	seed, err := ota.NewAttempt(attTok, asgTok, artifact.Component, target, now)
	if err != nil {
		return nil, refuse(CodeInvalidAssignment, "%v", err)
	}
	pin, err := api.pinSignature(tx, artifact)
	if err != nil {
		return nil, err
	}
	if pin == nil && !api.AllowUnverified {
		// The trust the gate read was derived outside this transaction; the signer behind it was
		// revoked between that read and this one.
		return nil, refuse(CodeUnverifiedRefused,
			"artifact %q is UNVERIFIED and this instance does not allow assigning unverified artifacts "+
				"(updates.allowUnverifiedArtifacts)", artifact.Token)
	}
	asg := UpdateAssignment{Token: asgTok, DeviceToken: deviceToken, Component: artifact.Component,
		ArtifactId: artifact.ID, Actor: actorOf(ctx), ScopeAnchors: rdb.Anchors(anchors)}
	if pin != nil {
		asg.SignatureId = sql.NullInt64{Int64: int64(pin.Signature.ID), Valid: true}
	}
	if err := tx.Create(&asg).Error; err != nil {
		// The partial unique index admits one current row per device and component, so a
		// concurrent assign that got in first surfaces here as a constraint error.
		var again []UpdateAssignment
		if q := tx.Where("device_token = ? AND component = ? AND superseded_at IS NULL", deviceToken, artifact.Component).
			Limit(1).Find(&again); q.Error == nil && len(again) == 1 {
			return nil, refuse(CodeUpdateInProgress, "device %q was assigned an update at the same moment", deviceToken)
		}
		return nil, err
	}
	att := UpdateAttempt{
		Token: seed.AttemptID, AssignmentId: asg.ID, DeviceToken: deviceToken, Component: seed.Component,
		TargetVersion: target.Version, TargetDigest: target.Digest, RequiresReboot: target.RequiresReboot,
		State: string(seed.State), LastReportAt: seed.LastReportAt,
	}
	if err := tx.Create(&att).Error; err != nil {
		return nil, err
	}
	return &UpdateAssignmentView{Assignment: asg, Attempt: att, Artifact: *artifact, Pin: pin}, nil
}

// pinSignature picks the signature an assignment is made under: the OLDEST signature of the
// artifact whose signer is ACTIVE, or nil when there is none. Oldest, so the choice does not move
// when a newer signer is added.
//
// Each candidate signer row is read FOR SHARE (PostgreSQL), so a revoke that is concurrent with
// this assignment serializes with it: either the revoke committed first and the row reads
// REVOKED, or it waits until this transaction commits and the pin it leaves behind is the one
// every later message uses. The lock is a no-op on SQLite, which the unit tests run on.
func (api *Api) pinSignature(tx *gorm.DB, artifact *Artifact) (*SignaturePin, error) {
	var sigs []ArtifactSignature
	if err := tx.Where("artifact_id = ?", artifact.ID).Order("created_at ASC, id ASC").Find(&sigs).Error; err != nil {
		return nil, err
	}
	for _, sig := range sigs {
		q := tx
		if tx.Dialector.Name() == "postgres" {
			q = tx.Clauses(clause.Locking{Strength: "SHARE"})
		}
		var signers []ArtifactSigner
		if err := q.Where("id = ?", sig.SignerId).Limit(1).Find(&signers).Error; err != nil {
			return nil, err
		}
		if len(signers) == 1 && signers[0].State == SignerActive {
			return &SignaturePin{Signature: sig, Signer: signers[0]}, nil
		}
	}
	return nil, nil
}

// saveAttempt writes the columns a reducer step decided, only if nobody changed the row since it
// was read. It reports whether it won; a loser re-reads and tries again.
func (api *Api) saveAttempt(ctx context.Context, row UpdateAttempt, next ota.Attempt) (bool, error) {
	if api.beforeSave != nil {
		api.beforeSave(row)
	}
	cols := otaColumns(next)
	cols["version"] = row.Version + 1
	cols["updated_at"] = api.now()
	res := api.RDB.DB(ctx).Model(&UpdateAttempt{ID: row.ID, Token: row.Token}).
		Where("id = ? AND version = ?", row.ID, row.Version).Updates(cols)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// CancelUpdate stops an update. From QUEUED it takes effect at once; while the device is before
// installation it records the request and waits for the device's ABANDONED; from installation on it
// is refused as beyond recall. In every accepted case the device is told, because it may already
// hold the assignment.
func (api *Api) CancelUpdate(ctx context.Context, assignmentToken string) (*UpdateAssignmentView, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	if api.Desired == nil {
		return nil, refuse(CodeDeviceChannelUnwired, "the device channel is not available, so a cancel cannot be delivered")
	}
	for range casRetries {
		view, err := api.AssignmentByToken(ctx, assignmentToken)
		if err != nil {
			return nil, err
		}
		if view == nil {
			return nil, refuse(CodeAssignmentNotFound, "no update assignment with token %q", assignmentToken)
		}
		cur := view.Attempt.ToOTA(view.Assignment.Token)
		next, err := ota.Cancel(cur, api.now())
		switch {
		case errors.Is(err, ota.ErrTerminal):
			return nil, refuse(CodeAttemptTerminal, "update %q has already finished (%s)", assignmentToken, view.Attempt.State)
		case errors.Is(err, ota.ErrBeyondRecall):
			return nil, refuse(CodeBeyondRecall, "update %q is %s; installation has begun and cannot be recalled",
				assignmentToken, view.Attempt.State)
		case err != nil:
			return nil, err
		}
		won, err := api.saveAttempt(ctx, view.Attempt, next)
		if err != nil {
			return nil, err
		}
		if !won {
			continue
		}
		api.publishCancel(ctx, view.Assignment.DeviceToken, view.Attempt.Token, view.Assignment.Token)
		return api.AssignmentByToken(ctx, assignmentToken)
	}
	return nil, fmt.Errorf("update %q changed too often to cancel; try again", assignmentToken)
}

// AbandonUnknown ends an UNKNOWN attempt because an operator decided to stop waiting for the device
// to say what it runs. It is a separate, explicit act from CancelUpdate, which refuses UNKNOWN: the
// device may have installed the update, and nothing here learns whether it did. The attempt ends
// CANCELLED with the reason stored on it (the failure detail, code OPERATOR_ABANDONED), the write is
// journaled under the operator, and the device is sent an update.cancel as for any other cancel.
// Afterwards the component is free for a new assignment. Check the device inventory: its state is
// unknown, not reverted.
func (api *Api) AbandonUnknown(ctx context.Context, assignmentToken, reason string) (*UpdateAssignmentView, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	if api.Desired == nil {
		return nil, refuse(CodeDeviceChannelUnwired, "the device channel is not available, so the device cannot be told")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, refuse(CodeInvalidReason, "a reason is required to abandon an update whose outcome is unknown")
	}
	for range casRetries {
		view, err := api.AssignmentByToken(ctx, assignmentToken)
		if err != nil {
			return nil, err
		}
		if view == nil {
			return nil, refuse(CodeAssignmentNotFound, "no update assignment with token %q", assignmentToken)
		}
		next, err := ota.Abandon(view.Attempt.ToOTA(view.Assignment.Token), reason)
		switch {
		case errors.Is(err, ota.ErrTerminal):
			return nil, refuse(CodeAttemptTerminal, "update %q has already finished (%s)", assignmentToken, view.Attempt.State)
		case errors.Is(err, ota.ErrNotUnknown):
			return nil, refuse(CodeNotUnknown, "update %q is %s; only an UNKNOWN update can be abandoned (use cancelUpdate)",
				assignmentToken, view.Attempt.State)
		case err != nil:
			return nil, refuse(CodeInvalidReason, "%v", err)
		}
		won, err := api.saveAttempt(ctx, view.Attempt, next)
		if err != nil {
			return nil, err
		}
		if !won {
			continue
		}
		log.Warn().Str("assignment", assignmentToken).Str("device", view.Assignment.DeviceToken).Str("by", actorOf(ctx)).
			Str("reason", reason).Msg("An UNKNOWN update was abandoned by an operator; the device state is not known.")
		api.publishCancel(ctx, view.Assignment.DeviceToken, view.Attempt.Token, view.Assignment.Token)
		return api.AssignmentByToken(ctx, assignmentToken)
	}
	return nil, fmt.Errorf("update %q changed too often to abandon; try again", assignmentToken)
}

func (api *Api) publishAssignment(ctx context.Context, v *UpdateAssignmentView) {
	if v.Attempt.CancelRequested || ota.State(v.Attempt.State).Terminal() {
		return
	}
	trust, sig := ota.TrustUnverified, (*ota.AssignmentSignature)(nil)
	if !v.Assignment.SignatureId.Valid && !api.AllowUnverified {
		// An assignment that pins no signature was made while the instance allowed unverified
		// artifacts. The instance no longer does, so it is not (re)sent: the device keeps what it
		// had, and the operator re-assigns a signed artifact.
		api.Undelivered.Add(1)
		log.Warn().Str("assignment", v.Assignment.Token).Str("device", v.Assignment.DeviceToken).
			Msg("An unsigned update assignment is not delivered: this instance no longer allows unverified artifacts (updates.allowUnverifiedArtifacts).")
		return
	}
	if v.Assignment.SignatureId.Valid {
		// The assignment was made under a signature. If that signature or its signer cannot be
		// read, the message is NOT sent: sending it as UNVERIFIED would silently downgrade an
		// assignment the platform decided was signed, which a device that does not require
		// signatures would then install.
		if v.Pin == nil {
			api.Undelivered.Add(1)
			log.Error().Str("assignment", v.Assignment.Token).Int64("signature", v.Assignment.SignatureId.Int64).
				Msg("An update assignment's pinned signature or signer cannot be read; it is not delivered.")
			return
		}
		alg, err := WireAlgorithm(v.Pin.Signer.Algorithm)
		if err != nil {
			api.Undelivered.Add(1)
			log.Error().Err(err).Str("assignment", v.Assignment.Token).
				Msg("An update assignment's pinned signer has an unmappable algorithm; it is not delivered.")
			return
		}
		trust = ota.TrustSigned
		sig = &ota.AssignmentSignature{
			Signature: base64.StdEncoding.EncodeToString(v.Pin.Signature.Signature),
			KeyID:     v.Pin.Signer.KeyId, Algorithm: alg,
		}
	}
	body, err := ota.EncodeUpdateAssignment(v.Attempt.Token, v.Assignment.Token, v.Assignment.Component, trust, sig, ota.AssignmentArtifact{
		Version: v.Artifact.Version, Digest: v.Artifact.Digest, Size: v.Artifact.SizeBytes, RequiresReboot: v.Attempt.RequiresReboot,
	})
	if err != nil {
		log.Error().Err(err).Str("assignment", v.Assignment.Token).Msg("An update assignment could not be framed; it is not delivered.")
		return
	}
	api.publish(ctx, v.Assignment.DeviceToken, body, "update.assignment")
}

func (api *Api) publishCancel(ctx context.Context, deviceToken, attemptToken, assignmentToken string) {
	body, err := ota.EncodeUpdateCancel(attemptToken, assignmentToken)
	if err != nil {
		log.Error().Err(err).Str("assignment", assignmentToken).Msg("An update cancel could not be framed; it is not delivered.")
		return
	}
	api.publish(ctx, deviceToken, body, "update.cancel")
}

func (api *Api) publish(ctx context.Context, deviceToken string, body []byte, kind string) {
	if api.Desired == nil {
		return
	}
	if err := api.Desired.PublishDesired(ctx, deviceToken, body); err != nil {
		log.Error().Err(err).Str("device", deviceToken).Str("kind", kind).
			Msg("Unable to publish to a device's desired channel; the device picks the state up at its next sync.")
	}
}

// PublishCancelFor tells a device to drop an assignment the platform no longer holds for it. The
// attempt may be unknown (empty).
func (api *Api) PublishCancelFor(ctx context.Context, deviceToken, attemptToken, assignmentToken string) {
	api.publishCancel(ctx, deviceToken, attemptToken, assignmentToken)
}

// ApplyReport folds one update.progress or update.inventory report from a device into its attempt
// with the core reducer, and stores the result when the reducer applied it. The device is the one
// the subject named; a report can only ever reach that device's own assignments.
func (api *Api) ApplyReport(ctx context.Context, deviceToken string, rep ota.Report) (ota.Verdict, error) {
	if err := api.refuseIfDisabled(ctx, deviceToken); err != nil {
		return "", err
	}
	if rep.Kind == ota.KindInventory {
		return api.applyInventory(ctx, deviceToken, rep)
	}
	for range casRetries {
		asg, err := api.assignmentByToken(ctx, deviceToken, rep.AssignmentID)
		if err != nil {
			return "", err
		}
		if asg == nil {
			return VerdictUnknownAssignment, nil
		}
		row, err := api.attemptOf(ctx, asg.ID)
		if err != nil {
			return "", err
		}
		if row == nil {
			return "", fmt.Errorf("update assignment %q has no attempt row", asg.Token)
		}
		next, verdict := ota.Apply(row.ToOTA(asg.Token), rep, api.now())
		if verdict != ota.VerdictApplied {
			return verdict, nil
		}
		won, err := api.saveAttempt(ctx, *row, next)
		if err != nil {
			return "", err
		}
		if won {
			return ota.VerdictApplied, nil
		}
	}
	return "", fmt.Errorf("update attempt %q changed too often to apply a report", rep.AttemptID)
}

// applyInventory reconciles a device's current attempt for the reported component, if that attempt
// is UNKNOWN. An inventory is attempt-independent truth: with nothing to reconcile it changes
// nothing and says so.
func (api *Api) applyInventory(ctx context.Context, deviceToken string, rep ota.Report) (ota.Verdict, error) {
	for range casRetries {
		var current []UpdateAssignment
		if err := api.RDB.DB(ctx).Where("device_token = ? AND component = ? AND superseded_at IS NULL",
			deviceToken, rep.Component).Limit(1).Find(&current).Error; err != nil {
			return "", err
		}
		if len(current) == 0 {
			return VerdictUnknownAssignment, nil
		}
		row, err := api.attemptOf(ctx, current[0].ID)
		if err != nil {
			return "", err
		}
		if row == nil {
			return "", fmt.Errorf("update assignment %q has no attempt row", current[0].Token)
		}
		next, verdict := ota.Reconcile(row.ToOTA(current[0].Token), rep, api.now())
		if verdict != ota.VerdictApplied {
			return verdict, nil
		}
		won, err := api.saveAttempt(ctx, *row, next)
		if err != nil {
			return "", err
		}
		if won {
			return ota.VerdictApplied, nil
		}
	}
	return "", errors.New("update attempt changed too often to reconcile an inventory")
}

// CurrentUpdates returns the device's current assignments whose attempt is still live, for a sync
// to answer with. A cancel-requested attempt is included so the device is told again.
func (api *Api) CurrentUpdates(ctx context.Context, deviceToken string) ([]UpdateAssignmentView, error) {
	var current []UpdateAssignment
	if err := api.RDB.DB(ctx).Where("device_token = ? AND superseded_at IS NULL", deviceToken).
		Order("created_at DESC").Limit(MaxComponentsPerSync).Find(&current).Error; err != nil {
		return nil, err
	}
	if len(current) == 0 {
		return nil, nil
	}
	// Asked only now that there is something to answer with: a sync with nothing to say costs no lookup.
	if err := api.refuseIfDisabled(ctx, deviceToken); err != nil {
		return nil, err
	}
	views, err := api.viewsOf(ctx, current)
	if err != nil {
		return nil, err
	}
	live := views[:0]
	for _, v := range views {
		if !ota.State(v.Attempt.State).Terminal() {
			live = append(live, v)
		}
	}
	return live, nil
}

// MaxComponentsPerSync bounds how many components one device is answered for in a single sync.
const MaxComponentsPerSync = 16

// PublishSyncAnswer sends a device what it should be working on: each live assignment, and a
// cancel for any whose cancellation it has not confirmed.
func (api *Api) PublishSyncAnswer(ctx context.Context, deviceToken string, views []UpdateAssignmentView) {
	for i := range views {
		v := &views[i]
		if v.Attempt.CancelRequested {
			api.publishCancel(ctx, deviceToken, v.Attempt.Token, v.Assignment.Token)
			continue
		}
		api.publishAssignment(ctx, v)
	}
}

// Download refusal reasons: a closed vocabulary, used as metric labels and in the consumer's log.
const (
	GrantUnknownAssignment = "unknown_assignment"
	GrantSuperseded        = "superseded"
	GrantFinished          = "finished"
	GrantCancelRequested   = "cancel_requested"
	GrantArtifactGone      = "artifact_unavailable"
	GrantNotConfigured     = "not_configured"
)

// GrantRefusal is the platform declining to mint a download capability. Reason is one of the
// Grant* values; Stale says the device should be told to drop the assignment.
type GrantRefusal struct {
	Reason       string
	AttemptToken string
	Stale        bool
}

func (g *GrantRefusal) Error() string { return "download grant refused: " + g.Reason }

// Grant is a minted capability URL and when it stops working.
type Grant struct {
	URL       string
	ExpiresAt time.Time
}

// GrantDownload mints a capability for a device's CURRENT assignment. Identity cannot change by
// asking again: a refresh names the same artifact and assignment, and the digest it covers is the
// attempt's target digest.
func (api *Api) GrantDownload(ctx context.Context, deviceToken, assignmentToken string) (*Grant, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("a download grant requires a tenant")
	}
	if err := api.refuseIfDisabled(ctx, deviceToken); err != nil {
		return nil, err
	}
	asg, err := api.assignmentByToken(ctx, deviceToken, assignmentToken)
	if err != nil {
		return nil, err
	}
	if asg == nil {
		return nil, &GrantRefusal{Reason: GrantUnknownAssignment, Stale: true}
	}
	row, err := api.attemptOf(ctx, asg.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("update assignment %q has no attempt row", asg.Token)
	}
	switch {
	case asg.SupersededAt.Valid:
		return nil, &GrantRefusal{Reason: GrantSuperseded, AttemptToken: row.Token, Stale: true}
	case ota.State(row.State).Terminal():
		return nil, &GrantRefusal{Reason: GrantFinished, AttemptToken: row.Token, Stale: true}
	case row.CancelRequested:
		return nil, &GrantRefusal{Reason: GrantCancelRequested, AttemptToken: row.Token, Stale: true}
	}
	// Only now: with downloads off, an assignment that is stale or unknown must still be answered
	// with its cancel (Stale), so the device stops asking for something that is gone. Only a
	// CURRENT assignment is refused as not_configured.
	if api.DownloadBaseURL == "" {
		return nil, &GrantRefusal{Reason: GrantNotConfigured}
	}
	artifact, err := api.artifactByID(ctx, asg.ArtifactId)
	if err != nil {
		return nil, err
	}
	if artifact == nil || artifact.State != StateAvailable || !artifact.BlobRef.Valid {
		return nil, &GrantRefusal{Reason: GrantArtifactGone, AttemptToken: row.Token}
	}
	ttl := api.CapabilityTTL
	if ttl <= 0 || ttl > MaxCapabilityTTL {
		ttl = MaxCapabilityTTL
	}
	exp := api.now().Add(ttl).Truncate(time.Second)
	capability, err := api.MintCapability(ctx, Capability{Tenant: tenant, DeviceToken: deviceToken,
		AssignmentID: asg.Token, Digest: row.TargetDigest, ArtifactTok: artifact.Token, Expires: exp})
	if err != nil {
		return nil, err
	}
	return &Grant{URL: DownloadURL(api.DownloadBaseURL, artifact.Token, capability), ExpiresAt: exp}, nil
}

// PublishGrant puts a minted capability on the device's desired channel. Unlike an assignment or a
// cancel, a grant is the device's answer to a request it is waiting on, so a failure to publish is
// returned and the request is retried rather than left for the next sync.
func (api *Api) PublishGrant(ctx context.Context, deviceToken, assignmentToken string, g *Grant) error {
	if api.Desired == nil {
		return errors.New("the device channel is not available, so a download grant cannot be delivered")
	}
	body, err := ota.EncodeDownloadGrant(assignmentToken, g.URL, g.ExpiresAt)
	if err != nil {
		// A grant that cannot be framed would be rejected by the device however often it is
		// retried; the device asks again and gets the same refusal, which is counted.
		return fmt.Errorf("framing a download grant: %w", err)
	}
	return api.Desired.PublishDesired(ctx, deviceToken, body)
}

// DownloadTarget is what a verified capability resolves to once the database agrees with it.
type DownloadTarget struct {
	Artifact Artifact
	Ref      string
}

// Why a download is refused after its capability verified. The handler maps each to a status.
var (
	// ErrDownloadStale: the assignment is gone, superseded, finished or cancelled. 403.
	ErrDownloadStale = errors.New("the assignment this capability was minted for is no longer current")
	// ErrDownloadGone: the artifact was retired or its bytes are not available. 410.
	ErrDownloadGone = errors.New("the artifact is no longer available")
	// ErrDownloadDisabled: an operator has disabled the device the capability was minted for. A
	// capability minted earlier does not outlive the quarantine. 403.
	ErrDownloadDisabled = errors.New("the device this capability was minted for is disabled")
)

// ResolveDownload checks that the database state a verified capability refers to still holds: the
// assignment is the device's current one, its attempt is live and not being cancelled, and the
// artifact in the URL is the assignment's own, AVAILABLE, with the digest the capability covers.
// ctx must carry the capability's tenant.
func (api *Api) ResolveDownload(ctx context.Context, c *Capability) (*DownloadTarget, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, ErrDownloadStale
	}
	asg, err := api.assignmentByToken(ctx, c.DeviceToken, c.AssignmentID)
	if err != nil {
		return nil, err
	}
	if asg == nil || asg.SupersededAt.Valid {
		return nil, ErrDownloadStale
	}
	row, err := api.attemptOf(ctx, asg.ID)
	if err != nil {
		return nil, err
	}
	if row == nil || ota.State(row.State).Terminal() || row.CancelRequested || row.TargetDigest != c.Digest {
		return nil, ErrDownloadStale
	}
	// After the cheap staleness checks, so a stale request costs no lookup. A capability minted
	// before the device was disabled does not outlive the quarantine.
	if err := api.refuseIfDisabled(ctx, c.DeviceToken); err != nil {
		if errors.Is(err, ErrDeviceDisabled) {
			return nil, ErrDownloadDisabled
		}
		return nil, err
	}
	artifact, err := api.artifactByID(ctx, asg.ArtifactId)
	if err != nil {
		return nil, err
	}
	if artifact == nil || artifact.Token != c.ArtifactTok || artifact.Digest != c.Digest {
		return nil, ErrDownloadStale
	}
	if artifact.State != StateAvailable || !artifact.BlobRef.Valid {
		return nil, ErrDownloadGone
	}
	return &DownloadTarget{Artifact: *artifact, Ref: artifact.BlobRef.String}, nil
}

// deadlineGroups pairs each phase's deadline with the states it governs, as the reducer's Tick
// does. The sweep selects with these so that a row not yet past its OWN deadline is not read again
// on every pass.
func deadlineGroups(p ota.Policy) []struct {
	states []string
	after  time.Duration
} {
	return []struct {
		states []string
		after  time.Duration
	}{
		{[]string{string(ota.StateQueued), string(ota.StateInitiated)}, p.Acknowledge},
		{[]string{string(ota.StateDownloading), string(ota.StateDownloaded)}, p.Download},
		{[]string{string(ota.StateVerified)}, p.Verify},
		{[]string{string(ota.StateUpdating)}, p.Install},
		{[]string{string(ota.StateRebooting)}, p.Confirm},
	}
}

// SweepResult counts what one deadline pass did.
type SweepResult struct {
	Examined int
	TimedOut int
	Unknown  int
}

// maxSweepPages bounds how many pages one pass walks looking for work. A pass that finds nothing to
// do on a page moves on to the next instead of stopping, so rows it cannot act on (a deleted
// tenant, an attempt whose write keeps failing) cannot pin the front of the queue; this bounds the
// reads a pass does when there is a great deal of such junk.
const maxSweepPages = 20

// SweepDeadlines moves attempts whose phase deadline has elapsed: TIMED_OUT before installation is
// reported started, UNKNOWN after. It reads pages across every tenant under a system context,
// oldest first, and applies each change under that tenant own context with the same compare-and-set
// the report path uses, so two replicas sweeping at once, or a report landing mid-sweep, resolve
// to one outcome. A device whose attempt timed out is told to stop.
//
// limit is how many attempts a pass may MOVE, and also the page size. Rows it examines but does not
// move (a tenant that has been deleted, an attempt that was lost to a concurrent writer, a write
// that failed) do not count toward it: the walk is by keyset past them, so none of them can fill
// every page and starve the rows behind. Tenants found deleted are excluded from the next page in
// the query itself.
func (api *Api) SweepDeadlines(ctx context.Context, limit int) (SweepResult, error) {
	var res SweepResult
	if err := api.Policy.Validate(); err != nil {
		return res, err
	}
	now := api.now()
	var clauses []string
	var args []any
	for _, g := range deadlineGroups(api.Policy) {
		clauses = append(clauses, "(state IN ? AND last_report_at < ?)")
		args = append(args, g.states, now.Add(-g.after))
	}
	due := "(" + strings.Join(clauses, " OR ") + ")"
	moved := 0
	var skip []string // tenants found deleted during this pass
	var after *UpdateAttempt
	for page := 0; page < maxSweepPages && moved < limit; page++ {
		// 🔴 THE ONLY TENANT-ISOLATION BYPASS IN THIS SERVICE, and it is one SELECT. Which attempts
		// are overdue is a question across every tenant, and no tenant context can ask it. It reads
		// rows (never writes) and returns them; every change made to one is made by tickOne under
		// core.WithTenant(that attempt's own tenant), so the tenant predicate is injected and the
		// fail-closed check is live for each write.
		q := api.RDB.DB(core.WithSystemContext(ctx)).Where(due, args...)
		if after != nil {
			q = q.Where("(last_report_at > ? OR (last_report_at = ? AND id > ?))", after.LastReportAt, after.LastReportAt, after.ID)
		}
		if len(skip) > 0 {
			q = q.Where("tenant_id NOT IN ?", skip)
		}
		var rows []UpdateAttempt
		if err := q.Order("last_report_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return res, err
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			d := rows[i]
			after = &rows[i]
			res.Examined++
			if api.TenantDeleted != nil && api.TenantDeleted(d.TenantId) {
				if len(skip) < 100 && !slices.Contains(skip, d.TenantId) {
					skip = append(skip, d.TenantId)
				}
				continue
			}
			out, err := api.tickOne(core.WithTenant(ctx, d.TenantId), d.ID)
			if err != nil {
				log.Warn().Err(err).Str("attempt", d.Token).Msg("An update attempt deadline could not be applied; it is retried at the next pass.")
				continue
			}
			switch out {
			case ota.StateTimedOut:
				res.TimedOut++
				moved++
			case ota.StateUnknown:
				res.Unknown++
				moved++
			}
		}
		if len(rows) < limit {
			break
		}
	}
	return res, nil
}

// tickOne applies Tick to one attempt under its tenant's context and returns the state it moved to,
// or "" when it did not move.
func (api *Api) tickOne(ctx context.Context, attemptID uint) (ota.State, error) {
	for range casRetries {
		var rows []UpdateAttempt
		if err := rdb.FindByKeys(api.RDB.DB(ctx), &rows, "id", []uint{attemptID}); err != nil {
			return "", err
		}
		if len(rows) == 0 {
			return "", nil
		}
		row := rows[0]
		var asg []UpdateAssignment
		if err := rdb.FindByKeys(api.RDB.DB(ctx), &asg, "id", []uint{row.AssignmentId}); err != nil {
			return "", err
		}
		if len(asg) == 0 {
			return "", fmt.Errorf("update attempt %q has no assignment row", row.Token)
		}
		next, moved, err := ota.Tick(row.ToOTA(asg[0].Token), api.Policy, api.now())
		if err != nil {
			return "", err
		}
		if !moved {
			return "", nil
		}
		won, err := api.saveAttempt(ctx, row, next)
		if err != nil {
			return "", err
		}
		if !won {
			continue
		}
		if next.State == ota.StateTimedOut {
			api.publishCancel(ctx, row.DeviceToken, row.Token, asg[0].Token)
		}
		return next.State, nil
	}
	return "", nil
}
