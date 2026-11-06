// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// LIVE SESSIONS OF A REVOKED CERTIFICATE. A revocation refuses the certificate from the next
// connect on every replica (the connect read is uncached), but a session already open holds its
// JWT for hours and the broker does not look at the certificate again. So every write that ADDS
// revocation entries ends the sessions of the devices it affects, through the durable
// PendingSessionKick queue: the debt is written in the same transaction as the entries, so a
// revocation that committed always has its kick on record.
//
// The seam a revocation writer uses is three calls, in this order:
//
//  1. before its transaction: reconcileLegacyChainLinksBounded (links for pre-release credentials);
//  2. inside it, with the anchor locked: queueRevocationKicksOnTx with what it newly revoked;
//  3. after it commits: endRevokedSessions, whose error is ErrSessionsNotEnded when a kick could
//     not be confirmed (the revocation stands).
//
// A manual revoke makes the three calls (RevokeDeviceCertificate); a CRL upload makes them with the
// serials its list names that the issuer's previous list did not (see NewlyRevokedSerials).

// kickOriginRevocationBase separates the Origin of a certificate-revocation kick from a
// provisioning profile's id, a device reset's 0, and a trust-anchor disable's kickOriginAnchorBase
// plus id. Adding the anchor's id to a base no other namespace reaches keeps the debts apart;
// TestKickOriginsDoNotCollide pins it.
const kickOriginRevocationBase uint = 1 << 41

// revocationKickOrigin is the queue Origin of the kicks owed because certificates under trust
// anchor id were revoked.
func revocationKickOrigin(anchorId uint) uint { return kickOriginRevocationBase + anchorId }

// isRevocationKickOrigin is whether o names a certificate-revocation debt.
func isRevocationKickOrigin(o uint) bool {
	return o >= kickOriginRevocationBase && o < kickOriginDeviceDisabled
}

// DefaultRevocationResweepAfter is how long after its first sweep a revocation's kick is repeated.
// It must outlast the admission that was in flight when the revocation committed: the callout's
// lookup timeout plus the broker's own authorization timeout.
const DefaultRevocationResweepAfter = 10 * time.Second

const (
	// MaxRevocationResweepAfter is the longest re-sweep delay accepted.
	MaxRevocationResweepAfter = 5 * time.Minute
	// brokerAuthorizationTimeout is the NATS server's authorization timeout the callout answers
	// inside (authorization.timeout in the broker configuration).
	brokerAuthorizationTimeout = 5 * time.Second
)

// ValidateRevocationResweepAfter checks a re-sweep delay against the callout's lookup timeout, at
// startup. A delay that does not exceed lookupTimeout + the broker's authorization timeout could
// run the re-sweep while the admission it exists to catch is still in flight, which is no re-sweep.
func ValidateRevocationResweepAfter(d, lookupTimeout time.Duration) error {
	if floor := lookupTimeout + brokerAuthorizationTimeout; d <= floor {
		return fmt.Errorf("the revocation re-sweep delay %s must exceed the callout lookup timeout plus the broker authorization timeout (%s)", d, floor)
	}
	if d > MaxRevocationResweepAfter {
		return fmt.Errorf("the revocation re-sweep delay %s must be at most %s", d, MaxRevocationResweepAfter)
	}
	return nil
}

// revocationResweepAfter is the delay in force: the Api's, or the default when it sets none.
func (api *Api) revocationResweepAfter() time.Duration {
	if api.RevocationResweepAfter > 0 {
		return api.RevocationResweepAfter
	}
	return DefaultRevocationResweepAfter
}

// revocationInlineKickRows bounds the pending rows the writer's own call asks the broker about. A
// fleet-sized kick (every device of a CA) is thousands of requests; the call ends the first of
// them and reports ErrSessionsNotEnded for the rest, which the background drain carries on.
// A variable so a test can shrink it.
var revocationInlineKickRows = 5_000

// RevocationDelta is what a revocation write newly revoked, in the two vocabularies of the store.
type RevocationDelta struct {
	// Fingerprints of certificates revoked by DeviceChain's own word (manual entries).
	Fingerprints []string
	// Serials of certificates a CRL names that the issuer's previous list did not.
	Serials []x509trust.IssuerSerial
}

func (d RevocationDelta) empty() bool { return len(d.Fingerprints) == 0 && len(d.Serials) == 0 }

// NewlyRevokedSerials is the diff a CRL upload owes the kick: the serials of next that previous
// does not hold. A serial present in both (its reason may have changed) is not a new revocation.
func NewlyRevokedSerials(issuer x509trust.IssuerID, previous, next []string) []x509trust.IssuerSerial {
	held := make(map[string]struct{}, len(previous))
	for _, s := range previous {
		held[s] = struct{}{}
	}
	var out []x509trust.IssuerSerial
	for _, s := range next {
		if _, ok := held[s]; !ok {
			out = append(out, x509trust.IssuerSerial{Issuer: issuer, Serial: s})
		}
	}
	return out
}

// revocationKickTargets names the devices whose live sessions a delta ends, for one anchor, read
// in tx (the writer's transaction, the anchor locked).
//
// 🔴 TARGETING IS CONSERVATIVE, because the links are a hint (see api_x509_chain_links.go):
//   - a LEAF entry ends the sessions of the devices whose credential's depth-0 link matches: the
//     fingerprint, or (issuer name, serial) with the issuer key equal or absent (a legacy link). A
//     credential whose leaf could not be linked is MARKED with its pin as the fingerprint, so a
//     manual revoke finds it by the same match. A DISABLED
//     credential is matched too: its own disable commits before its kick, and a kick that could
//     not be confirmed there is not durably owed, so this revocation is the next chance to end
//     that session (one extra request when there is none);
//   - an entry that matches a link at depth >= 1 is a CA-level revocation: EVERY device with an
//     enabled certificate credential under the anchor is ended, since a session's path is not the
//     path its credential was created through;
//   - linksWhole false (the reconciler stopped or failed) is not trusted either: the kick covers
//     the whole anchor.
//
// An entry that matches nothing (a certificate not registered, an intermediate never seen) ends
// nothing: the connect path refuses it. Every query names the tenant and the anchor.
func revocationKickTargets(tx *gorm.DB, tenant string, anchorId uint, d RevocationDelta, linksWhole bool) (tokens []string, anchorWide bool, err error) {
	if d.empty() {
		return nil, false, nil
	}
	if !linksWhole {
		tokens, err = anchorDeviceTokens(tx, tenant, anchorId)
		return tokens, true, err
	}
	credentials := map[uint]struct{}{}
	links := func() *gorm.DB {
		return tx.Session(&gorm.Session{NewDB: true}).Model(&DeviceCredentialChainLink{}).
			Where("tenant_id = ? AND trust_anchor_id = ?", tenant, anchorId)
	}
	note := func(rows []DeviceCredentialChainLink) {
		for _, l := range rows {
			if l.Depth >= 1 {
				anchorWide = true
			} else {
				credentials[l.CredentialId] = struct{}{}
			}
		}
	}
	for chunk := range slices.Chunk(sortedUniqueTokens(d.Fingerprints), rdb.MaxLookupKeys) {
		var rows []DeviceCredentialChainLink
		if err := links().Where("fingerprint IN ?", chunk).Find(&rows).Error; err != nil {
			return nil, false, err
		}
		note(rows)
	}
	// CRL entries: the index is (issuer name, serial); the issuer key, when the link has one, is
	// compared here. A link without a key (legacy) matches on name and serial alone.
	type nameSerial struct{ name, serial string }
	keysOf := map[nameSerial][]string{}
	pairs := make([][]any, 0, len(d.Serials))
	for _, s := range d.Serials {
		ns := nameSerial{s.Issuer.Name, s.Serial}
		if _, seen := keysOf[ns]; !seen {
			pairs = append(pairs, []any{s.Issuer.Name, s.Serial})
		}
		keysOf[ns] = append(keysOf[ns], s.Issuer.Key)
	}
	for chunk := range slices.Chunk(pairs, rdb.MaxLookupKeys) {
		var rows []DeviceCredentialChainLink
		if err := links().Where("(issuer_name, serial) IN ?", chunk).Find(&rows).Error; err != nil {
			return nil, false, err
		}
		matched := rows[:0]
		for _, l := range rows {
			if l.IssuerKey == nil || slices.Contains(keysOf[nameSerial{l.IssuerName, l.Serial}], *l.IssuerKey) {
				matched = append(matched, l)
			}
		}
		note(matched)
	}
	if anchorWide {
		tokens, err = anchorDeviceTokens(tx, tenant, anchorId)
		return tokens, true, err
	}
	ids := make([]uint, 0, len(credentials))
	for id := range credentials {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for chunk := range slices.Chunk(ids, rdb.MaxLookupKeys) {
		var deviceIds []uint
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&DeviceCredential{}).
			Where("tenant_id = ? AND trust_anchor_id = ? AND credential_type = ? AND id IN ?",
				tenant, anchorId, string(CredentialX509Certificate), chunk).
			Distinct().Pluck("device_id", &deviceIds).Error; err != nil {
			return nil, false, err
		}
		for dchunk := range slices.Chunk(deviceIds, rdb.MaxLookupKeys) {
			var got []string
			if err := tx.Session(&gorm.Session{NewDB: true}).Unscoped().Model(&Device{}).
				Where("tenant_id = ? AND id IN ?", tenant, dchunk).Pluck("token", &got).Error; err != nil {
				return nil, false, err
			}
			tokens = append(tokens, got...)
		}
	}
	return sortedUniqueTokens(tokens), false, nil
}

// anchorDeviceTokens is the token of every device with an enabled certificate credential under the
// anchor: one statement, the credentials as a subquery, so a fleet-sized answer needs no
// parameter list.
func anchorDeviceTokens(tx *gorm.DB, tenant string, anchorId uint) ([]string, error) {
	sub := tx.Session(&gorm.Session{NewDB: true}).Model(&DeviceCredential{}).Select("device_id").
		Where("tenant_id = ? AND trust_anchor_id = ? AND credential_type = ? AND enabled = ?",
			tenant, anchorId, string(CredentialX509Certificate), true)
	var tokens []string
	if err := tx.Session(&gorm.Session{NewDB: true}).Unscoped().Model(&Device{}).
		Where("tenant_id = ? AND id IN (?)", tenant, sub).Pluck("token", &tokens).Error; err != nil {
		return nil, err
	}
	return sortedUniqueTokens(tokens), nil
}

// prepareRevocationKicks is step 1 of the seam: it links the legacy (pre-release) certificate
// credentials of the anchor so the targeting can find them, and reports whether the anchor's
// links are now whole. With no kicker wired nothing is read (there are no sessions of ours).
//
// 🔴 BOUNDED AND ANCHOR-SCOPED: it links at most legacyReconcileBudget credentials of THIS anchor,
// so a revocation never pays O(credentials of the tenant). "Not whole" (the budget ran out, or the
// reconciler failed) is the targeting's cue to cover the whole anchor rather than trust links that
// may be missing: an over-kick, never a missed one. A failure never stops the revocation, which is
// enforced at connect whatever the links say.
func (api *Api) prepareRevocationKicks(ctx context.Context, tenant, anchorToken string) (linksWhole bool) {
	if api.SessionKicker == nil {
		return false
	}
	anchor, err := api.trustAnchorByToken(ctx, anchorToken)
	if err != nil {
		return false
	}
	return api.reconcileLegacyChainLinksBounded(ctx, tenant, anchor.ID)
}

// queueRevocationKicksOnTx records, in tx (the revocation's transaction, the anchor locked), the
// kicks the delta owes, and returns how many devices it queued. With no kicker wired there are no
// sessions of ours to end and nothing is read or written.
func (api *Api) queueRevocationKicksOnTx(tx *gorm.DB, tenant string, anchorId uint, d RevocationDelta, linksWhole bool) (int, error) {
	if api.SessionKicker == nil || d.empty() {
		return 0, nil
	}
	tokens, _, err := revocationKickTargets(tx, tenant, anchorId, d, linksWhole)
	if err != nil {
		return 0, fmt.Errorf("finding the devices whose sessions a revocation ends: %w", err)
	}
	if err := api.queueSessionKicksOnTx(tx, tenant, revocationKickOrigin(anchorId), tokens); err != nil {
		return 0, err
	}
	return len(tokens), nil
}

// endRevokedSessions asks the broker to end the sessions the anchor's revocations owe, after the
// revocation has committed, and returns nil only when every one was confirmed. It runs whether or
// not this call revoked anything: kicks left pending by an earlier call are retried by repeating
// it. An unconfirmed kick is ErrSessionsNotEnded (the revocation is saved and new connects are
// refused), and so is a debt too large to settle inline, which the background drain carries on.
func (api *Api) endRevokedSessions(ctx context.Context, tenant string, anchorId uint) error {
	if api.SessionKicker == nil {
		return nil
	}
	origin := revocationKickOrigin(anchorId)
	started := time.Now().UTC()
	if err := api.drainSessionKicks(ctx, tenant, revocationInlineKickRows, func(db *gorm.DB) *gorm.DB {
		return db.Where("origin = ?", origin)
	}); err != nil {
		return err
	}
	// What is still owed and was due when this call began: a first sweep beyond the inline limit,
	// or a re-sweep that came due before this call and was not reached. (A row this call settled
	// waits for a re-sweep that is not due yet, and is not owed.)
	var left []PendingSessionKick
	if err := api.RDB.DB(core.WithTenant(ctx, tenant)).
		Where("tenant_id = ? AND origin = ? AND (not_before IS NULL OR not_before <= ?)", tenant, origin, started).
		Limit(1).Find(&left).Error; err != nil {
		return fmt.Errorf("%w: could not re-read the pending session kicks: %v", ErrSessionsNotEnded, err)
	}
	if len(left) > 0 {
		return fmt.Errorf("%w: more devices are owed a kick than one call settles; the background retry continues", ErrSessionsNotEnded)
	}
	return nil
}

// SessionsEndedOf is the verdict a caller reports for the error of a revocation write: true when
// every kick was confirmed, false when the revocation stands but ErrSessionsNotEnded says the
// sessions were not confirmed ended. Any other error is returned as other, and is the caller's to
// fail with.
func SessionsEndedOf(err error) (ended bool, other error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrSessionsNotEnded) {
		return false, nil
	}
	return false, err
}
