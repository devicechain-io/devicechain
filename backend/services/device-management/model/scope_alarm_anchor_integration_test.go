// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The alarm anchor filter on the real engine: the stamp is a text[] compared with `@>` beside
// the scope's `&&`, which SQLite cannot run. The last test records the plan.
//
//	docker run -d --name dc-s3-alarm-pg-$$ -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-s3-alarm-pg-$$ 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'AlarmAnchor' -v
package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

// anchorRig is the scope rig plus places: "shared" holds acme-pump and globex-pump,
// "acme-only" and "globex-only" hold one each, and "other" is empty. The asset "shared-asset"
// holds both devices.
type anchorRig struct{ scopeAlarmRig }

func newAnchorRig(t *testing.T) anchorRig {
	t.Helper()
	r := anchorRig{newScopeAlarmRig(t)}
	ctx := r.operator
	_, err := r.api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "floor"})
	require.NoError(t, err)
	_, err = r.api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: "rig"})
	require.NoError(t, err)
	for _, a := range []string{"shared", "acme-only", "globex-only", "other"} {
		_, err := r.api.CreateArea(ctx, &AreaCreateRequest{Token: a, AreaTypeToken: "floor"})
		require.NoError(t, err)
	}
	_, err = r.api.CreateAsset(ctx, &AssetCreateRequest{Token: "shared-asset", AssetTypeToken: "rig"})
	require.NoError(t, err)
	r.place(t, "acme-pump", "shared")
	r.place(t, "globex-pump", "shared")
	r.place(t, "acme-pump", "acme-only")
	r.place(t, "globex-pump", "globex-only")
	for _, d := range []string{"acme-pump", "globex-pump"} {
		_, err := r.api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: d + "-on-shared-asset",
			SourceType: "device", Source: d, TargetType: "asset", Target: "shared-asset",
			RelationshipType: AssignmentRelationshipType})
		require.NoError(t, err)
	}
	return r
}

func (r anchorRig) place(t *testing.T, device, area string) {
	t.Helper()
	_, err := r.api.CreateEntityRelationship(r.operator, &EntityRelationshipCreateRequest{Token: device + "-in-" + area,
		SourceType: "device", Source: device, TargetType: "area", Target: area,
		RelationshipType: AssignmentRelationshipType})
	require.NoError(t, err)
}

func (r anchorRig) unplace(t *testing.T, device, area string) {
	t.Helper()
	_, err := r.api.RemoveEntityRelationship(r.operator, device+"-in-"+area)
	require.NoError(t, err)
}

func (r anchorRig) edge(t *testing.T, device, key string, edge string, sev AlarmSeverity, at time.Time) {
	t.Helper()
	v := 99.0
	require.NoError(t, r.api.ApplyAlarmContributorEdge(r.operator, r.devices[device], key, "temp", "rule-"+key,
		edge, string(sev), &v, at))
}

func anchorKeys(t *testing.T, r anchorRig, ctx context.Context, kind, token string) []string {
	t.Helper()
	found, err := r.api.Alarms(ctx, AlarmSearchCriteria{Anchor: &AlarmAnchor{Type: kind, Token: token}})
	require.NoError(t, err)
	require.EqualValues(t, len(found.Results), found.Pagination.TotalRecords, "the count and the page agree")
	keys := []string{}
	for _, a := range found.Results {
		keys = append(keys, a.AlarmKey)
	}
	sort.Strings(keys)
	return keys
}

func severityCounts(t *testing.T, r anchorRig, ctx context.Context, a *AlarmAnchor) map[string]int {
	t.Helper()
	rows, err := r.api.AlarmCountsBySeverity(ctx, AlarmCountCriteria{Anchor: a}, nil)
	require.NoError(t, err)
	out := map[string]int{}
	for _, c := range rows {
		out[c.Severity] = c.Count
	}
	return out
}

func typeCounts(t *testing.T, r anchorRig, ctx context.Context, a *AlarmAnchor) map[string]int {
	t.Helper()
	rows, err := r.api.AlarmCountsByDeviceType(ctx, AlarmCountCriteria{Anchor: a})
	require.NoError(t, err)
	out := map[string]int{}
	for _, c := range rows {
		out[c.DeviceType.Token+"/"+c.Severity] = c.Count
	}
	return out
}

// In a shared area each customer's lists and counts are its own devices' alarms and nothing
// more; the unscoped operator sees both, which is the control proving the fixture holds both.
func TestAlarmAnchorIsTheIntersectionWithTheScopeOnPostgres(t *testing.T) {
	r := newAnchorRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.edge(t, "acme-pump", "acme-hot", AlarmEdgeRaised, AlarmSeverityMinor, t0)
	r.edge(t, "globex-pump", "globex-hot", AlarmEdgeRaised, AlarmSeverityCritical, t0)
	acme, globex := r.member("customer:acme"), r.member("customer:globex")

	for _, c := range []struct{ kind, token string }{{"area", "shared"}, {"asset", "shared-asset"}} {
		t.Run(c.kind, func(t *testing.T) {
			a := &AlarmAnchor{Type: c.kind, Token: c.token}
			require.Equal(t, []string{"acme-hot"}, anchorKeys(t, r, acme, c.kind, c.token))
			require.Equal(t, []string{"globex-hot"}, anchorKeys(t, r, globex, c.kind, c.token))
			require.Equal(t, []string{"acme-hot", "globex-hot"}, anchorKeys(t, r, r.operator, c.kind, c.token), "control: unscoped sees both")

			require.Equal(t, map[string]int{"MINOR": 1}, severityCounts(t, r, acme, a))
			require.Equal(t, map[string]int{"CRITICAL": 1}, severityCounts(t, r, globex, a))
			require.Equal(t, map[string]int{"MINOR": 1, "CRITICAL": 1}, severityCounts(t, r, r.operator, a))

			require.Equal(t, map[string]int{"pump/MINOR": 1}, typeCounts(t, r, acme, a))
			require.Equal(t, map[string]int{"pump/CRITICAL": 1}, typeCounts(t, r, globex, a))
			require.Equal(t, map[string]int{"pump/MINOR": 1, "pump/CRITICAL": 1}, typeCounts(t, r, r.operator, a))
		})
	}

	// An area only globex's device is in answers acme nothing; a place that exists nowhere
	// answers the same; the control sees globex's alarm there.
	require.Empty(t, anchorKeys(t, r, acme, "area", "globex-only"))
	require.Empty(t, anchorKeys(t, r, acme, "area", "no-such-area"))
	require.Equal(t, []string{"globex-hot"}, anchorKeys(t, r, r.operator, "area", "globex-only"))
	require.Empty(t, severityCounts(t, r, acme, &AlarmAnchor{Type: "area", Token: "globex-only"}))
	require.Empty(t, typeCounts(t, r, acme, &AlarmAnchor{Type: "area", Token: "globex-only"}))
	require.Equal(t, map[string]int{"CRITICAL": 1}, severityCounts(t, r, r.operator, &AlarmAnchor{Type: "area", Token: "globex-only"}))

	// A customer anchor: its own is fine, another's is FORBIDDEN on every read, whether or
	// not the customer exists, and the unscoped operator may ask about either.
	require.Equal(t, []string{"acme-hot"}, anchorKeys(t, r, acme, "customer", "acme"))
	require.Equal(t, []string{"globex-hot"}, anchorKeys(t, r, r.operator, "customer", "globex"))
	for _, token := range []string{"globex", "no-such-customer"} {
		a := &AlarmAnchor{Type: "customer", Token: token}
		_, err := r.api.Alarms(acme, AlarmSearchCriteria{Anchor: a})
		require.True(t, errors.Is(err, auth.ErrForbidden), "alarms: %v", err)
		_, err = r.api.AlarmCountsBySeverity(acme, AlarmCountCriteria{Anchor: a}, nil)
		require.True(t, errors.Is(err, auth.ErrForbidden), "counts by severity: %v", err)
		_, err = r.api.AlarmCountsByDeviceType(acme, AlarmCountCriteria{Anchor: a})
		require.True(t, errors.Is(err, auth.ErrForbidden), "counts by device type: %v", err)
		// The refusal does not wait on the other facets naming something.
		dev, bogus := "device", "no-such-device"
		_, err = r.api.Alarms(acme, AlarmSearchCriteria{OriginatorType: &dev, Originator: &bogus, Anchor: a})
		require.True(t, errors.Is(err, auth.ErrForbidden), "alarms with an unresolvable originator: %v", err)
	}

	// A kind alarms are not filtered by is an error, not an empty answer.
	_, err := r.api.Alarms(acme, AlarmSearchCriteria{Anchor: &AlarmAnchor{Type: "device", Token: "acme-pump"}})
	require.Error(t, err)
	_, err = r.api.Alarms(r.operator, AlarmSearchCriteria{Anchor: &AlarmAnchor{Type: "area"}})
	require.Error(t, err)

	// A scope with no anchors reaches nothing, whatever it asks for.
	require.Empty(t, anchorKeys(t, r, r.member([]string{}...), "area", "shared"))
}

// D3: an alarm stays under the area its device was in when the alarm cycle began. Moving the
// device does not move the alarm; the device's NEXT cycle is under the new area.
func TestAlarmAnchorFollowsTheStampNotTheCurrentPlacementOnPostgres(t *testing.T) {
	r := newAnchorRig(t)
	acme := r.member("customer:acme")
	t0 := time.Now().UTC().Truncate(time.Second)
	r.edge(t, "acme-pump", "hot", AlarmEdgeRaised, AlarmSeverityMajor, t0)
	require.Equal(t, []string{"hot"}, anchorKeys(t, r, acme, "area", "shared"))

	// The device leaves "shared" for "other".
	r.unplace(t, "acme-pump", "shared")
	r.place(t, "acme-pump", "other")
	require.Equal(t, []string{"hot"}, anchorKeys(t, r, acme, "area", "shared"), "the alarm left the area its device was in when it was raised")
	require.Empty(t, anchorKeys(t, r, acme, "area", "other"), "the alarm moved with the device")
	require.Equal(t, map[string]int{"MAJOR": 1}, severityCounts(t, r, acme, &AlarmAnchor{Type: "area", Token: "shared"}))
	require.Empty(t, severityCounts(t, r, acme, &AlarmAnchor{Type: "area", Token: "other"}))

	// The device's next cycle begins where it is now.
	r.edge(t, "acme-pump", "hot", AlarmEdgeResolved, "", t0.Add(time.Minute))
	r.edge(t, "acme-pump", "hot", AlarmEdgeRaised, AlarmSeverityMajor, t0.Add(2*time.Minute))
	require.Equal(t, []string{"hot"}, anchorKeys(t, r, acme, "area", "other"))
	require.Empty(t, anchorKeys(t, r, acme, "area", "shared"))
}

// TestAlarmAnchorPlanOnPostgres records the plan of the anchor-filtered scoped count over a
// large alarms table. It asserts the answer and logs EXPLAIN so the PR can quote it.
func TestAlarmAnchorPlanOnPostgres(t *testing.T) {
	r := newAnchorRig(t)
	db := r.api.RDB.DB(r.operator)
	const n = 20000
	t0 := time.Now().UTC().Truncate(time.Second)
	// Bulk rows through SQL: n alarms spread over 400 areas and 50 customers; three of them
	// are acme's in the probed area.
	require.NoError(t, db.Exec(fmt.Sprintf(`
INSERT INTO alarms (created_at, updated_at, tenant_id, token, alarm_key, originator_type, originator_id,
  metric_key, state, severity, acknowledged, raised_time, scope_anchors, contributor_version)
SELECT now(), now(), ?, 'plan-' || g, 'plan-key-' || g, 'device', ?, 'temp', 'ACTIVE', 'MINOR', false, ?,
  ARRAY['customer:c' || (g %% 50), 'area:a' || (g %% 400)]::text[], 0
FROM generate_series(1, %d) g`, n), r.tenant, r.devices["acme-pump"], t0).Error)
	require.NoError(t, db.Exec(`UPDATE alarms SET scope_anchors = ARRAY['customer:acme','area:probe']::text[]
WHERE token IN ('plan-1','plan-2','plan-3')`).Error)
	require.NoError(t, db.Exec(`ANALYZE alarms`).Error)

	acme := r.member("customer:acme", "customer:c7")
	require.Equal(t, map[string]int{"MINOR": 3}, severityCounts(t, r, acme, &AlarmAnchor{Type: "area", Token: "probe"}))

	var plan []string
	require.NoError(t, db.Raw(`EXPLAIN (ANALYZE, COSTS OFF)
SELECT severity, COUNT(*) FROM alarms
WHERE tenant_id = ? AND deleted_at IS NULL AND scope_anchors @> ARRAY['area:probe']::text[]
  AND scope_anchors && ARRAY['customer:acme','customer:c7']::text[]
GROUP BY severity`, r.tenant).Scan(&plan).Error)
	t.Logf("EXPLAIN (%d alarms):\n%s", n, strings.Join(plan, "\n"))
}
