// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
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
	CodeUploadInProgress           = "ARTIFACT_UPLOAD_IN_PROGRESS"
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
	// Undelivered counts assignments the platform declined to send because it could not stand
	// behind their trust (a pin it cannot read, or an unsigned assignment while the instance
	// refuses unverified artifacts). Each one is also logged.
	Undelivered atomic.Int64

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

	// The update plane (assignments, the device channels and the download capability). All of it
	// is optional for the catalogue and REQUIRED for an assignment: each operation that needs a
	// collaborator that is not wired is refused by name rather than half done.
	//
	// Devices resolves a device's profile in device-management; Desired puts messages on a
	// device's desired-state channel; Secrets holds each tenant's capability key.
	Devices DeviceLookup
	Desired DesiredPublisher
	Secrets secrets.SecretStore
	// Policy is how long the platform waits in each phase of an attempt.
	Policy ota.Policy
	// AllowUnverified is the FIXED config updates.allowUnverifiedArtifacts. False means only an
	// artifact with a signature by an ACTIVE signer (derived trust SIGNED) can be assigned.
	AllowUnverified bool
	// DownloadBaseURL is the public base a device reaches the download endpoint at, with no
	// trailing path. Empty means no capability URL can be minted, and a download request is
	// refused loudly.
	DownloadBaseURL string
	// CapabilityTTL is how long a minted capability lives (at most MaxCapabilityTTL).
	CapabilityTTL time.Duration
	// Clock is the time source; nil means time.Now. A seam for deadline and expiry tests.
	Clock func() time.Time

	keys capabilityKeys

	// disabled caches the device-disabled answer behind the device-facing operations (see DisabledCacheTTL).
	disabled disabledCache

	// beforeSave runs between an attempt being read and its compare-and-set write. A seam for the
	// one thing a unit test cannot otherwise reach: a second writer landing in that window.
	beforeSave func(row UpdateAttempt)

	// lockTenant takes the per-tenant admission lock inside the create transaction. A seam
	// so a test can see that it is taken before the sum is read; nil means the real lock.
	lockTenant func(ctx context.Context, tx *gorm.DB) error

	// Groups resolves an entity group to its devices in device-management, for a rollout's start.
	// Nil means it is not wired, and starting a rollout is REFUSED.
	Groups GroupResolver
	// OnRolloutStep observes every rollout step (the driver's and the inline one after a start or
	// resume): the service records its metrics through it. Nil observes nothing.
	OnRolloutStep func(res RolloutStepResult, err error)
	// OnRolloutListFailed observes a failed query for the RUNNING rollouts, which stops every
	// rollout's step before one is attempted. Nil observes nothing.
	OnRolloutListFailed func(err error)

	// beforeDispatch runs inside a rollout step after the in-flight count is taken under the
	// rollout's lock and before any device is dispatched. A seam for the concurrency tests: two
	// replicas held here at once is exactly what the lock exists to prevent.
	beforeDispatch func()
	// noRolloutLock drops the rollout row lock. Set ONLY by the negative control of the
	// two-replica test, to show that the lock is what keeps the cap.
	noRolloutLock bool
	// rolloutPassAfter is where this replica's next driver pass resumes its walk of the RUNNING
	// rollouts (the last id the previous pass reached; 0 starts over), so a pass bounded to
	// maxRolloutsPerPass rollouts still reaches every one of them in turn.
	rolloutPassAfter atomic.Uint64
	// rolloutsPerPass overrides maxRolloutsPerPass when positive. A seam for the fairness test.
	rolloutsPerPass int
}

// The held-bytes predicate in CreateArtifact selects the artifacts that count against a tenant's ceiling: those
// that hold, or are about to hold, bytes. A retired artifact that never received content
// holds none, so retiring an abandoned declaration gives its quota back; a retired
// artifact whose bytes are stored still counts, because they still are.

// lockTenantArtifacts takes pg_advisory_xact_lock keyed on the tenant, as the other
// services do for their per-tenant admission checks.
//
// IT IS A NO-OP ON SQLITE, which the unit tests run on, because the function does not
// exist there. The unit tests therefore prove the lock is requested before the sum is
// read (through the lockTenant seam) and that the statement is the right one for
// PostgreSQL; the overshoot it prevents is only observable against a real server, and
// lock_integration_test.go (build tag integration) observes it there.
func lockTenantArtifacts(ctx context.Context, tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		// A lock keyed on an empty string would serialize every tenant against every other.
		return core.ErrNoTenant
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", artifactLockKey(tenant)).Error
}

// artifactLockKey is the advisory-lock key for a tenant's artifact admissions.
func artifactLockKey(tenant string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("update-management:artifact-quota:" + tenant))
	return int64(h.Sum64())
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
			"the object store is not configured, so artifact content cannot be stored; configure one at install time "+
				"(dcctl bootstrap --object-store filesystem|s3, or the chart's instance.config.infrastructure.blob.backend)")
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
	// The quota check and the insert share one transaction, and the transaction first takes
	// the tenant's advisory lock. Without the lock two creates both read the same sum under
	// READ COMMITTED and both insert, each one passing a ceiling the pair exceeds; with it
	// the second waits for the first to commit and then reads a sum that includes it. The
	// lock is held to the end of the transaction and is per tenant, so tenants do not queue
	// behind each other.
	lock := api.lockTenant
	if lock == nil {
		lock = lockTenantArtifacts
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lock(ctx, tx); err != nil {
			return err
		}
		var held int64
		if err := tx.Model(&Artifact{}).Where("state <> 'RETIRED' OR blob_ref IS NOT NULL").
			Select("CAST(COALESCE(SUM(size_bytes), 0) AS BIGINT)").Scan(&held).Error; err != nil {
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
	// A new artifact has no signatures, so there is nothing to derive.
	created.Trust = TrustUnverified
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
// is none, with its DERIVED trust filled in. The tenant scope confines the lookup to the
// caller's tenant.
func (api *Api) ArtifactByToken(ctx context.Context, token string) (*Artifact, error) {
	a, err := api.ArtifactRowByToken(ctx, token)
	if err != nil || a == nil {
		return nil, err
	}
	if err := api.fillTrust(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// ArtifactRowByToken is ArtifactByToken WITHOUT the derived trust (Trust is left empty), for a
// caller that only needs the row. The upload path is one: it streams a large body, and a lookup
// that also touches the signer tables costs it work that has nothing to do with the bytes.
func (api *Api) ArtifactRowByToken(ctx context.Context, token string) (*Artifact, error) {
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
	var signed *gorm.DB
	if criteria.TrustStatus != nil {
		if *criteria.TrustStatus != TrustSigned && *criteria.TrustStatus != TrustUnverified {
			return nil, refuse(CodeInvalidTrustFilter, "trustStatus must be %s or %s", TrustSigned, TrustUnverified)
		}
		var err error
		if signed, err = api.signedArtifactIDs(ctx); err != nil {
			return nil, err
		}
	}
	db, pag := api.RDB.ListOf(ctx, &Artifact{}, func(q *gorm.DB) *gorm.DB {
		if criteria.TrustStatus != nil {
			if *criteria.TrustStatus == TrustSigned {
				q = q.Where("artifacts.id IN (?)", signed)
			} else {
				q = q.Where("artifacts.id NOT IN (?)", signed)
			}
		}
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
	ptrs := make([]*Artifact, len(results))
	for i := range results {
		ptrs[i] = &results[i]
	}
	if err := api.fillTrust(ctx, ptrs...); err != nil {
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
	if err := api.RDB.DB(ctx).Model(identified(a)).Where("id = ? AND state <> ?", a.ID, StateRetired).
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
	res := api.RDB.DB(ctx).Model(identified(a)).
		Where("id = ? AND state = ?", a.ID, StateUploading).
		Updates(map[string]any{
			"state":           StateAvailable,
			"blob_ref":        sql.NullString{String: ref.String(), Valid: true},
			"uploading_since": nil,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return refuse(CodeArtifactUploadRaceLost, "artifact %q is no longer accepting content", a.Token)
	}
	return nil
}

// UploadWindow is the longest an upload of size bytes is allowed to run: a fixed allowance
// plus the declared size at the slowest rate the handler tolerates. The handler's
// whole-upload deadline and the claim's expiry are both derived from it, so they cannot
// disagree.
func UploadWindow(size int64) time.Duration {
	const minThroughput = 256 << 10 // bytes per second
	return 2*time.Minute + time.Duration(size/minThroughput)*time.Second
}

// claimGrace is how long past its window a claim is honoured, so a replica whose clock
// runs behind does not see a live upload as expired.
const claimGrace = time.Minute

// ClaimUpload takes the artifact's single upload lease. Of any number of concurrent
// callers, across every replica, at most one succeeds; the others get
// ARTIFACT_UPLOAD_IN_PROGRESS and must not read their body. A lease older than the upload
// window belongs to a process that died, and is taken over.
func (api *Api) ClaimUpload(ctx context.Context, a *Artifact) error {
	now := time.Now().UTC()
	stale := now.Add(-(UploadWindow(a.SizeBytes) + claimGrace))
	res := api.RDB.DB(ctx).Model(identified(a)).
		Where("id = ? AND state = ? AND (uploading_since IS NULL OR uploading_since < ?)", a.ID, StateUploading, stale).
		Update("uploading_since", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return refuse(CodeUploadInProgress, "artifact %q already has an upload in progress", a.Token)
	}
	return nil
}

// ReleaseUpload gives the lease back. It is best effort by design: an expired lease is
// taken over anyway, and the only cost of a failed release is that a retry waits for it.
func (api *Api) ReleaseUpload(ctx context.Context, a *Artifact) error {
	return api.RDB.DB(ctx).Model(identified(a)).Where("id = ?", a.ID).Update("uploading_since", nil).Error
}

// identified is the zero-value model carrying only the identity of a loaded artifact, which
// is what lets the audit journal name the row (its primary key and its token as the label)
// without a second query. A conditional update that names its row by id must hand gorm this
// rather than a bare Artifact{}, or the journal records that something changed without
// recording what.
func identified(a *Artifact) *Artifact {
	return &Artifact{Model: gorm.Model{ID: a.ID}, TokenReference: a.TokenReference}
}
