// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Limits on the X.509 surface. They are code constants: a tenant that needs more is a
// conversation, not a setting.
const (
	// MaxTrustAnchorsPerTenant bounds the CA certificates one tenant trusts.
	MaxTrustAnchorsPerTenant = 32
	// MaxX509ProfilesPerTenant bounds the provisioning profiles that authenticate by certificate.
	MaxX509ProfilesPerTenant = 8
	// MaxX509CredentialsPerDevice bounds the live certificate credentials one device holds, so
	// a renewal can overlap the certificate it replaces without the set growing for ever.
	MaxX509CredentialsPerDevice = 3
)

// invalidAnchor refuses an X.509 request that cannot be honoured (a trust anchor, a certificate
// credential, a certificate profile). INVALID_VALUE.
func invalidAnchor(format string, args ...any) error {
	return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf(format, args...))
}

// kickOriginAnchorBase separates the Origin of a trust-anchor kick from a provisioning
// profile's (a profile id) and from a device reset's (0) in the pending session-kick queue,
// whose key is (tenant, device, origin). Both ids come from sequences that start at 1, so
// adding the anchor's id to a base no profile id will ever reach keeps the three namespaces
// apart; TestKickOriginsDoNotCollide pins it.
const kickOriginAnchorBase uint = 1 << 40

// anchorKickOrigin is the queue Origin of a kick owed because trust anchor id was disabled.
func anchorKickOrigin(id uint) uint { return kickOriginAnchorBase + id }

// lockTrustAnchorByToken reads the anchor with this token on tx, locking its row FOR UPDATE.
// Disable, delete and every write that binds something to an anchor serialize on it. A token
// that names nothing in the tenant is gorm.ErrRecordNotFound.
func lockTrustAnchorByToken(tx *gorm.DB, token string) (*DeviceTrustAnchor, error) {
	var locked DeviceTrustAnchor
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token = ?", token).First(&locked).Error; err != nil {
		return nil, err
	}
	return &locked, nil
}

// lockTrustAnchorShareByToken reads the anchor with this token on tx, locking its row FOR SHARE
// to the commit. A write that BINDS something to an anchor (a credential, a profile) holds it
// while it re-checks that the anchor is still enabled and then inserts, so a disable or a delete
// (FOR UPDATE) either finishes first, and the bind sees it, or waits for the bind and sees what
// it added. An anchor deleted meanwhile is gorm.ErrRecordNotFound.
func lockTrustAnchorShareByToken(tx *gorm.DB, token string) (*DeviceTrustAnchor, error) {
	var locked DeviceTrustAnchor
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("token = ?", token).First(&locked).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
		}
		return nil, err
	}
	return &locked, nil
}

// lockTenantCreates serializes the creates of one tenant rows of a kind for the rest of the
// transaction, so the count a limit is checked against is the count the insert lands on. On
// PostgreSQL it is a transaction-scoped advisory lock keyed on the tenant and the kind; anywhere
// else (the SQLite unit-test database, which has one writer at a time) there is nothing to take.
func lockTenantCreates(tx *gorm.DB, tenant, kind string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", tenant+":"+kind).Error
}

// lockDeviceRow locks a device FOR UPDATE to the commit: the per-device limit on certificate
// credentials is counted and written under it.
func lockDeviceRow(tx *gorm.DB, id uint) error {
	var d Device
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", id).First(&d).Error
}

// countX509Credentials is how many certificate credentials the device holds.
func countX509Credentials(tx *gorm.DB, deviceId uint) (int64, error) {
	var n int64
	err := tx.Model(&DeviceCredential{}).
		Where("device_id = ? AND credential_type = ?", deviceId, string(CredentialX509Certificate)).Count(&n).Error
	return n, err
}

// referenceToNotFound turns the foreign-key refusal of an insert that named a trust anchor
// deleted since it was read into the NOT_FOUND the caller would have had a moment later.
func referenceToNotFound(err error, anchorToken string) error {
	if class, ok := integrity.Refused(err); ok && class == integrity.ClassReference {
		return fmt.Errorf("trust anchor %q: %w", anchorToken, gorm.ErrRecordNotFound)
	}
	return err
}

// trustAnchorByToken reads one anchor of the calling tenant, or gorm.ErrRecordNotFound.
func (api *Api) trustAnchorByToken(ctx context.Context, token string) (*DeviceTrustAnchor, error) {
	found, err := api.DeviceTrustAnchorsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("trust anchor %q: %w", token, gorm.ErrRecordNotFound)
	}
	return found[0], nil
}

// CreateDeviceTrustAnchor stores a CA certificate the tenant trusts. The certificate is parsed
// and checked here (exactly one, a CA that may sign certificates, unexpired, an allowed key, no
// name constraints, no private key anywhere in the text), and the row stores the canonical PEM
// together with the fingerprint, which is unique per tenant: the same CA cannot be registered
// twice.
func (api *Api) CreateDeviceTrustAnchor(ctx context.Context, request *DeviceTrustAnchorCreateRequest) (*DeviceTrustAnchor, error) {
	anchor, err := x509trust.ParseTrustAnchor(request.CertificatePem, time.Now())
	if err != nil {
		return nil, invalidAnchor("certificatePem: %v", err)
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	created := &DeviceTrustAnchor{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity:    rdb.MetadataEntity{Metadata: metadataJSON},
		CertificatePem:    anchor.PEM,
		FingerprintSha256: anchor.Info.FingerprintSHA256,
		Subject:           anchor.Info.Subject,
		Issuer:            anchor.Info.Issuer,
		NotBefore:         anchor.Info.NotBefore,
		NotAfter:          anchor.Info.NotAfter,
		Enabled:           enabled,
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("no tenant in context")
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenantCreates(tx, tenant, "trust_anchors"); err != nil {
			return err
		}
		var n int64
		if err := tx.Model(&DeviceTrustAnchor{}).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxTrustAnchorsPerTenant {
			return limit.Exceeded("trust anchors", int(n)+1, MaxTrustAnchorsPerTenant)
		}
		return tx.Create(created).Error
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// DeviceTrustAnchorsByToken reads trust anchors by token.
func (api *Api) DeviceTrustAnchorsByToken(ctx context.Context, tokens []string) ([]*DeviceTrustAnchor, error) {
	found := make([]*DeviceTrustAnchor, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// DeviceTrustAnchorsById reads trust anchors by id.
func (api *Api) DeviceTrustAnchorsById(ctx context.Context, ids []uint) ([]*DeviceTrustAnchor, error) {
	return rdb.FindByIds[DeviceTrustAnchor](api.RDB.DB(ctx), ids)
}

// DeviceTrustAnchors searches the tenant's trust anchors.
func (api *Api) DeviceTrustAnchors(ctx context.Context, criteria DeviceTrustAnchorSearchCriteria) (*DeviceTrustAnchorSearchResults, error) {
	results := make([]DeviceTrustAnchor, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceTrustAnchor{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", *criteria.Enabled)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &DeviceTrustAnchorSearchResults{Results: results, Pagination: pag}, nil
}

// TrustAnchorCredentialCount is how many credentials (soft-deleted ones included, which is
// what the foreign key counts) rely on the anchor.
func (api *Api) TrustAnchorCredentialCount(ctx context.Context, anchorId uint) (int64, error) {
	var n int64
	err := api.RDB.DB(ctx).Unscoped().Model(&DeviceCredential{}).Where("trust_anchor_id = ?", anchorId).Count(&n).Error
	return n, err
}

// TrustAnchorProfileCount is how many provisioning profiles authenticate against the anchor.
func (api *Api) TrustAnchorProfileCount(ctx context.Context, anchorId uint) (int64, error) {
	var n int64
	err := api.RDB.DB(ctx).Unscoped().Model(&ProvisioningProfile{}).Where("trust_anchor_id = ?", anchorId).Count(&n).Error
	return n, err
}

// UpdateDeviceTrustAnchor applies a PARTIAL update. Turning an enabled anchor OFF withdraws the
// CA: every connect against it is refused from the moment this commits, and the live broker
// sessions of the devices whose credentials rely on it are ended.
//
// The ending is recorded in the SAME transaction as the disable (PendingSessionKick, keyed by
// the anchor), so a kick that could not be confirmed is retried by the background drain and by
// repeating this call, even though a repeat changes nothing. A failure to confirm is
// ErrSessionsNotEnded: the disable is saved and new connects are refused; what is not known is
// whether a session that was already open is still publishing.
//
// LOCK ORDER: the anchor row first, then nothing else is locked (the credentials are only read).
// A credential or profile insert bound to the anchor takes a key-share lock on the anchor row
// through its foreign key, so it waits for a disable in flight and neither can deadlock the other.
func (api *Api) UpdateDeviceTrustAnchor(ctx context.Context, token string, request *DeviceTrustAnchorUpdateRequest) (*DeviceTrustAnchor, error) {
	var (
		updated   *DeviceTrustAnchor
		deviceIds []uint
	)
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		deviceIds = deviceIds[:0]
		locked, err := lockTrustAnchorByToken(tx, token)
		if err != nil {
			return err
		}
		wasEnabled := locked.Enabled
		enabled, err := request.Enabled.ApplyToRequired("enabled", locked.Enabled)
		if err != nil {
			return err
		}
		metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(locked.Metadata)))
		if err != nil {
			return err
		}
		locked.Name = request.Name.ApplyToNullString(locked.Name)
		locked.Description = request.Description.ApplyToNullString(locked.Description)
		locked.Metadata = metadataJSON
		locked.Enabled = enabled
		if err := tx.Save(locked).Error; err != nil {
			return err
		}
		updated = locked
		if !wasEnabled && enabled {
			// Re-enabled: the CA is trusted again, so a kick owed for its earlier withdrawal is
			// not owed any more. It would otherwise end the sessions of devices that have just
			// reconnected under the restored anchor.
			return tx.Where("tenant_id = ? AND origin = ?", locked.TenantId, anchorKickOrigin(locked.ID)).
				Delete(&PendingSessionKick{}).Error
		}
		if !wasEnabled || enabled {
			return nil
		}
		// Withdrawn now: the devices holding an enabled credential that chains to this anchor.
		if err := tx.Model(&DeviceCredential{}).
			Where("trust_anchor_id = ? AND enabled = ?", locked.ID, true).
			Distinct().Pluck("device_id", &deviceIds).Error; err != nil {
			return err
		}
		var tokens []string
		if len(deviceIds) > 0 {
			if err := tx.Unscoped().Model(&Device{}).Where("id IN ?", deviceIds).Pluck("token", &tokens).Error; err != nil {
				return err
			}
		}
		return api.queueSessionKicksOnTx(tx, locked.TenantId, anchorKickOrigin(locked.ID), tokens)
	})
	if err != nil {
		return nil, err
	}
	api.evictDeviceCredentials(ctx, updated.TenantId, deviceIds...)
	if !updated.Enabled {
		// Drained whether or not this call disabled anything: kicks left pending by an earlier
		// disable are retried here, and success means they were all confirmed.
		if kerr := api.drainSessionKicks(ctx, updated.TenantId, 0, func(db *gorm.DB) *gorm.DB {
			return db.Where("origin = ?", anchorKickOrigin(updated.ID))
		}); kerr != nil {
			return updated, kerr
		}
	}
	return updated, nil
}

// DeleteDeviceTrustAnchor deletes an anchor nothing relies on. A credential or a provisioning
// profile that still names it refuses the delete with ErrEntityInUse (IN_USE), counted the way
// the foreign key counts (soft-deleted credentials included); disable the anchor, or remove
// what relies on it, first. The anchor's row lock is held to the commit, and the foreign key is
// the backstop for a binding that slips in between the count and the delete.
func (api *Api) DeleteDeviceTrustAnchor(ctx context.Context, token string) (bool, error) {
	deleted := false
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockTrustAnchorByToken(tx, token)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var creds, profiles int64
		if err := tx.Unscoped().Model(&DeviceCredential{}).Where("trust_anchor_id = ?", locked.ID).Count(&creds).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Model(&ProvisioningProfile{}).Where("trust_anchor_id = ?", locked.ID).Count(&profiles).Error; err != nil {
			return err
		}
		if creds+profiles > 0 {
			return fmt.Errorf("%w: %d credential(s) and %d provisioning profile(s) rely on trust anchor %q; remove them first",
				ErrEntityInUse, creds, profiles, token)
		}
		result := tx.Unscoped().Delete(locked)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected > 0
		return nil
	})
	if err != nil {
		return false, inUseOn(err, fmt.Sprintf("trust anchor %q is still relied on by a credential or a provisioning profile", token))
	}
	return deleted, nil
}

// requireUsableAnchor is the check a write that BINDS something to an anchor makes: the anchor
// exists in the tenant and is enabled (binding to a withdrawn CA would store a credential that
// can never authenticate).
func requireUsableAnchor(anchor *DeviceTrustAnchor) error {
	if !anchor.Enabled {
		return invalidAnchor("trust anchor %q is disabled", anchor.Token)
	}
	return nil
}

// trimmedToken is a token argument with surrounding blanks removed; "" means none.
func trimmedToken(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}
