// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// A line filter names a closed vocabulary: a value outside it is refused, never answered with an
// empty page that reads as "there are none". The values the served enums list are accepted.
func TestBulkChangeFiltersRefuseAnUnknownStateOverTheWire(t *testing.T) {
	withEngine(t)
	ctx, _ := fleetWireCtx(t, person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead))
	_, errs := execFleet(t, ctx, fleetWireCreate, fleetWireRequest("op1", "d1", "ghost", "d2"))
	require.Empty(t, errs)

	lines := func(filter string) (int, []string) {
		data, errs := execFleet(t, ctx, `{ bulkChangeLines(token: "op1", criteria: {pageNumber: 1, pageSize: 10, `+filter+`}) { pagination { totalRecords } } }`, nil)
		if len(errs) > 0 {
			return 0, errs
		}
		var out struct {
			L struct{ Pagination struct{ TotalRecords int } } `json:"bulkChangeLines"`
		}
		require.NoError(t, json.Unmarshal(data, &out))
		return out.L.Pagination.TotalRecords, nil
	}
	for _, filter := range []string{`posting: "POSTD"`, `posting: "planned"`, `settlement: "ambiguous"`, `settlement: ""`} {
		_, errs := lines(filter)
		require.Len(t, errs, 1, filter)
		require.True(t, strings.HasPrefix(errs[0], model.FleetCodeInvalidRequest+"|"), errs[0])
	}
	for filter, want := range map[string]int{`posting: "PLANNED"`: 2, `posting: "REFUSED"`: 1, `posting: "DISPATCHING"`: 0, `settlement: "AMBIGUOUS"`: 0} {
		n, errs := lines(filter)
		require.Empty(t, errs, filter)
		require.Equal(t, want, n, filter)
	}

	ops := func(filter string) []string {
		_, errs := execFleet(t, ctx, `{ bulkChanges(criteria: {pageNumber: 1, pageSize: 10, `+filter+`}) { pagination { totalRecords } } }`, nil)
		return errs
	}
	for _, filter := range []string{`dispatch: "ready"`, `dispatch: "DONE"`, `evidence: "open"`} {
		errs := ops(filter)
		require.Len(t, errs, 1, filter)
		require.True(t, strings.HasPrefix(errs[0], model.FleetCodeInvalidRequest+"|"), errs[0])
	}
	for _, filter := range []string{`dispatch: "READY"`, `dispatch: "DRAFT"`, `evidence: "OPEN"`, `kind: "anything.else"`} {
		require.Empty(t, ops(filter), filter)
	}
}

// afterOrdinal pages on the last ordinal read, so a line leaving the filter cannot shift another past
// the reader.
func TestBulkChangeLinesPageAfterAnOrdinalOverTheWire(t *testing.T) {
	withEngine(t)
	ctx, _ := fleetWireCtx(t, person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead))
	_, errs := execFleet(t, ctx, fleetWireCreate, fleetWireRequest("op1", "d1", "ghost", "d2"))
	require.Empty(t, errs)
	page := func(after int) ([]int, int) {
		data, errs := execFleet(t, ctx, `query($after: Int) { bulkChangeLines(token: "op1", criteria: {pageNumber: 1, pageSize: 1, afterOrdinal: $after}) { results { ordinal } pagination { totalRecords } } }`,
			map[string]any{"after": after})
		require.Empty(t, errs)
		var out struct {
			L struct {
				Results    []struct{ Ordinal int }
				Pagination struct{ TotalRecords int }
			} `json:"bulkChangeLines"`
		}
		require.NoError(t, json.Unmarshal(data, &out))
		got := []int{}
		for _, r := range out.L.Results {
			got = append(got, r.Ordinal)
		}
		return got, out.L.Pagination.TotalRecords
	}
	for after, want := range map[int][]int{0: {1}, 1: {2}, 2: {3}, 3: {}} {
		got, remaining := page(after)
		require.Equal(t, want, got, "after %d", after)
		require.Equal(t, 3-after, remaining, "the total counts what is left after the cursor")
	}
}

// lastErrorCode is the last error the engine met on the operation: a retry recorded on any of its
// lines writes it, and nothing before the first one does.
func TestBulkChangeLastErrorCodeIsTheLastRetryRecordedOverTheWire(t *testing.T) {
	withEngine(t)
	ctx, api := fleetWireCtx(t, person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead))
	_, errs := execFleet(t, ctx, fleetWireCreate, fleetWireRequest("op1", "d1", "d2"))
	require.Empty(t, errs)
	last := func() *string {
		data, errs := execFleet(t, ctx, `{ bulkChange(token: "op1") { lastErrorCode } }`, nil)
		require.Empty(t, errs)
		var out struct {
			Op struct{ LastErrorCode *string } `json:"bulkChange"`
		}
		require.NoError(t, json.Unmarshal(data, &out))
		return out.Op.LastErrorCode
	}
	require.Nil(t, last(), "no error has been met yet")

	planned := model.FleetPostingPlanned
	res, err := api.FleetOperationLines(ctx, "op1", model.FleetLineSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, Posting: &planned})
	require.NoError(t, err)
	require.Len(t, res.Results, 2)
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	require.NoError(t, api.RecordFleetLineRetry(ctx, res.Results[0].ID, "CONNECTION_LOST", at))
	require.Equal(t, "CONNECTION_LOST", *last())
	require.NoError(t, api.RecordFleetLineRetry(ctx, res.Results[1].ID, "STATEMENT_TIMEOUT", at.Add(time.Second)))
	require.Equal(t, "STATEMENT_TIMEOUT", *last(), "the latest error, whichever line met it")

	op, err := api.FleetOperationByToken(ctx, "op1")
	require.NoError(t, err)
	rt, err := api.FleetOperationRuntimeOf(ctx, op)
	require.NoError(t, err)
	require.True(t, rt.LastErrorAt.Valid)
	require.True(t, rt.LastErrorAt.Time.Equal(at.Add(time.Second)), "and when it was met")
}
