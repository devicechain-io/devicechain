// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The decoder column is jsonb, and Postgres re-renders what it is given: numbers are written out
// without an exponent and every ',' and ':' gains a space. A size cap measured on the bytes the
// client sent is not a cap on what a read returns, and only a real server shows the difference.
//
// Run the same way as the other integration tests in this package:
//
//	docker run -d --name dc-it-decoder -e POSTGRES_PASSWORD=postgres -P postgres:17
//	DC_IT_PGPORT=$(docker port dc-it-decoder 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'DecoderJsonb' -v
//	docker rm -f dc-it-decoder
package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jsonbDecoderDoc(tableBody string) string {
	return `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","ignore":true}],"table":{"v":1,"x":[` + tableBody + `]}}`
}

func jsonbTenant(ctx context.Context) string {
	tenant, _ := core.TenantFromContext(ctx)
	return tenant
}

func storedDecoderLength(t *testing.T, api *Api, ctx context.Context, token string) int {
	t.Helper()
	var n int
	require.NoError(t, api.RDB.DB(ctx).Raw(
		`SELECT coalesce(length(decoder_declaration::text), 0) FROM "device-management".device_profiles WHERE token = ? AND tenant_id = ?`, token, jsonbTenant(ctx)).Scan(&n).Error)
	return n
}

func TestDecoderJsonbHugeExponentIsRefusedAndNeverStored(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("jb%d", time.Now().UnixNano()))
	seedDeviceProfile(t, api, ctx, "meter")

	// The audit's input: about 18 KB as sent, ~200 MB as stored.
	hostile := jsonbDecoderDoc(strings.TrimSuffix(strings.Repeat("1e100000,", 2000), ","))
	require.Less(t, len(hostile), payloaddecode.MaxDocumentBytes, "the OLD cap, measured on the input, admits this")

	// Control: the server really does render it enormous, which is what makes the old check wrong.
	var rendered int
	require.NoError(t, api.RDB.DB(ctx).Raw(`SELECT length(?::jsonb::text)`, `{"table":[1e100000]}`).Scan(&rendered).Error)
	require.Greater(t, rendered, 100000, "premise: jsonb writes 1e100000 out in full")

	_, err := api.SetDeviceProfileDecoder(ctx, "meter", []byte(hostile))
	refusal(t, err, CodeInvalidDecoder)
	assert.Zero(t, storedDecoderLength(t, api, ctx, "meter"), "nothing was written")
}

// A legitimate decoder near the cap round-trips through jsonb under the cap and publishes.
func TestDecoderJsonbNearCapDecoderRoundTripsAndPublishes(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("jb%d", time.Now().UnixNano()))
	seedDeviceProfile(t, api, ctx, "meter")

	// Grow the table until the stored rendering is just under the cap.
	best := ""
	for k := 1000; ; k += 25 {
		doc := jsonbDecoderDoc(strings.TrimSuffix(strings.Repeat("0,", k), ","))
		if size, err := payloaddecode.CanonicalSize([]byte(doc)); err != nil || size > payloaddecode.MaxDocumentBytes {
			break
		}
		best = doc
	}
	require.NotEmpty(t, best)

	_, err := api.SetDeviceProfileDecoder(ctx, "meter", []byte(best))
	require.NoError(t, err)
	stored := storedDecoderLength(t, api, ctx, "meter")
	assert.LessOrEqual(t, stored, payloaddecode.MaxDocumentBytes, "what the server holds is under the cap")
	assert.Greater(t, stored, payloaddecode.MaxDocumentBytes-100, "the test document is near the cap")

	// It reads back (the profile read the console and the publish gate use) and still compiles.
	p, err := api.deviceProfileByToken(ctx, "meter")
	require.NoError(t, err)
	require.NotNil(t, p.DecoderDeclaration)
	assert.LessOrEqual(t, len(*p.DecoderDeclaration), payloaddecode.MaxDocumentBytes)

	_, err = api.PublishDeviceProfile(ctx, "meter", nil, nil, "tester")
	require.NoError(t, err, "a decoder the save accepted must publish")
	res, err := api.TestPayloadDecoder(ctx, &PayloadDecoderTestRequest{ProfileToken: "meter"})
	require.NoError(t, err)
	assert.Nil(t, res.Failure, "the saved decoder previews")
}

// A legacy row (written before the save gate measured the stored form) is an invalid plan: the
// preview and the publish refuse it, with no 200 MB document in the way.
func TestDecoderJsonbLegacyOverCapRowIsRefusedOnRead(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("jb%d", time.Now().UnixNano()))
	seedDeviceProfile(t, api, ctx, "meter")

	hostile := jsonbDecoderDoc(strings.TrimSuffix(strings.Repeat("1e5000,", 200), ","))
	require.NoError(t, api.RDB.DB(ctx).Exec(
		`UPDATE "device-management".device_profiles SET decoder_declaration = ?::jsonb WHERE token = ? AND tenant_id = ?`, hostile, "meter", jsonbTenant(ctx)).Error)
	require.Greater(t, storedDecoderLength(t, api, ctx, "meter"), payloaddecode.MaxDocumentBytes)

	_, err := api.PublishDeviceProfile(ctx, "meter", nil, nil, "tester")
	refusal(t, err, CodeInvalidDecoder, "save it again")

	res, err := api.TestPayloadDecoder(ctx, &PayloadDecoderTestRequest{ProfileToken: "meter"})
	require.NoError(t, err)
	require.NotNil(t, res.Failure, "the preview reports an invalid decoder instead of compiling it")
}
