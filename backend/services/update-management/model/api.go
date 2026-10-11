// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Bounds on the declared metadata. They are deliberately small: an artifact's metadata
// is a handful of short strings, and every bound fails the write rather than truncating.
const (
	maxNameLen         = 128
	maxVersionLen      = 64
	maxComponentLen    = 64
	maxMediaTypeLen    = 127
	maxCompatProfiles  = 64
	MaxProvenanceBytes = 4 << 10
	defaultMediaType   = "application/octet-stream"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Stable refusal codes (extensions.code on the GraphQL plane).
const (
	CodeArtifactTooLarge           = "ARTIFACT_TOO_LARGE"
	CodeArtifactTooLargeForBackend = "ARTIFACT_TOO_LARGE_FOR_BACKEND"
	CodeTenantArtifactQuota        = "TENANT_ARTIFACT_QUOTA_EXCEEDED"
	CodeUnknownCompatibleProfile   = "UNKNOWN_COMPATIBLE_PROFILE"
	CodeProfileValidationUnavail   = "PROFILE_VALIDATION_UNAVAILABLE"
	CodeArtifactNotUploadable      = "ARTIFACT_NOT_UPLOADABLE"
	CodeInvalidArtifact            = "INVALID_ARTIFACT"
	CodeArtifactNotFound           = "ARTIFACT_NOT_FOUND"
	CodeTenantDeleted              = "TENANT_DELETED"
	CodeObjectStoreNotConfigured   = "OBJECT_STORE_NOT_CONFIGURED"
	CodeArtifactUploadRaceLost     = "ARTIFACT_UPLOAD_CONFLICT"
)

// RefusalError is a refusal with a machine-readable code. It is NOT a conflict.Error:
// that type means "a value that must be unique is already in use", and a client may treat
// it as "already exists, carry on".
type RefusalError struct {
	Code string
	Msg  string
}

func (e *RefusalError) Error() string { return e.Msg }

// Extensions types the refusal for the GraphQL layer.
func (e *RefusalError) Extensions() map[string]any { return map[string]any{"code": e.Code} }

func refuse(code, format string, args ...any) error {
	return &RefusalError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ProfileChecker confirms that device-profile tokens exist in a tenant. It reports the
// tokens that do NOT exist; an error means the answer could not be obtained, which the
// caller treats as a refusal, never as "all exist".
type ProfileChecker interface {
	Missing(ctx context.Context, tenant string, tokens []string) ([]string, error)
}

// Limits are the fixed ceilings the catalogue enforces.
type Limits struct {
	// MaxArtifactBytes is the effective per-artifact ceiling (already clamped for the
	// blob backend).
	MaxArtifactBytes int64
	// ConfiguredMaxArtifactBytes is the unclamped, configured ceiling; it is only used to
	// tell "over the ceiling" from "over what this backend can store".
	ConfiguredMaxArtifactBytes int64
	// MaxTenantArtifactBytes caps the summed size of one tenant's artifacts.
	MaxTenantArtifactBytes int64
}

// Api is the artifact catalogue's persistence and verification surface.
type Api struct {
	RDB    *rdb.RdbManager
	Blobs  blob.Store
	Limits Limits
	// Profiles validates compatible-profile tokens against device-management. Nil means
	// it is not wired, and createArtifact is then REFUSED (fail closed): an artifact
	// whose compatibility list was never checked would be assignable to nothing real.
	Profiles ProfileChecker
	// TenantDeleted reports whether a tenant has been through the delete door. When set,
	// writes are refused for such a tenant so a still-valid token cannot re-land rows
	// behind the purge sweep. Nil disables the gate (user-management not configured, unit
	// tests).
	TenantDeleted func(tenant string) bool
}

// NewApi creates the catalogue API.
func NewApi(rdbm *rdb.RdbManager, blobs blob.Store, limits Limits) *Api {
	return &Api{RDB: rdbm, Blobs: blobs, Limits: limits}
}

// RefuseDeletedTenant is the lifecycle gate every write passes through.
func (api *Api) RefuseDeletedTenant(ctx context.Context) error {
	if api.TenantDeleted == nil {
		return nil
	}
	if tenant, ok := core.TenantFromContext(ctx); ok && api.TenantDeleted(tenant) {
		return refuse(CodeTenantDeleted, "this tenant has been deleted; artifacts can no longer be written")
	}
	return nil
}

func validKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// boundedText trims s and checks it is non-empty, within max, and free of control
// characters (an artifact's name is rendered in consoles and device logs).
func boundedText(field, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", refuse(CodeInvalidArtifact, "%s must not be empty", field)
	}
	if len(s) > max {
		return "", refuse(CodeInvalidArtifact, "%s exceeds %d bytes", field, max)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", refuse(CodeInvalidArtifact, "%s must not contain control characters", field)
		}
	}
	return s, nil
}

// NormalizeDigest validates a declared digest and returns it in its stored form.
func NormalizeDigest(d string) (string, error) {
	d = strings.ToLower(strings.TrimSpace(d))
	if !digestPattern.MatchString(d) {
		return "", refuse(CodeInvalidArtifact, "digest must be %q followed by 64 hex digits", DigestPrefix)
	}
	return d, nil
}

// CreateArtifact declares a new artifact in UPLOADING state. No bytes are accepted here;
// the content follows over the upload endpoint.
func (api *Api) CreateArtifact(ctx context.Context, req *ArtifactCreateRequest) (*Artifact, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("artifact create requires a tenant")
	}
	// Declaring an artifact whose content can never be stored would leave it UPLOADING
	// for ever, so a deployment with no object store refuses the declaration itself.
	if api.Blobs == nil {
		return nil, refuse(CodeObjectStoreNotConfigured,
			"the object store is not configured, so artifact content cannot be stored")
	}
	if !validKind(req.Kind) {
		return nil, refuse(CodeInvalidArtifact, "kind must be one of %s", strings.Join(Kinds, ", "))
	}
	name, err := boundedText("name", req.Name, maxNameLen)
	if err != nil {
		return nil, err
	}
	version, err := boundedText("version", req.Version, maxVersionLen)
	if err != nil {
		return nil, err
	}
	component, err := boundedText("component", req.Component, maxComponentLen)
	if err != nil {
		return nil, err
	}
	digest, err := NormalizeDigest(req.Digest)
	if err != nil {
		return nil, err
	}
	mediaType := defaultMediaType
	if req.MediaType != nil {
		if mediaType, err = boundedText("mediaType", *req.MediaType, maxMediaTypeLen); err != nil {
			return nil, err
		}
	}
	if err := api.checkSize(req.SizeBytes); err != nil {
		return nil, err
	}
	profiles, err := normalizeProfiles(req.CompatibleProfiles)
	if err != nil {
		return nil, err
	}
	provenance, err := api.provenance(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := api.checkProfiles(ctx, tenant, profiles); err != nil {
		return nil, err
	}
	profilesJSON, err := json.Marshal(profiles)
	if err != nil {
		return nil, err
	}

	created := &Artifact{
		TokenReference:     rdb.TokenReference{Token: req.Token},
		Kind:               req.Kind,
		Name:               name,
		Version:            version,
		Component:          component,
		SizeBytes:          req.SizeBytes,
		Digest:             digest,
		MediaType:          mediaType,
		CompatibleProfiles: datatypes.JSON(profilesJSON),
		TrustStatus:        TrustUnverified,
		Provenance:         provenance,
		State:              StateUploading,
	}
	// The quota check and the insert share one transaction so the sum read is the one
	// the insert is judged against. Two concurrent creates can still both pass (READ
	// COMMITTED), overshooting the ceiling by at most one artifact each; the ceiling
	// bounds storage growth, not a billing figure, and serialising every create of a
	// tenant to close that window would cost more than the overshoot does.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var held int64
		if err := tx.Model(&Artifact{}).Select("CAST(COALESCE(SUM(size_bytes), 0) AS BIGINT)").Scan(&held).Error; err != nil {
			return err
		}
		if held+req.SizeBytes > api.Limits.MaxTenantArtifactBytes {
			return refuse(CodeTenantArtifactQuota,
				"this tenant's artifacts would hold %d bytes, over the %d-byte ceiling", held+req.SizeBytes, api.Limits.MaxTenantArtifactBytes)
		}
		return tx.Create(created).Error
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (api *Api) checkSize(size int64) error {
	if size <= 0 {
		return refuse(CodeInvalidArtifact, "sizeBytes must be positive")
	}
	if size > api.Limits.ConfiguredMaxArtifactBytes {
		return refuse(CodeArtifactTooLarge, "sizeBytes %d exceeds the %d-byte artifact ceiling", size, api.Limits.ConfiguredMaxArtifactBytes)
	}
	if size > api.Limits.MaxArtifactBytes {
		return refuse(CodeArtifactTooLargeForBackend,
			"sizeBytes %d exceeds the %d bytes this deployment's object store can accept in one upload", size, api.Limits.MaxArtifactBytes)
	}
	return nil
}

func normalizeProfiles(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, refuse(CodeInvalidArtifact, "compatibleProfiles must name at least one device profile")
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if err := core.ValidateToken(p); err != nil {
			return nil, refuse(CodeInvalidArtifact, "compatibleProfiles contains an invalid profile token: %v", err)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > maxCompatProfiles {
		return nil, refuse(CodeInvalidArtifact, "compatibleProfiles names more than %d profiles", maxCompatProfiles)
	}
	return out, nil
}

func (api *Api) checkProfiles(ctx context.Context, tenant string, tokens []string) error {
	if api.Profiles == nil {
		return refuse(CodeProfileValidationUnavail,
			"compatible profiles cannot be validated: the device-management endpoint or service secret is not configured")
	}
	missing, err := api.Profiles.Missing(ctx, tenant, tokens)
	if err != nil {
		return refuse(CodeProfileValidationUnavail, "compatible profiles could not be validated against device-management: %v", err)
	}
	if len(missing) > 0 {
		return refuse(CodeUnknownCompatibleProfile, "unknown device profile(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// provenance builds the {uploadedBy, source, notes} record. uploadedBy is stamped from
// the verified caller, never taken from the request.
func (api *Api) provenance(ctx context.Context, req *ArtifactCreateRequest) (datatypes.JSON, error) {
	rec := map[string]string{}
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		if who := firstNonEmpty(claims.Username, claims.Email); who != "" {
			rec["uploadedBy"] = who
		}
	}
	if req.Source != nil && strings.TrimSpace(*req.Source) != "" {
		rec["source"] = strings.TrimSpace(*req.Source)
	}
	if req.Notes != nil && strings.TrimSpace(*req.Notes) != "" {
		rec["notes"] = strings.TrimSpace(*req.Notes)
	}
	if len(rec) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxProvenanceBytes {
		return nil, refuse(CodeInvalidArtifact, "source and notes exceed %d bytes together", MaxProvenanceBytes)
	}
	return datatypes.JSON(b), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ArtifactByToken returns the tenant's artifact with the given token, or nil when there
// is none. The tenant scope confines the lookup to the caller's tenant.
func (api *Api) ArtifactByToken(ctx context.Context, token string) (*Artifact, error) {
	found := make([]*Artifact, 0, 1)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", []string{token}); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return found[0], nil
}

// Artifacts searches the tenant's artifacts.
func (api *Api) Artifacts(ctx context.Context, criteria ArtifactSearchCriteria) (*ArtifactSearchResults, error) {
	results := make([]Artifact, 0)
	db, pag := api.RDB.ListOf(ctx, &Artifact{}, func(q *gorm.DB) *gorm.DB {
		if criteria.Kind != nil {
			q = q.Where("kind = ?", *criteria.Kind)
		}
		if criteria.State != nil {
			q = q.Where("state = ?", *criteria.State)
		}
		if criteria.Component != nil {
			q = q.Where("component = ?", *criteria.Component)
		}
		return q
	}, criteria.Pagination)
	if err := db.Find(&results).Error; err != nil {
		return nil, err
	}
	return &ArtifactSearchResults{Results: results, Pagination: pag}, nil
}

// RetireArtifact withdraws an artifact from new use. It is idempotent. The bytes stay in
// the object store: a retired artifact may still be referenced by an update already in
// flight, and removing content is the hard-delete path, which is not offered until
// assignments exist to check it against.
func (api *Api) RetireArtifact(ctx context.Context, token string) (*Artifact, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	a, err := api.ArtifactByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, refuse(CodeArtifactNotFound, "no artifact with token %q", token)
	}
	if a.State == StateRetired {
		return a, nil
	}
	if err := api.RDB.DB(ctx).Model(&Artifact{}).Where("id = ? AND state <> ?", a.ID, StateRetired).
		Update("state", StateRetired).Error; err != nil {
		return nil, err
	}
	return api.ArtifactByToken(ctx, token)
}

// ErrNotUploadable is returned for an upload into an artifact that is not UPLOADING.
func ErrNotUploadable(a *Artifact) error {
	return refuse(CodeArtifactNotUploadable,
		"artifact %q is %s; content can only be uploaded while it is %s", a.Token, a.State, StateUploading)
}

// ErrNotFound is the refusal for a token that names no artifact in the caller's tenant.
func ErrNotFound(token string) error {
	return refuse(CodeArtifactNotFound, "no artifact with token %q", token)
}

// CompleteUpload records a verified upload: UPLOADING to AVAILABLE with the stored
// object's reference. The transition is a conditional update, so of two concurrent
// uploads at most one wins and an artifact never changes after it is AVAILABLE; the
// loser gets ARTIFACT_UPLOAD_CONFLICT and must delete its own object.
func (api *Api) CompleteUpload(ctx context.Context, a *Artifact, ref blob.Ref) error {
	res := api.RDB.DB(ctx).Model(&Artifact{}).
		Where("id = ? AND state = ?", a.ID, StateUploading).
		Updates(map[string]any{
			"state":    StateAvailable,
			"blob_ref": sql.NullString{String: ref.String(), Valid: true},
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return refuse(CodeArtifactUploadRaceLost, "artifact %q is no longer accepting content", a.Token)
	}
	return nil
}
