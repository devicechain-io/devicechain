// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intentseam

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/intent"
	"gorm.io/gorm"
)

// The stored vocabulary this package reads. They are device-management's model constants,
// repeated because the model imports this package; TestVocabularyAgreesWithTheModel holds them
// together.
const (
	deviceMemberType = string(entity.TypeDevice)
	dynamicMode      = "dynamic"
	sharedScope      = "SHARED"
)

// The group rejection codes, the same vocabulary resolveDeviceGroupTargets answers with.
const (
	CodeGroupNotFound        = "GROUP_NOT_FOUND"
	CodeGroupNotADeviceGroup = "GROUP_NOT_A_DEVICE_GROUP"
	CodeGroupVersionNotFound = "GROUP_VERSION_NOT_FOUND"
	CodeGroupNotVersioned    = "GROUP_NOT_VERSIONED"
)

// Target is one frozen version of a dynamic device group. The seam never reads a group's
// editable draft selector.
type Target struct {
	GroupToken string
	Version    int32
}

// Rejection is a decided refusal of a target. When Rejected is false the other two are empty.
type Rejection struct {
	Rejected bool
	Code     string
	Reason   string
}

func reject(code, format string, a ...any) Rejection {
	return Rejection{Rejected: true, Code: code, Reason: fmt.Sprintf(format, a...)}
}

// Row is one member as the observation sees it: fixed-size whatever the stored value is.
type Row struct {
	DeviceID          int64
	Token             string
	DeclaredValueType *string
	Present           bool
	ValueType         string
	ValueDigest       string
}

// DeclarationCount is how many members declare the key with one type; a nil type counts the
// members that do not declare it (no profile, an unpublished profile, or a version that does not
// name the key).
type DeclarationCount struct {
	DeclaredValueType *string
	Count             int64
}

// DigestAnswer is configurationKeyStateDigest's answer. When Rejected is set every other field
// but ResolvedVersion is empty.
type DigestAnswer struct {
	Rejection
	ResolvedVersion int32
	MemberCount     int64
	Digest          string
	Declarations    []DeclarationCount
	PresentCount    int64
	OversizeCount   int64
}

// PageAnswer is one keyset page. NextCursor is the last row's device id when the page is full
// (or ended early on the byte budget) and zero when the walk is done.
type PageAnswer struct {
	Rejection
	ResolvedVersion int32
	Rows            []Row
	NextCursor      int64
}

// IntersectAnswer reports whether two frozen targets share a live device.
type IntersectAnswer struct {
	Rejection
	Intersects bool
}

// frozenTarget is a resolved, lowered target.
type frozenTarget struct {
	frag string
	args []any
}

// The two group lookups, each scoped by its own tenant and liveness predicates.
const (
	groupSQL = `SELECT g.id, g.member_type, g.membership_mode FROM entity_groups g
WHERE g.tenant_id = ? AND g.deleted_at IS NULL AND g.token = ?`
	groupVersionSQL = `SELECT v.selector, v.member_type FROM entity_group_versions v
WHERE v.tenant_id = ? AND v.deleted_at IS NULL AND v.entity_group_id = ? AND v.version = ?`
)

// resolveTarget reads the frozen version of a dynamic device group, in the caller's snapshot, and
// lowers its selector. A missing group and another tenant's group are the same rejection.
func resolveTarget(tx *gorm.DB, tenant string, t Target) (*frozenTarget, Rejection, error) {
	if t.Version < 1 {
		return nil, reject(CodeGroupVersionNotFound, "entity group %q has no version %d", t.GroupToken, t.Version), nil
	}
	var groups []struct {
		ID             int64
		MemberType     string
		MembershipMode string
	}
	if err := tx.Raw(groupSQL, tenant, t.GroupToken).Scan(&groups).Error; err != nil {
		return nil, Rejection{}, err
	}
	if len(groups) == 0 {
		return nil, reject(CodeGroupNotFound, "entity group %q does not exist", t.GroupToken), nil
	}
	g := groups[0]
	if g.MemberType != deviceMemberType {
		return nil, reject(CodeGroupNotADeviceGroup, "entity group %q collects %s, not devices", t.GroupToken, g.MemberType), nil
	}
	if g.MembershipMode != dynamicMode {
		return nil, reject(CodeGroupNotVersioned, "entity group %q is a static group and has no versions", t.GroupToken), nil
	}
	var versions []struct {
		Selector   string
		MemberType string
	}
	if err := tx.Raw(groupVersionSQL, tenant, g.ID, t.Version).Scan(&versions).Error; err != nil {
		return nil, Rejection{}, err
	}
	if len(versions) == 0 {
		return nil, reject(CodeGroupVersionNotFound, "entity group %q has no version %d", t.GroupToken, t.Version), nil
	}
	if versions[0].MemberType != deviceMemberType {
		return nil, reject(CodeGroupNotADeviceGroup, "entity group %q version %d collects %s, not devices",
			t.GroupToken, t.Version, versions[0].MemberType), nil
	}
	frag, args, err := lowerFrozen(tenant, versions[0].Selector)
	if err != nil {
		return nil, Rejection{}, fmt.Errorf("entity group %q version %d: %w", t.GroupToken, t.Version, err)
	}
	return &frozenTarget{frag: frag, args: args}, Rejection{}, nil
}

// lowerFrozen lowers a frozen selector for the devices table. A frozen selector cleared the
// publish gate, so a failure here is an error, never a rejection.
func lowerFrozen(tenant, source string) (string, []any, error) {
	sel, err := selector.Compile(source, deviceMemberType)
	if err != nil {
		return "", nil, err
	}
	return sel.Lower(selector.LowerParams{
		TenantId:    tenant,
		MemberType:  deviceMemberType,
		MemberTable: "devices",
		FacetScope:  sharedScope,
		NumericCast: func(col string) string { return col + "::numeric" },
	})
}

// memberPredicate is the ONE membership predicate every statement starts from: the outer devices
// row, scoped by tenant and liveness in the statement itself, AND the lowered frozen selector.
// Membership is decided by this and nothing else.
func memberPredicate(tenant string, t *frozenTarget) (string, []any) {
	return `FROM devices
WHERE devices.tenant_id = ? AND devices.deleted_at IS NULL AND (` + t.frag + `)`, append([]any{tenant}, t.args...)
}

// membersSQL is the membership builder every observation projection starts from: the members
// after a keyset cursor, bounded. It selects the columns the observation needs; a later
// projection (reported-state counts) selects from it unchanged.
//
// A page is ordered by id, so the bound keeps the first members after the cursor. The digest is
// not: it orders its own aggregate, and its bound only detects a target over the limit, which
// is refused whichever members were kept. Leaving the ORDER BY off there is a cost decision, not
// a style one: with ORDER BY id LIMIT n the planner may walk the devices primary key of every
// tenant to stop early, which on the 200,000-device cost fixture read 885,000 other rows for a
// negated selector.
func membersSQL(tenant string, t *frozenTarget, afterID int64, limit int, ordered bool) (string, []any) {
	from, args := memberPredicate(tenant, t)
	q := `SELECT devices.id, devices.token, devices.device_type_id ` + from + ` AND devices.id > ?`
	if ordered {
		q += `
ORDER BY devices.id`
	}
	return q + ` LIMIT ?`, append(args, afterID, limit)
}

// observationSQL builds the row set r over the members: the row-preserving chain to the key's
// declaration, mirroring activeConfigurationDeclaration link for link (device type, its profile,
// the profile's active version), the key's declared type parsed once per distinct profile
// version, and the member's SHARED attribute for the key. Every link is a LEFT JOIN with its
// predicates in ON, so no link can remove a member. A stored value longer than
// intent.MaxIntentValueBytes is reported by its length (octet_length reads the stored size and
// does not fetch an out-of-line value) and is never hashed.
//
// Extension point: a later column of the page (an origin class, a write time) is added to r and
// to the page projection only. The digest line is fixed by intent.ObservationHasher's format and
// changes only together with intent.CompilerBuild.
func observationSQL(tenant, key string, t *frozenTarget, afterID int64, limit int, ordered bool) (string, []any) {
	members, margs := membersSQL(tenant, t, afterID, limit, ordered)
	q := `WITH m AS MATERIALIZED (` + members + `),
c AS MATERIALIZED (
SELECT m.id, m.token, dp.active_version, pv.id AS pv_id FROM m
LEFT JOIN device_types dt ON dt.tenant_id = ? AND dt.deleted_at IS NULL AND dt.id = m.device_type_id
LEFT JOIN device_profiles dp ON dp.tenant_id = ? AND dp.deleted_at IS NULL AND dp.id = dt.profile_id
LEFT JOIN device_profile_versions pv ON pv.tenant_id = ? AND pv.deleted_at IS NULL AND pv.device_profile_id = dp.id AND pv.version = dp.active_version
),
decl AS MATERIALIZED (
SELECT pvs.id AS pv_id, (SELECT e.value ->> 'valueType' FROM jsonb_array_elements(CASE WHEN jsonb_typeof(pvs.snapshot -> 'configuration') = 'array' THEN pvs.snapshot -> 'configuration' ELSE '[]'::jsonb END) e WHERE e.value ->> 'key' = ? LIMIT 1) AS declared_value_type
FROM device_profile_versions pvs
WHERE pvs.tenant_id = ? AND pvs.deleted_at IS NULL AND pvs.id IN (SELECT DISTINCT c.pv_id FROM c WHERE c.pv_id IS NOT NULL)
),
r AS MATERIALIZED (
SELECT c.id, c.token, d.declared_value_type,
(c.active_version IS NOT NULL AND c.pv_id IS NULL) AS broken,
(ea.value IS NOT NULL) AS present,
CASE WHEN ea.value IS NULL THEN '' ELSE ea.value_type END AS value_type,
CASE WHEN ea.value IS NULL THEN ''
WHEN octet_length(ea.value) <= ` + fmt.Sprint(intent.MaxIntentValueBytes) + ` THEN 'sha256:' || encode(sha256(convert_to(ea.value_type || ':' || ea.value, 'UTF8')), 'hex')
ELSE 'OVERSIZE:' || octet_length(ea.value) END AS value_digest
FROM c
LEFT JOIN decl d ON d.pv_id = c.pv_id
LEFT JOIN entity_attributes ea ON ea.tenant_id = ? AND ea.deleted_at IS NULL AND ea.entity_type = ? AND ea.entity_id = c.id AND ea.scope = ? AND ea.attr_key = ?
)
`
	args := append(margs, tenant, tenant, tenant, key, tenant, tenant, deviceMemberType, sharedScope, key)
	return q, args
}

// digestSQL projects r to the digest: counts, the declarations, and SHA-256 over the rows in
// ascending id order in intent.ObservationHasher's line format. The members are bounded at one
// more than maxMembers, so a larger target is detected without being hashed whole.
func digestSQL(tenant, key string, t *frozenTarget, maxMembers int) (string, []any) {
	q, args := observationSQL(tenant, key, t, 0, maxMembers+1, false)
	q += `SELECT count(*) AS member_count,
count(*) FILTER (WHERE r.broken) AS broken_count,
count(*) FILTER (WHERE r.present) AS present_count,
count(*) FILTER (WHERE r.value_digest LIKE 'OVERSIZE:%') AS oversize_count,
'sha256:' || encode(sha256(convert_to(coalesce(string_agg(r.id::text || E'\t' || coalesce(r.declared_value_type, '') || E'\t' || CASE WHEN r.present THEN '1' ELSE '0' END || E'\t' || r.value_type || E'\t' || r.value_digest || E'\n', '' ORDER BY r.id), ''), 'UTF8')), 'hex') AS digest,
coalesce((SELECT jsonb_agg(jsonb_build_object('t', x.t, 'n', x.n)) FROM (SELECT r2.declared_value_type AS t, count(*) AS n FROM r r2 GROUP BY r2.declared_value_type) x), '[]'::jsonb)::text AS declarations
FROM r`
	return q, args
}

// pageSQL projects r to one page of rows.
func pageSQL(tenant, key string, t *frozenTarget, afterID int64, limit int) (string, []any) {
	q, args := observationSQL(tenant, key, t, afterID, limit, true)
	q += `SELECT r.id, r.token, r.declared_value_type, r.broken, r.present, r.value_type, r.value_digest FROM r ORDER BY r.id`
	return q, args
}

// intersectSQL asks whether one live device of the tenant satisfies both frozen selectors. Each
// target's members are materialized from the scoped membership predicate before they are
// compared, so the statement reads each target once, within the tenant, whether or not the two
// intersect. (A single EXISTS over both selectors lets the planner walk the devices primary key of
// every tenant hoping to stop early, which on two disjoint targets reads the whole table.)
func intersectSQL(tenant string, a, b *frozenTarget) (string, []any) {
	fa, aargs := memberPredicate(tenant, a)
	fb, bargs := memberPredicate(tenant, b)
	q := `WITH ta AS MATERIALIZED (SELECT devices.id ` + fa + `),
tb AS MATERIALIZED (SELECT devices.id ` + fb + `)
SELECT EXISTS (SELECT 1 FROM ta WHERE ta.id IN (SELECT tb.id FROM tb)) AS intersects`
	return q, append(aargs, bargs...)
}

func tenantOf(ctx context.Context, db *gorm.DB) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return "", core.ErrNoTenant
	}
	if db.Dialector.Name() != "postgres" {
		return "", ErrPostgresOnly
	}
	return tenant, nil
}

// Digest answers configurationKeyStateDigest: one statement, so one snapshot.
func (s *Seam) Digest(ctx context.Context, db *gorm.DB, t Target, key string) (*DigestAnswer, error) {
	tenant, err := tenantOf(ctx, db)
	if err != nil {
		return nil, err
	}
	out := &DigestAnswer{ResolvedVersion: t.Version}
	err = s.Run(ctx, db, OpDigest, func(tx *gorm.DB) (bool, error) {
		frozen, rej, err := resolveTarget(tx, tenant, t)
		if err != nil || rej.Rejected {
			out.Rejection = rej
			return rej.Rejected, err
		}
		q, args := digestSQL(tenant, key, frozen, s.maxMembers)
		var row struct {
			MemberCount   int64
			BrokenCount   int64
			PresentCount  int64
			OversizeCount int64
			Digest        string
			Declarations  string
		}
		if err := tx.Raw(q, args...).Scan(&row).Error; err != nil {
			return false, err
		}
		switch {
		case row.BrokenCount > 0:
			out.Rejection = reject(CodeProfileVersionMissing,
				"%d members' profiles name an active version that does not exist", row.BrokenCount)
		case row.MemberCount > int64(s.maxMembers):
			out.Rejection = reject(CodeTargetTooLarge,
				"entity group %q version %d has more than %d members", t.GroupToken, t.Version, s.maxMembers)
		}
		if out.Rejection.Rejected {
			return true, nil
		}
		decl, err := parseDeclarations(row.Declarations)
		if err != nil {
			return false, err
		}
		out.MemberCount, out.PresentCount, out.OversizeCount = row.MemberCount, row.PresentCount, row.OversizeCount
		out.Digest, out.Declarations = row.Digest, decl
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// parseDeclarations decodes the statement's declaration counts, the undeclared count first and
// the declared types in byte order, so the answer is deterministic.
func parseDeclarations(raw string) ([]DeclarationCount, error) {
	var entries []struct {
		T *string `json:"t"`
		N int64   `json:"n"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("decode the declaration counts: %w", err)
	}
	out := make([]DeclarationCount, 0, len(entries))
	for _, e := range entries {
		out = append(out, DeclarationCount{DeclaredValueType: e.T, Count: e.N})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].DeclaredValueType, out[j].DeclaredValueType
		if a == nil || b == nil {
			return a == nil && b != nil
		}
		return *a < *b
	})
	return out, nil
}

// Page answers configurationKeyState: one keyset page, built from the same FROM clause as the
// digest, so the two cannot disagree about who is a member.
func (s *Seam) Page(ctx context.Context, db *gorm.DB, t Target, key string, afterID int64, limit int) (*PageAnswer, error) {
	if limit < 1 || limit > MaxPageRows {
		return nil, fmt.Errorf("a page holds 1 to %d rows, got %d", MaxPageRows, limit)
	}
	if afterID < 0 {
		return nil, fmt.Errorf("a page cursor cannot be negative, got %d", afterID)
	}
	tenant, err := tenantOf(ctx, db)
	if err != nil {
		return nil, err
	}
	out := &PageAnswer{ResolvedVersion: t.Version, Rows: []Row{}}
	err = s.Run(ctx, db, OpPage, func(tx *gorm.DB) (bool, error) {
		frozen, rej, err := resolveTarget(tx, tenant, t)
		if err != nil || rej.Rejected {
			out.Rejection = rej
			return rej.Rejected, err
		}
		q, args := pageSQL(tenant, key, frozen, afterID, limit)
		var rows []struct {
			ID                int64
			Token             string
			DeclaredValueType *string
			Broken            bool
			Present           bool
			ValueType         string
			ValueDigest       string
		}
		if err := tx.Raw(q, args...).Scan(&rows).Error; err != nil {
			return false, err
		}
		bytes := 0
		for i, r := range rows {
			if r.Broken {
				out.Rejection = reject(CodeProfileVersionMissing,
					"device %d's profile names an active version that does not exist", r.ID)
				out.Rows, out.NextCursor = []Row{}, 0
				return true, nil
			}
			row := Row{DeviceID: r.ID, Token: r.Token, DeclaredValueType: r.DeclaredValueType, Present: r.Present,
				ValueType: r.ValueType, ValueDigest: r.ValueDigest}
			size := rowBytes(row)
			if i > 0 && bytes+size > s.pageBytes {
				out.NextCursor = out.Rows[len(out.Rows)-1].DeviceID
				return false, nil
			}
			bytes += size
			out.Rows = append(out.Rows, row)
		}
		if len(rows) == limit {
			out.NextCursor = rows[len(rows)-1].ID
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// rowBytes is a row's size as the GraphQL page serializes it: every field name and value, JSON
// escaped, plus the separators. It is what the byte budget counts.
func rowBytes(r Row) int {
	b, err := json.Marshal(struct {
		DeviceID          string  `json:"deviceId"`
		Token             string  `json:"token"`
		DeclaredValueType *string `json:"declaredValueType"`
		Present           bool    `json:"present"`
		ValueType         string  `json:"valueType"`
		ValueDigest       string  `json:"valueDigest"`
	}{fmt.Sprint(r.DeviceID), r.Token, r.DeclaredValueType, r.Present, r.ValueType, r.ValueDigest})
	if err != nil {
		return len(r.Token)*6 + 256
	}
	return len(b) + 1
}

// Intersect answers groupTargetsIntersect for two frozen targets: two groups, or two versions of
// one group, alike. A rejection of either target is the answer's rejection.
func (s *Seam) Intersect(ctx context.Context, db *gorm.DB, a, b Target) (*IntersectAnswer, error) {
	tenant, err := tenantOf(ctx, db)
	if err != nil {
		return nil, err
	}
	out := &IntersectAnswer{}
	err = s.Run(ctx, db, OpIntersect, func(tx *gorm.DB) (bool, error) {
		fa, rej, err := resolveTarget(tx, tenant, a)
		if err != nil || rej.Rejected {
			out.Rejection = rej
			return rej.Rejected, err
		}
		fb, rej, err := resolveTarget(tx, tenant, b)
		if err != nil || rej.Rejected {
			out.Rejection = rej
			return rej.Rejected, err
		}
		q, args := intersectSQL(tenant, fa, fb)
		var row struct{ Intersects bool }
		if err := tx.Raw(q, args...).Scan(&row).Error; err != nil {
			return false, err
		}
		out.Intersects = row.Intersects
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
