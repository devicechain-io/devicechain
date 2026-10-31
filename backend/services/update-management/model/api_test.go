// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// fakeProfiles knows a fixed set of profile tokens per tenant.
type fakeProfiles struct {
	known map[string][]string
	err   error
	calls int
}

func (f *fakeProfiles) Missing(_ context.Context, tenant string, tokens []string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	have := map[string]bool{}
	for _, t := range f.known[tenant] {
		have[t] = true
	}
	var missing []string
	for _, t := range tokens {
		if !have[t] {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

type testEnv struct {
	api      *Api
	profiles *fakeProfiles
	store    blob.Store
}

// newTestEnv builds an Api over in-memory SQLite migrated by the REAL migration chain
// (so the live model is checked against the snapshot the migration creates, and the
// unique indexes exist), tenant scoping registered, and a filesystem object store.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil)
	// The chain runs before the tenant callbacks exist, as core/rdb runs it in the system
	// context: gorm's AutoMigrate reads an existing table through a statement the tenant
	// scope cannot classify.
	for _, m := range schema.Migrations {
		require.NoError(t, m.Migrate(db))
	}
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))

	store, err := blob.New(context.Background(),
		blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, "inst")
	require.NoError(t, err)
	profiles := &fakeProfiles{known: map[string][]string{
		"acme":   {"gateway", "sensor"},
		"globex": {"gateway"},
	}}
	api := NewApi(&rdb.RdbManager{Database: db}, store, Limits{
		MaxArtifactBytes: 1 << 20, ConfiguredMaxArtifactBytes: 1 << 20, MaxTenantArtifactBytes: 3 << 20,
	})
	api.Profiles = profiles
	return &testEnv{api: api, profiles: profiles, store: store}
}

func tenantCtx(tenant string) context.Context {
	ctx := core.WithTenant(context.Background(), tenant)
	return auth.WithClaims(ctx, &auth.Claims{Tenant: tenant, Username: "alice"})
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

func validRequest(token string, content []byte) *ArtifactCreateRequest {
	return &ArtifactCreateRequest{
		Token: token, Kind: KindFirmware, Name: "gateway-fw", Version: "1.0.0", Component: "firmware",
		SizeBytes: int64(len(content)), Digest: digestOf(content), CompatibleProfiles: []string{"gateway"},
	}
}

func requireRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var re *RefusalError
	require.True(t, errors.As(err, &re), "want a %s refusal, got %v", code, err)
	require.Equal(t, code, re.Code, "refusal message: %s", re.Msg)
}

func TestCreateDeclaresAnUploadingUnverifiedArtifact(t *testing.T) {
	env := newTestEnv(t)
	content := []byte("firmware bytes")
	req := validRequest("fw-1", content)
	src, notes := "ci build 42", "release candidate"
	req.Source, req.Notes = &src, &notes
	a, err := env.api.CreateArtifact(tenantCtx("acme"), req)
	require.NoError(t, err)

	require.Equal(t, StateUploading, a.State)
	require.Equal(t, TrustUnverified, a.TrustStatus)
	require.Equal(t, TrustUnverified, a.Trust, "derived trust of a new artifact")
	require.False(t, a.BlobRef.Valid, "no content is referenced until a verified upload")
	require.Equal(t, "application/octet-stream", a.MediaType)
	require.JSONEq(t, `["gateway"]`, string(a.CompatibleProfiles))
	// uploadedBy is stamped from the verified caller; source and notes are the caller's.
	require.JSONEq(t, `{"uploadedBy":"alice","source":"ci build 42","notes":"release candidate"}`, string(a.Provenance))

	got, err := env.api.ArtifactByToken(tenantCtx("acme"), "fw-1")
	require.NoError(t, err)
	require.Equal(t, digestOf(content), got.Digest, "the digest round-trips byte for byte")
}

// The digest is compared as bytes, so it must be stored and returned exactly as normalized:
// a declared upper-case digest is lower-cased once, at the write.
func TestDigestIsNormalizedToLowerCaseAndValidated(t *testing.T) {
	env := newTestEnv(t)
	req := validRequest("fw-1", []byte("x"))
	req.Digest = "SHA256:" + upper(req.Digest[len(DigestPrefix):])
	a, err := env.api.CreateArtifact(tenantCtx("acme"), req)
	require.NoError(t, err)
	require.Equal(t, digestOf([]byte("x")), a.Digest)

	for _, bad := range []string{"", "sha256:abc", "md5:" + digestOf([]byte("x"))[7:], digestOf([]byte("x")) + "00", "sha256:" + string(make([]byte, 64))} {
		r := validRequest("fw-bad", []byte("x"))
		r.Digest = bad
		_, err := env.api.CreateArtifact(tenantCtx("acme"), r)
		requireRefusal(t, err, CodeInvalidArtifact)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestCreateRefusesInvalidDeclarations(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	for name, mut := range map[string]func(*ArtifactCreateRequest){
		"unknown kind":        func(r *ArtifactCreateRequest) { r.Kind = "BOOTLOADER" },
		"empty name":          func(r *ArtifactCreateRequest) { r.Name = "  " },
		"empty version":       func(r *ArtifactCreateRequest) { r.Version = "" },
		"empty component":     func(r *ArtifactCreateRequest) { r.Component = "" },
		"control char":        func(r *ArtifactCreateRequest) { r.Name = "a\x00b" },
		"overlong name":       func(r *ArtifactCreateRequest) { r.Name = string(make([]byte, 129)) },
		"zero size":           func(r *ArtifactCreateRequest) { r.SizeBytes = 0 },
		"negative size":       func(r *ArtifactCreateRequest) { r.SizeBytes = -5 },
		"no profiles":         func(r *ArtifactCreateRequest) { r.CompatibleProfiles = nil },
		"bad profile token":   func(r *ArtifactCreateRequest) { r.CompatibleProfiles = []string{"not a token!"} },
		"too many profiles":   func(r *ArtifactCreateRequest) { r.CompatibleProfiles = manyTokens(65) },
		"oversize provenance": func(r *ArtifactCreateRequest) { s := string(make([]byte, MaxProvenanceBytes+1)); r.Notes = &s },
	} {
		t.Run(name, func(t *testing.T) {
			r := validRequest("fw-x", []byte("data"))
			mut(r)
			_, err := env.api.CreateArtifact(ctx, r)
			require.Error(t, err)
			var re *RefusalError
			require.True(t, errors.As(err, &re), "got %v", err)
		})
	}
}

func manyTokens(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	return out
}

func TestCreateRefusesUnknownCompatibleProfileAndNamesIt(t *testing.T) {
	env := newTestEnv(t)
	r := validRequest("fw-1", []byte("data"))
	r.CompatibleProfiles = []string{"gateway", "ghost"}
	_, err := env.api.CreateArtifact(tenantCtx("acme"), r)
	requireRefusal(t, err, CodeUnknownCompatibleProfile)
	require.Contains(t, err.Error(), "ghost")
	require.NotContains(t, err.Error(), "gateway", "only the unknown profile is named")
}

// The profile list is validated against device-management in the CALLER's tenant: a
// profile that exists only in another tenant is unknown.
func TestProfileValidationIsInTheCallersTenant(t *testing.T) {
	env := newTestEnv(t)
	r := validRequest("fw-1", []byte("data"))
	r.CompatibleProfiles = []string{"sensor"} // acme has it, globex does not
	_, err := env.api.CreateArtifact(tenantCtx("globex"), r)
	requireRefusal(t, err, CodeUnknownCompatibleProfile)
	_, err = env.api.CreateArtifact(tenantCtx("acme"), r)
	require.NoError(t, err)
}

// Fail closed: when profile validation is not wired, or cannot answer, nothing is created.
func TestCreateFailsClosedWithoutProfileValidation(t *testing.T) {
	env := newTestEnv(t)
	r := validRequest("fw-1", []byte("data"))

	env.api.Profiles = nil
	_, err := env.api.CreateArtifact(tenantCtx("acme"), r)
	requireRefusal(t, err, CodeProfileValidationUnavail)

	env.api.Profiles = &fakeProfiles{err: errors.New("device-management is down")}
	_, err = env.api.CreateArtifact(tenantCtx("acme"), r)
	requireRefusal(t, err, CodeProfileValidationUnavail)

	found, err := env.api.Artifacts(tenantCtx("acme"), ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}})
	require.NoError(t, err)
	require.Empty(t, found.Results, "a refused declaration must leave no row behind")
}

func TestCreateRefusesWithoutAnObjectStore(t *testing.T) {
	env := newTestEnv(t)
	env.api.Blobs = nil
	_, err := env.api.CreateArtifact(tenantCtx("acme"), validRequest("fw-1", []byte("data")))
	requireRefusal(t, err, CodeObjectStoreNotConfigured)
	// The refusal must tell the operator how to fix it, naming both the bootstrap flag and the chart value.
	var re *RefusalError
	require.True(t, errors.As(err, &re))
	require.Contains(t, re.Msg, "dcctl bootstrap --object-store")
	require.Contains(t, re.Msg, "instance.config.infrastructure.blob.backend")
}

func TestSizeCeilings(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")

	r := validRequest("fw-big", []byte("x"))
	r.SizeBytes = 1<<20 + 1
	_, err := env.api.CreateArtifact(ctx, r)
	requireRefusal(t, err, CodeArtifactTooLarge)

	// The backend clamp is distinct from the configured ceiling: the S3 buffering limit.
	env.api.Limits.MaxArtifactBytes = 512
	r = validRequest("fw-clamped", []byte("x"))
	r.SizeBytes = 513
	_, err = env.api.CreateArtifact(ctx, r)
	requireRefusal(t, err, CodeArtifactTooLargeForBackend)
	r.SizeBytes = 512
	_, err = env.api.CreateArtifact(ctx, r)
	require.NoError(t, err, "exactly the ceiling is allowed")
}

func TestTenantQuotaCountsEveryNonDeletedArtifactAndIsPerTenant(t *testing.T) {
	env := newTestEnv(t)
	env.api.Limits.MaxTenantArtifactBytes = 1000
	acme := tenantCtx("acme")

	mk := func(token string, version string, size int64) *ArtifactCreateRequest {
		r := validRequest(token, []byte("x"))
		r.Version, r.SizeBytes = version, size
		return r
	}
	_, err := env.api.CreateArtifact(acme, mk("a", "1", 600))
	require.NoError(t, err)
	_, err = env.api.CreateArtifact(acme, mk("b", "2", 401))
	requireRefusal(t, err, CodeTenantArtifactQuota)
	_, err = env.api.CreateArtifact(acme, mk("b", "2", 400))
	require.NoError(t, err, "exactly at the ceiling is allowed")

	// A retired artifact whose bytes are stored still holds them, so it still counts.
	a, err := env.api.ArtifactByToken(acme, "a")
	require.NoError(t, err)
	require.NoError(t, env.api.CompleteUpload(acme, a, blob.Ref{Backend: "filesystem", Key: "inst/acme/artifacts/a"}))
	_, err = env.api.RetireArtifact(acme, "a")
	require.NoError(t, err)
	_, err = env.api.CreateArtifact(acme, mk("c", "3", 1))
	requireRefusal(t, err, CodeTenantArtifactQuota)

	// A declaration retired before any content arrived holds nothing, so it gives its
	// quota back: abandoned declarations do not burn the ceiling for ever.
	_, err = env.api.RetireArtifact(acme, "b")
	require.NoError(t, err)
	_, err = env.api.CreateArtifact(acme, mk("c", "3", 400))
	require.NoError(t, err, "the abandoned 400-byte declaration's quota is free again")
	_, err = env.api.CreateArtifact(acme, mk("d", "4", 1))
	requireRefusal(t, err, CodeTenantArtifactQuota)

	// Another tenant has its own allowance.
	_, err = env.api.CreateArtifact(tenantCtx("globex"), mk("a", "1", 1000))
	require.NoError(t, err)
}

// The per-tenant admission lock is taken inside the create transaction BEFORE the held
// sum is read: with the lock first, a second create waits and then reads a sum that
// includes the first; with it after (or absent) both read the same sum and both insert.
// The lock itself is a no-op on SQLite, so this proves the ORDER through the seam and the
// recorded statements; lock_integration_test.go observes the overshoot against PostgreSQL.
func TestTheTenantLockIsTakenBeforeTheHeldSumIsRead(t *testing.T) {
	env := newTestEnv(t)
	var events []string
	env.api.lockTenant = func(ctx context.Context, _ *gorm.DB) error {
		tenant, _ := core.TenantFromContext(ctx)
		events = append(events, "lock:"+tenant)
		return nil
	}
	record := func(tx *gorm.DB) {
		if len(tx.Statement.Selects) > 0 {
			events = append(events, "sum")
		}
	}
	require.NoError(t, env.api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:record-sum", record))
	require.NoError(t, env.api.RDB.Database.Callback().Row().Before("gorm:row").Register("test:record-sum-row", record))
	_, err := env.api.CreateArtifact(tenantCtx("acme"), validRequest("fw-1", []byte("data")))
	require.NoError(t, err)
	require.Equal(t, []string{"lock:acme", "sum"}, events)
}

// On PostgreSQL the lock is pg_advisory_xact_lock keyed on the tenant: distinct per
// tenant (tenants must not queue behind each other), stable for one.
func TestTheAdvisoryLockStatementAndKey(t *testing.T) {
	require.Equal(t, artifactLockKey("acme"), artifactLockKey("acme"))
	require.NotEqual(t, artifactLockKey("acme"), artifactLockKey("globex"))

	pg, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost", PreferSimpleProtocol: true, WithoutReturning: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	require.NoError(t, err)
	stmt := pg.Session(&gorm.Session{DryRun: true}).Exec("SELECT pg_advisory_xact_lock(?)", artifactLockKey("acme")).Statement
	require.Contains(t, stmt.SQL.String(), "pg_advisory_xact_lock")
	require.NoError(t, lockTenantArtifacts(tenantCtx("acme"), pg.Session(&gorm.Session{DryRun: true})))

	// No tenant, no lock: a key on the empty string would serialise every tenant.
	require.Error(t, lockTenantArtifacts(context.Background(), pg.Session(&gorm.Session{DryRun: true})))
}

func TestIdentityAndTokenAreUniquePerTenant(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.api.CreateArtifact(tenantCtx("acme"), validRequest("fw-1", []byte("data")))
	require.NoError(t, err)

	// Same token.
	r := validRequest("fw-1", []byte("data"))
	r.Version = "2.0.0"
	_, err = env.api.CreateArtifact(tenantCtx("acme"), r)
	require.Error(t, err)

	// Same name + component + version under a different token: the identity is immutable.
	_, err = env.api.CreateArtifact(tenantCtx("acme"), validRequest("fw-2", []byte("data")))
	require.Error(t, err)

	// Another tenant may reuse both.
	_, err = env.api.CreateArtifact(tenantCtx("globex"), validRequest("fw-1", []byte("data")))
	require.NoError(t, err)
}

func TestATenantCannotReadAnotherTenantsArtifact(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.api.CreateArtifact(tenantCtx("acme"), validRequest("fw-1", []byte("data")))
	require.NoError(t, err)

	got, err := env.api.ArtifactByToken(tenantCtx("globex"), "fw-1")
	require.NoError(t, err)
	require.Nil(t, got, "tenant B must not see tenant A's artifact by token")

	found, err := env.api.Artifacts(tenantCtx("globex"), ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}})
	require.NoError(t, err)
	require.Empty(t, found.Results)

	_, err = env.api.RetireArtifact(tenantCtx("globex"), "fw-1")
	requireRefusal(t, err, CodeArtifactNotFound)
	still, err := env.api.ArtifactByToken(tenantCtx("acme"), "fw-1")
	require.NoError(t, err)
	require.Equal(t, StateUploading, still.State, "a foreign retire must not have touched the row")
}

func TestQueriesWithoutATenantFailClosed(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.api.ArtifactByToken(context.Background(), "fw-1")
	require.Error(t, err, "the tenant-scope callback must reject a tenantless query")
}

func TestSearchFiltersAndOrdering(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	mk := func(token, version, kind, component string) {
		r := validRequest(token, []byte("x"))
		r.Version, r.Kind, r.Component = version, kind, component
		_, err := env.api.CreateArtifact(ctx, r)
		require.NoError(t, err)
	}
	mk("a", "1", KindFirmware, "firmware")
	mk("b", "2", KindSoftware, "app/agent")
	mk("c", "3", KindFirmware, "firmware")
	_, err := env.api.RetireArtifact(ctx, "c")
	require.NoError(t, err)

	fw := KindFirmware
	res, err := env.api.Artifacts(ctx, ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}, Kind: &fw})
	require.NoError(t, err)
	require.Len(t, res.Results, 2)

	st := StateRetired
	res, err = env.api.Artifacts(ctx, ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}, State: &st})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "c", res.Results[0].Token)

	comp := "app/agent"
	res, err = env.api.Artifacts(ctx, ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}, Component: &comp})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "b", res.Results[0].Token)
}

func TestRetireIsIdempotentAndKeepsTheContentReference(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	a, err := env.api.CreateArtifact(ctx, validRequest("fw-1", []byte("data")))
	require.NoError(t, err)
	ref, err := env.store.Put(ctx, blob.Key{Tenant: "acme", Purpose: BlobPurpose, ID: "obj1"},
		bytesReader("data"), blob.PutOptions{})
	require.NoError(t, err)
	require.NoError(t, env.api.CompleteUpload(ctx, a, ref))

	r1, err := env.api.RetireArtifact(ctx, "fw-1")
	require.NoError(t, err)
	r2, err := env.api.RetireArtifact(ctx, "fw-1")
	require.NoError(t, err)
	require.Equal(t, StateRetired, r1.State)
	require.Equal(t, StateRetired, r2.State)
	require.Equal(t, ref.String(), r2.BlobRef.String, "retiring keeps the bytes and their reference")
	_, err = env.store.Stat(ctx, ref)
	require.NoError(t, err, "retiring must not delete the object")
}

// An artifact is never mutable once AVAILABLE: of two completions only the first lands,
// and a RETIRED artifact cannot be completed back to AVAILABLE.
func TestCompleteUploadIsAConditionalTransition(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	a, err := env.api.CreateArtifact(ctx, validRequest("fw-1", []byte("data")))
	require.NoError(t, err)

	first := blob.Ref{Backend: "filesystem", Key: "inst/acme/artifacts/one"}
	second := blob.Ref{Backend: "filesystem", Key: "inst/acme/artifacts/two"}
	require.NoError(t, env.api.CompleteUpload(ctx, a, first))
	requireRefusal(t, env.api.CompleteUpload(ctx, a, second), CodeArtifactUploadRaceLost)

	got, err := env.api.ArtifactByToken(ctx, "fw-1")
	require.NoError(t, err)
	require.Equal(t, first.String(), got.BlobRef.String, "the first verified upload stays")

	b, err := env.api.CreateArtifact(ctx, func() *ArtifactCreateRequest {
		r := validRequest("fw-2", []byte("data"))
		r.Version = "2"
		return r
	}())
	require.NoError(t, err)
	_, err = env.api.RetireArtifact(ctx, "fw-2")
	require.NoError(t, err)
	requireRefusal(t, env.api.CompleteUpload(ctx, b, second), CodeArtifactUploadRaceLost)
}

func TestDeletedTenantWritesAreRefusedButReadsWork(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	_, err := env.api.CreateArtifact(ctx, validRequest("fw-1", []byte("data")))
	require.NoError(t, err)

	env.api.TenantDeleted = func(tenant string) bool { return tenant == "acme" }
	_, err = env.api.CreateArtifact(ctx, validRequest("fw-2", []byte("data")))
	requireRefusal(t, err, CodeTenantDeleted)
	_, err = env.api.RetireArtifact(ctx, "fw-1")
	requireRefusal(t, err, CodeTenantDeleted)

	got, err := env.api.ArtifactByToken(ctx, "fw-1")
	require.NoError(t, err)
	require.NotNil(t, got)
}

func bytesReader(s string) *bytes.Reader { return bytes.NewReader([]byte(s)) }
