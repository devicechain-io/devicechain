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
		SourceArea: "device-management", Reason: reason, DeviceToken: device, Error: "no such device"})
	require.NoError(t, err)
}

// The acceptance query: an unknown-token device's refusal is found by reason, and nothing else
// is. Then the controls: another tenant sees none of it, a customer-scoped member is refused
// outright, and a caller without event:read is refused.
func TestFailedEventsQueryIsTenantAdminOnly(t *testing.T) {
	api := failedEventsApi(t)
	const tenant = "acme"
	at := time.Now().UTC().Add(-time.Minute)
	seedFailed(t, api, tenant, 7, at, int32(resolvedevent.FailureReason_DeviceNotFound), "ghost-1")
	seedFailed(t, api, tenant, 8, at, int32(resolvedevent.FailureReason_Unauthenticated), "dev-2")
	seedFailed(t, api, "globex", 9, at, int32(resolvedevent.FailureReason_DeviceNotFound), "ghost-9")
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})

	const q = `query { failedEvents(criteria: {pageNumber: 1, pageSize: 10, reason: DEVICE_NOT_FOUND}) {
		results { recordSeq reason reasonCode deviceToken error suppressed overflow payloadStatus }
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

	admin := scopedCtx(api, tenant, nil, string(auth.EventRead))
	rows, codes := run(admin)
	require.Empty(t, codes)
	require.Len(t, rows, 1, "only the DEVICE_NOT_FOUND row of this tenant")
	assert.Equal(t, "7", rows[0]["recordSeq"])
	assert.Equal(t, "DEVICE_NOT_FOUND", rows[0]["reason"])
	assert.EqualValues(t, 3, rows[0]["reasonCode"])
	assert.Equal(t, "ghost-1", rows[0]["deviceToken"])
	assert.Equal(t, "UNKNOWN", rows[0]["payloadStatus"], "no broker was asked, so neither held nor gone is claimed")

	// Negative controls.
	_, codes = run(scopedCtx(api, tenant, []string{"customer:acme"}, string(auth.EventRead)))
	assert.NotEmpty(t, codes, "a customer-scoped member read the tenant's failures")
	_, codes = run(scopedCtx(api, tenant, nil, string(auth.DeviceRead)))
	assert.NotEmpty(t, codes, "failedEvents answered without event:read")
	other := scopedCtx(api, "globex", nil, string(auth.EventRead))
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

	admin := scopedCtx(api, "acme", nil, string(auth.EventRead))
	resp := schema.Exec(admin, sum, "", map[string]interface{}{"s": since})
	require.Empty(t, resp.Errors)
	assert.Contains(t, string(resp.Data), `"DEVICE_NOT_FOUND"`)
	resp = schema.Exec(admin, cnt, "", map[string]interface{}{"s": since})
	require.Empty(t, resp.Errors)
	assert.JSONEq(t, `{"failedEventCount":1}`, string(resp.Data))

	for _, q := range []string{sum, cnt} {
		scoped := scopedCtx(api, "acme", []string{"customer:acme"}, string(auth.EventRead))
		assert.NotEmpty(t, schema.Exec(scoped, q, "", map[string]interface{}{"s": since}).Errors, "scoped caller")
		bad := schema.Exec(admin, q, "", map[string]interface{}{"s": "yesterday"})
		assert.NotEmpty(t, bad.Errors, "a since that is not RFC 3339 is refused")
	}
}

// A reason this build does not name is served as UNKNOWN WITH ITS NUMBER, never as a neighbour.
func TestAnUnknownReasonKeepsItsNumber(t *testing.T) {
	api := failedEventsApi(t)
	seedFailed(t, api, "acme", 1, time.Now().UTC(), 99, "dev")
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	admin := scopedCtx(api, "acme", nil, string(auth.EventRead))

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
	assert.Equal(t, "EXPIRED", payloadStatusOf(w, 99))
	assert.Equal(t, "AVAILABLE", payloadStatusOf(w, 100))
	assert.Equal(t, "AVAILABLE", payloadStatusOf(w, 5000))

	down := askWatermark(context.Background(), fakeFirst{err: errors.New("broker unreachable")})
	assert.Equal(t, "UNKNOWN", payloadStatusOf(down, 99), "an unanswered question is not 'expired'")
	assert.Equal(t, "UNKNOWN", payloadStatusOf(payloadWatermark{}, 0))
}
