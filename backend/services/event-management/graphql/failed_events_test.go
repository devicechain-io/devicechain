// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func failedEventsApi(t *testing.T) *model.Api {
	t.Helper()
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.FailedEventRecord{}))
	return model.NewApi(&rdb.RdbManager{Database: db})
}

func seedFailed(t *testing.T, api *model.Api, tenant string, seq int64, at time.Time, reason int32, device string) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	_, err := api.InsertFailedEventRecord(ctx, &model.FailedEventRecord{OccurredTime: at, RecordSeq: seq,
		SourceArea: "device-management", Reason: reason, DeviceToken: device, Error: "no such device", PayloadRetained: true})
	require.NoError(t, err)
}

// The acceptance query: an unknown-token device's refusal is found by reason, and nothing else
// is. Then the controls: another tenant sees none of it, a customer-scoped member is refused
// outright, and a viewer holding only the read-only baseline (event:read) is refused.
func TestFailedEventsQueryIsTenantAdminOnly(t *testing.T) {
	api := failedEventsApi(t)
	const tenant = "acme"
	at := time.Now().UTC().Add(-time.Minute)
	seedFailed(t, api, tenant, 7, at, int32(resolvedevent.FailureReason_DeviceNotFound), "ghost-1")
	seedFailed(t, api, tenant, 8, at, int32(resolvedevent.FailureReason_Unauthenticated), "dev-2")
	seedFailed(t, api, "globex", 9, at, int32(resolvedevent.FailureReason_DeviceNotFound), "ghost-9")
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})

	const q = `query { failedEvents(criteria: {pageNumber: 1, pageSize: 10, reason: DEVICE_NOT_FOUND}) {
		results { id reason reasonCode deviceToken error suppressed overflow payloadStatus }
		pagination { totalRecords } } }`
	run := func(ctx context.Context) (rows []map[string]interface{}, codes []string) {
		resp := schema.Exec(ctx, q, "", nil)
		for _, e := range resp.Errors {
			if c, ok := e.Extensions["code"].(string); ok {
				codes = append(codes, c)
			} else {
				codes = append(codes, e.Message)
			}
		}
		var data struct {
			FailedEvents struct{ Results []map[string]interface{} }
		}
		if len(resp.Data) > 0 && string(resp.Data) != "null" {
			require.NoError(t, json.Unmarshal(resp.Data, &data))
		}
		return data.FailedEvents.Results, codes
	}

	admin := scopedCtx(api, tenant, nil, string(auth.DeviceWrite))
	rows, codes := run(admin)
	require.Empty(t, codes)
	require.Len(t, rows, 1, "only the DEVICE_NOT_FOUND row of this tenant")
	assert.Regexp(t, `^[0-9a-f]{32}$`, rows[0]["id"], "an opaque id, not the stream position")
	assert.Equal(t, "DEVICE_NOT_FOUND", rows[0]["reason"])
	assert.EqualValues(t, 3, rows[0]["reasonCode"])
	assert.Equal(t, "ghost-1", rows[0]["deviceToken"])
	assert.Equal(t, "UNKNOWN", rows[0]["payloadStatus"], "no broker was asked, so neither held nor gone is claimed")

	// Negative controls.
	_, codes = run(scopedCtx(api, tenant, []string{"customer:acme"}, string(auth.DeviceWrite)))
	assert.NotEmpty(t, codes, "a customer-scoped member read the tenant's failures")
	_, codes = run(scopedCtx(api, tenant, nil, string(auth.EventRead), string(auth.DeviceRead)))
	assert.NotEmpty(t, codes, "failedEvents answered without device:write")
	other := scopedCtx(api, "globex", nil, string(auth.DeviceWrite))
	rows, codes = run(other)
	require.Empty(t, codes)
	require.Len(t, rows, 1)
	assert.Equal(t, "ghost-9", rows[0]["deviceToken"], "globex sees its own row and never acme's")
}

func TestSummaryAndCountAreAuthorizedToo(t *testing.T) {
	api := failedEventsApi(t)
	at := time.Now().UTC().Add(-time.Minute)
	seedFailed(t, api, "acme", 1, at, int32(resolvedevent.FailureReason_DeviceNotFound), "ghost-1")
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	since := at.Add(-time.Hour).Format(time.RFC3339)
	const sum = `query($s: String!) { failedEventSummary(since: $s) { reason records devices { deviceToken records } } }`
	const cnt = `query($s: String!) { failedEventCount(deviceToken: "ghost-1", since: $s) }`

	admin := scopedCtx(api, "acme", nil, string(auth.DeviceWrite))
	resp := schema.Exec(admin, sum, "", map[string]interface{}{"s": since})
	require.Empty(t, resp.Errors)
	assert.Contains(t, string(resp.Data), `"DEVICE_NOT_FOUND"`)
	resp = schema.Exec(admin, cnt, "", map[string]interface{}{"s": since})
	require.Empty(t, resp.Errors)
	assert.JSONEq(t, `{"failedEventCount":1}`, string(resp.Data))

	for _, q := range []string{sum, cnt} {
		scoped := scopedCtx(api, "acme", []string{"customer:acme"}, string(auth.DeviceWrite))
		assert.NotEmpty(t, schema.Exec(scoped, q, "", map[string]interface{}{"s": since}).Errors, "scoped caller")
		viewer := scopedCtx(api, "acme", nil, string(auth.EventRead), string(auth.DeviceRead))
		assert.NotEmpty(t, schema.Exec(viewer, q, "", map[string]interface{}{"s": since}).Errors,
			"a viewer with only the read-only baseline must be refused")
		bad := schema.Exec(admin, q, "", map[string]interface{}{"s": "yesterday"})
		assert.NotEmpty(t, bad.Errors, "a since that is not RFC 3339 is refused")
	}
}

// A reason this build does not name is served as UNKNOWN WITH ITS NUMBER, never as a neighbour.
func TestAnUnknownReasonKeepsItsNumber(t *testing.T) {
	api := failedEventsApi(t)
	seedFailed(t, api, "acme", 1, time.Now().UTC(), 99, "dev")
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	admin := scopedCtx(api, "acme", nil, string(auth.DeviceWrite))

	resp := schema.Exec(admin, `query { failedEvents(criteria: {pageNumber: 1, pageSize: 10, reason: UNKNOWN}) {
		results { reason reasonCode } } }`, "", nil)
	require.Empty(t, resp.Errors)
	assert.JSONEq(t, `{"failedEvents":{"results":[{"reason":"UNKNOWN","reasonCode":99}]}}`, string(resp.Data))

	// The filter for a NAMED reason does not return it.
	resp = schema.Exec(admin, `query { failedEvents(criteria: {pageNumber: 1, pageSize: 10, reason: INVALID}) {
		results { reason } } }`, "", nil)
	require.Empty(t, resp.Errors)
	assert.JSONEq(t, `{"failedEvents":{"results":[]}}`, string(resp.Data))
}

// The SDL enum IS the protobuf vocabulary: a reason added to the proto without a value here would
// be served as UNKNOWN, so this fails the day someone adds one. The negative control is the
// counter-assertion that the extraction can see a missing value.
func TestTheEnumCoversTheProtobufVocabulary(t *testing.T) {
	declared := enumValues(t, "FailedEventReason")
	want := []string{"UNKNOWN"}
	for code, name := range resolvedevent.FailureReason_name {
		if code == 0 {
			continue
		}
		want = append(want, strings.ToUpper(snake(name)))
	}
	assert.ElementsMatch(t, want, declared)

	// Control: the extraction sees a value that is missing.
	assert.NotContains(t, declared, "NOT_A_REASON")
}

func snake(s string) string {
	name, _ := model.ReasonName(int32(resolvedevent.FailureReason_value[s]))
	return name
}

func enumValues(t *testing.T, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)enum ` + name + ` \{(.*?)\n\}`).FindStringSubmatch(SchemaContent)
	require.NotNil(t, m, "enum %s not found", name)
	var out []string
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, `"`) {
			continue
		}
		out = append(out, line)
	}
	return out
}

type fakeFirst struct {
	first uint64
	err   error
}

func (f fakeFirst) StreamFirstSequence(context.Context, string) (uint64, error) {
	return f.first, f.err
}

// A record below the stream's first retained sequence reads EXPIRED, one at or above it
// AVAILABLE, and a stream that cannot be asked reads UNKNOWN. None of the three is an error.
func TestAnExpiredPayloadReadsExpiredNotAnError(t *testing.T) {
	w := askWatermark(context.Background(), fakeFirst{first: 100})
	assert.Equal(t, "EXPIRED", payloadStatusOf(w, 99, true))
	assert.Equal(t, "AVAILABLE", payloadStatusOf(w, 100, true))
	assert.Equal(t, "AVAILABLE", payloadStatusOf(w, 5000, true))
	assert.Equal(t, "NOT_RETAINED", payloadStatusOf(w, 5000, false), "a record that never held an original is not 'available'")
	assert.Equal(t, "NOT_RETAINED", payloadStatusOf(w, 99, false), "nor 'expired'")

	down := askWatermark(context.Background(), fakeFirst{err: errors.New("broker unreachable")})
	assert.Equal(t, "UNKNOWN", payloadStatusOf(down, 99, true), "an unanswered question is not 'expired'")
	assert.Equal(t, "UNKNOWN", payloadStatusOf(payloadWatermark{}, 0, true))
}

// The SDL's payload-status enum is exactly what payloadStatusOf can answer.
func TestThePayloadStatusEnumMatchesWhatTheResolverAnswers(t *testing.T) {
	declared := enumValues(t, "FailedEventPayloadStatus")
	w := askWatermark(context.Background(), fakeFirst{first: 100})
	answered := map[string]bool{
		payloadStatusOf(w, 99, true):                   true,
		payloadStatusOf(w, 100, true):                  true,
		payloadStatusOf(payloadWatermark{}, 100, true): true,
		payloadStatusOf(w, 100, false):                 true,
	}
	var got []string
	for v := range answered {
		got = append(got, v)
	}
	assert.ElementsMatch(t, declared, got)
	assert.NotContains(t, declared, "NOT_A_STATUS", "control: the extraction sees a value that is missing")
}

// The stream position is not part of the tenant's schema: it is a position on a stream every
// tenant shares.
func TestTheStreamPositionIsNotServed(t *testing.T) {
	api := failedEventsApi(t)
	seedFailed(t, api, "acme", 5, time.Now().UTC(), int32(resolvedevent.FailureReason_DeviceNotFound), "dev")
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	admin := scopedCtx(api, "acme", nil, string(auth.DeviceWrite))
	resp := schema.Exec(admin, `query { failedEvents(criteria: {pageNumber: 1, pageSize: 10}) { results { recordSeq } } }`, "", nil)
	assert.NotEmpty(t, resp.Errors, "recordSeq must not be a field of FailedEvent")
	resp = schema.Exec(admin, `query { failedEvents(criteria: {pageNumber: 1, pageSize: 10}) { results { id } } }`, "", nil)
	require.Empty(t, resp.Errors, "control: the same query with the opaque id succeeds")
}

// Every read of the index looks back at most 31 days, and a device filter means what the writer
// stored.
func TestQueryBoundsAndFilterNormalization(t *testing.T) {
	api := failedEventsApi(t)
	now := time.Now().UTC()
	long := strings.Repeat("a", 128)
	nf := int32(resolvedevent.FailureReason_DeviceNotFound)
	seedFailed(t, api, "acme", 1, now.Add(-40*24*time.Hour), nf, "old-dev")
	seedFailed(t, api, "acme", 2, now.Add(-time.Hour), nf, "new-dev")
	seedFailed(t, api, "acme", 3, now.Add(-time.Minute), nf, long)
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	admin := scopedCtx(api, "acme", nil, string(auth.DeviceWrite))
	tokens := func(q string, vars map[string]interface{}) ([]string, int) {
		resp := schema.Exec(admin, q, "", vars)
		var data struct {
			FailedEvents struct {
				Results []struct{ DeviceToken string }
			}
		}
		if len(resp.Errors) == 0 {
			require.NoError(t, json.Unmarshal(resp.Data, &data))
		}
		var out []string
		for _, r := range data.FailedEvents.Results {
			out = append(out, r.DeviceToken)
		}
		return out, len(resp.Errors)
	}

	// No since: the default window, not everything the table holds.
	got, _ := tokens(`query { failedEvents(criteria: {pageNumber: 1, pageSize: 10}) { results { deviceToken } } }`, nil)
	assert.ElementsMatch(t, []string{"new-dev", long}, got, "the 40-day-old row is outside the default window")
	// A since older than the floor is clamped to it.
	got, _ = tokens(`query($s: String) { failedEvents(criteria: {pageNumber: 1, pageSize: 10, since: $s}) { results { deviceToken } } }`,
		map[string]interface{}{"s": now.Add(-90 * 24 * time.Hour).Format(time.RFC3339)})
	assert.ElementsMatch(t, []string{"new-dev", long}, got, "a since beyond 31 days reads 31 days")
	// A narrower since is honoured.
	got, _ = tokens(`query($s: String) { failedEvents(criteria: {pageNumber: 1, pageSize: 10, since: $s}) { results { deviceToken } } }`,
		map[string]interface{}{"s": now.Add(-10 * time.Minute).Format(time.RFC3339)})
	assert.Equal(t, []string{long}, got)

	// A token longer than the column is found the way the writer stored it.
	got, _ = tokens(`query($t: String) { failedEvents(criteria: {pageNumber: 1, pageSize: 10, deviceToken: $t}) { results { deviceToken } } }`,
		map[string]interface{}{"t": long + "bbbb"})
	assert.Equal(t, []string{long}, got)

	// The overflow marker is refused on the list and on the count.
	_, errs := tokens(`query { failedEvents(criteria: {pageNumber: 1, pageSize: 10, deviceToken: "*"}) { results { deviceToken } } }`, nil)
	assert.NotZero(t, errs)
	cnt := schema.Exec(admin, `query { failedEventCount(deviceToken: "*", since: "2020-01-01T00:00:00Z") }`, "", nil)
	assert.NotEmpty(t, cnt.Errors)

	// The count is clamped too: the 40-day-old row is not counted from a since beyond the floor.
	count := func(tok string) string {
		r := schema.Exec(admin, `query($t: String!) { failedEventCount(deviceToken: $t, since: "2020-01-01T00:00:00Z") }`, "",
			map[string]interface{}{"t": tok})
		require.Empty(t, r.Errors)
		return string(r.Data)
	}
	assert.JSONEq(t, `{"failedEventCount":0}`, count("old-dev"))
	assert.JSONEq(t, `{"failedEventCount":1}`, count("new-dev"), "control: a row inside the floor is counted")
}
