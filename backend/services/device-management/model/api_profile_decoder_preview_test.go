// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// asCaller adds claims granting exactly the authorities (and the scope anchors, when given).
func asCaller(ctx context.Context, anchors []string, authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: strs, ScopeAnchors: anchors})
}

func readings(r *PayloadDecoderTestResult) map[string]string {
	out := map[string]string{}
	for _, x := range r.Readings {
		out[x.Key] = x.Value
	}
	return out
}

func (f *decoderFixture) test(req *PayloadDecoderTestRequest) *PayloadDecoderTestResult {
	f.t.Helper()
	req.ProfileToken = "meter"
	res, err := f.api.TestPayloadDecoder(f.ctx, req)
	require.NoError(f.t, err)
	return res
}

func TestTestDecoderRunsAFrameAndReportsVectors(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))

	for name, req := range map[string]*PayloadDecoderTestRequest{
		"hex":    {FrameHex: str("00 d7 01"), FPort: i32(2)},
		"base64": {FrameBase64: str("ANcB"), FPort: i32(2)},
	} {
		res := f.test(req)
		require.True(t, res.OK, "%s: %+v", name, res.Failure)
		assert.Equal(t, map[string]string{"temperature": "21.5", "door": "1"}, readings(res), name)
		require.NotNil(t, res.Section)
		assert.Equal(t, "telemetry", *res.Section)
		assert.Positive(t, res.Cost)
		require.Len(t, res.Vectors, 1)
		assert.True(t, res.Vectors[0].OK)
		assert.Equal(t, "room sensor", res.Vectors[0].Name)
	}
}

func TestTestDecoderRunsTheSavedDraftOrASuppliedDocument(t *testing.T) {
	f := newDecoderFixture(t)
	// No saved draft and no document: nothing to test.
	_, err := f.api.TestPayloadDecoder(f.ctx, &PayloadDecoderTestRequest{ProfileToken: "meter", FrameHex: str("00D701")})
	var bad *InvalidArgumentError
	require.ErrorAs(t, err, &bad)

	// A supplied document runs without being saved.
	res := f.test(&PayloadDecoderTestRequest{Document: str(decoderDoc), FrameHex: str("00D701"), FPort: i32(2)})
	require.True(t, res.OK, "%+v", res.Failure)
	assert.Nil(t, f.profile().DecoderDeclaration, "testing saved the document")
}

func TestTestDecoderReportsFailuresAsResults(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))

	// The port does not match any section.
	res := f.test(&PayloadDecoderTestRequest{FrameHex: str("00D701"), FPort: i32(9)})
	require.False(t, res.OK)
	require.NotNil(t, res.Failure)
	assert.Equal(t, "no_section", res.Failure.Reason)

	// An offset past the end of the frame names the expression.
	res = f.test(&PayloadDecoderTestRequest{FrameHex: str("00"), FPort: i32(2)})
	require.False(t, res.OK)
	assert.Equal(t, "eval_error", res.Failure.Reason)
	assert.Equal(t, "sections[0].outputs[0].expr", res.Failure.Path)

	// A frame that is not hex.
	res = f.test(&PayloadDecoderTestRequest{FrameHex: str("zz")})
	require.False(t, res.OK)
	assert.Equal(t, "input_malformed", res.Failure.Reason)

	// A frame over the cap.
	res = f.test(&PayloadDecoderTestRequest{FrameHex: str(repeat("00", 4097)), FPort: i32(2)})
	require.False(t, res.OK)
	assert.Equal(t, "input_too_large", res.Failure.Reason)

	// A decoder that needs the frame, given JSON.
	res = f.test(&PayloadDecoderTestRequest{JSON: str(`{"a":1}`)})
	require.False(t, res.OK)
	assert.Equal(t, "missing_input", res.Failure.Reason)
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// A23: an output the target metric refuses is a decode failure, and the panel says so.
func TestTestDecoderValidatesOutputsAgainstTheMetric(t *testing.T) {
	f := newDecoderFixture(t)
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
	  {"key":"door","expr":"double(u8(frame, 0)) / 2.0"}]}]}`
	res := f.test(&PayloadDecoderTestRequest{Document: str(doc), FrameHex: str("03")})
	require.False(t, res.OK)
	assert.Equal(t, "invalid_output", res.Failure.Reason, "an INT metric must refuse 1.5")
	res = f.test(&PayloadDecoderTestRequest{Document: str(doc), FrameHex: str("04")})
	require.True(t, res.OK, "%+v", res.Failure)
	assert.Equal(t, "2", readings(res)["door"], "an INT metric is rendered as an integer")
}

func TestTestDecoderReportsACompileFailureNotATransportError(t *testing.T) {
	f := newDecoderFixture(t)
	res := f.test(&PayloadDecoderTestRequest{Document: str(withDoc(t, `"key": "door"`, `"key": "fahrenheit"`)), FrameHex: str("00D701")})
	require.False(t, res.OK)
	require.NotNil(t, res.Failure)
	assert.Equal(t, "compile_failed", res.Failure.Reason)
	assert.Contains(t, res.Failure.Message, "fahrenheit")
	assert.Empty(t, res.Readings)
	res = f.test(&PayloadDecoderTestRequest{Document: str(`{"kind":"CEL","envVersion":2,"input":"FRAME","sections":[]}`), FrameHex: str("00")})
	assert.Equal(t, "env_unsupported", res.Failure.Reason)
}

func TestTestDecoderWithNoInputRunsOnlyTheVectors(t *testing.T) {
	f := newDecoderFixture(t)
	res := f.test(&PayloadDecoderTestRequest{Document: str(withDoc(t, `"door": 1}`, `"door": 0}`))})
	assert.True(t, res.OK)
	require.Len(t, res.Vectors, 1)
	assert.False(t, res.Vectors[0].OK)
	assert.Contains(t, res.Vectors[0].Error, "door")
}

func TestTestDecoderRefusesAmbiguousOrOversizedRequests(t *testing.T) {
	f := newDecoderFixture(t)
	for name, req := range map[string]*PayloadDecoderTestRequest{
		"two inputs":      {FrameHex: str("00"), FrameBase64: str("AA==")},
		"webhook alone":   {WebhookBody: str("{}")},
		"token no body":   {IntegrationToken: str("x"), FrameHex: str("00")},
		"headers no body": {Headers: []NameValue{{Name: "a", Value: "b"}}},
	} {
		req.ProfileToken = "meter"
		req.Document = str(decoderDoc)
		_, err := f.api.TestPayloadDecoder(f.ctx, req)
		var bad *InvalidArgumentError
		assert.ErrorAs(t, err, &bad, name)
	}
	_, err := f.api.TestPayloadDecoder(f.ctx, &PayloadDecoderTestRequest{ProfileToken: "meter", Document: str(decoderDoc),
		JSON: str(repeat("x", 65<<10))})
	_, over := limit.As(err)
	assert.True(t, over, "an oversized json was not refused with a limit error: %v", err)
}

func TestTestDecoderJSONInputsFollowTheDeclaredInput(t *testing.T) {
	f := newDecoderFixture(t)
	jsonDoc := `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[
	  {"key":"temperature","expr":"double(json.temp) * 2.0"}]}]}`
	res := f.test(&PayloadDecoderTestRequest{Document: str(jsonDoc), JSON: str(`{"temp": 10.5}`)})
	require.True(t, res.OK, "%+v", res.Failure)
	assert.Equal(t, "21", readings(res)["temperature"])

	decodedDoc := `{"kind":"CEL","envVersion":1,"input":"DECODED","sections":[{"name":"s","outputs":[
	  {"key":"temperature","expr":"double(decoded.t)"}]}]}`
	res = f.test(&PayloadDecoderTestRequest{Document: str(decodedDoc), JSON: str(`{"t": 4}`)})
	require.True(t, res.OK, "%+v", res.Failure)
	assert.Equal(t, "4", readings(res)["temperature"])
}

// createIntegration inserts an integration row directly: the test door reads only its stored
// descriptor, so the credential machinery of CreateIntegration is not part of what is measured.
func (f *decoderFixture) createIntegration(token, typ, identity string, generic *string, link bool) {
	f.t.Helper()
	in := &Integration{Type: typ, Enabled: true, Identity: identity, LinkMetrics: link, TokenDigest: token}
	in.Token = token
	if generic != nil {
		g := datatypes.JSON(*generic)
		in.Generic = &g
	}
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Create(in).Error)
}

func chirpUp() string {
	return `{
  "deduplicationId": "c9dbe358-2578-4fb7-b295-66b44edc45a6",
  "time": "` + time.Now().UTC().Format(time.RFC3339Nano) + `",
  "deviceInfo": {"devEui": "0101010101010101"},
  "fPort": 2, "fCnt": 7, "data": "ANcB",
  "rxInfo": [{"rssi": -60, "snr": 7.5}, {"rssi": -90, "snr": 2}]
}`
}

func TestTestDecoderParsesAWholeWebhookBodyWithTheSameCodecAsIngest(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	f.createIntegration("chirp", "CHIRPSTACK_V4", "DEV_EUI", nil, true)
	f.createIntegration("chirp-quiet", "CHIRPSTACK_V4", "DEV_EUI", nil, false)

	req := &PayloadDecoderTestRequest{WebhookBody: str(chirpUp()), IntegrationToken: str("chirp"),
		Query: []NameValue{{Name: "event", Value: "up"}}, ContentType: str("application/json")}
	res := f.test(req)
	require.True(t, res.OK, "%+v", res.Failure)
	got := readings(res)
	assert.Equal(t, "21.5", got["temperature"], "fPort 2 and the base64 frame came from the envelope")
	assert.Equal(t, "-60", got["lorawan.rssi"], "link metrics are appended per the integration's setting")
	assert.Equal(t, "7.5", got["lorawan.snr"])
	assert.Equal(t, "2", got["lorawan.gateways"])
	assert.EqualValues(t, 1, res.Uplinks)

	// The same body through an integration without link metrics appends none.
	req.IntegrationToken = str("chirp-quiet")
	got = readings(f.test(req))
	assert.NotContains(t, got, "lorawan.rssi")

	// What the codec refuses is reported, with the context ingest would use: no ?event=up.
	req.Query = nil
	res = f.test(req)
	require.False(t, res.OK)
	assert.Equal(t, "input_malformed", res.Failure.Reason)
	// A non-JSON content type is the network server's setting to fix.
	req.Query = []NameValue{{Name: "event", Value: "up"}}
	req.ContentType = str("application/x-protobuf")
	res = f.test(req)
	assert.Equal(t, "unsupported_encoding", res.Failure.Reason)
	// A message type that is not an uplink holds nothing to decode.
	req.ContentType = str("application/json")
	req.Query = []NameValue{{Name: "event", Value: "join"}}
	res = f.test(req)
	assert.Equal(t, "ignored_message", res.Failure.Reason)

	// A frame that is not base64 is reported, not decoded as empty.
	bad := `{"deduplicationId":"c9dbe358-2578-4fb7-b295-66b44edc45a6","deviceInfo":{"devEui":"0101010101010101"},"fPort":2,"data":"***"}`
	res = f.test(&PayloadDecoderTestRequest{WebhookBody: str(bad), IntegrationToken: str("chirp"),
		Query: []NameValue{{Name: "event", Value: "up"}}, ContentType: str("application/json")})
	require.False(t, res.OK)
	assert.Equal(t, "input_malformed", res.Failure.Reason)
	assert.Equal(t, "frame", res.Failure.Path)
}

func TestTestDecoderReadsAGenericItemAsJSONAndHonoursTheIdPointer(t *testing.T) {
	f := newDecoderFixture(t)
	f.createIntegration("hook", "GENERIC_JSON", "ALIAS:custom.sensor", str(`{"idFrom":"pointer:/sensor"}`), false)
	doc := `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[
	  {"key":"temperature","expr":"double(json.temp)"}]}]}`
	res := f.test(&PayloadDecoderTestRequest{Document: str(doc), IntegrationToken: str("hook"),
		WebhookBody: str(`{"sensor":"pump-7","temp":21.5}`), ContentType: str("application/json")})
	require.True(t, res.OK, "%+v", res.Failure)
	assert.Equal(t, "21.5", readings(res)["temperature"])

	// The id pointer is removed from the item before the decoder sees it (Cut A's rule), so a
	// decoder reading it fails rather than seeing the device id.
	leak := `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[
	  {"key":"temperature","expr":"double(json.sensor)"}]}]}`
	res = f.test(&PayloadDecoderTestRequest{Document: str(leak), IntegrationToken: str("hook"),
		WebhookBody: str(`{"sensor":"pump-7","temp":21.5}`), ContentType: str("application/json")})
	require.False(t, res.OK)
	assert.Equal(t, "eval_error", res.Failure.Reason)

	// An integration that does not exist.
	_, err := f.api.TestPayloadDecoder(f.ctx, &PayloadDecoderTestRequest{ProfileToken: "meter", Document: str(doc),
		IntegrationToken: str("nope"), WebhookBody: str(`{}`)})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// ---- A17: the sample boundary --------------------------------------------------------------

// sampleBytesProbe reads one byte of the frame out as a reading: a decoder an attacker would write.
const sampleBytesProbe = `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
  {"key":"door","expr":"u8(frame, 0)"}]}]}`

// seedSample stores a failing uplink whose first byte is the secret 0x2A for the profile.
func seedSample(t *testing.T, api *Api, ctx context.Context, profileToken string) uint {
	t.Helper()
	p, err := api.deviceProfileByToken(ctx, profileToken)
	require.NoError(t, err)
	tenant, _ := core.TenantFromContext(ctx)
	s := &ProfileDecoderSample{DeviceProfileId: p.ID, ProfileVersion: 1, DeviceToken: "dev-1", Reason: "eval_error",
		Input: []byte(`{"frameHex":"2a0102","fPort":2}`)}
	s.TenantId = tenant
	require.NoError(t, api.RDB.DB(ctx).Create(s).Error)
	return s.ID
}

func TestASampleIsEvaluatedForADeviceWriteCallerOfTheOwningTenant(t *testing.T) {
	f := newDecoderFixture(t)
	id := seedSample(t, f.api, f.ctx, "meter")
	writer := asCaller(f.ctx, nil, auth.DeviceRead, auth.DeviceWrite)

	res, err := f.api.TestPayloadDecoder(writer, &PayloadDecoderTestRequest{ProfileToken: "meter",
		Document: str(sampleBytesProbe), SampleId: &id})
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res.Failure)
	assert.Equal(t, "42", readings(res)["door"], "the sample's first byte, 0x2A")
}

// A17 negative control: the secret is not reachable with DeviceRead, however it is asked for.
func TestASampleIsNotEvaluatedForADeviceReadCaller(t *testing.T) {
	f := newDecoderFixture(t)
	id := seedSample(t, f.api, f.ctx, "meter")
	reader := asCaller(f.ctx, nil, auth.DeviceRead)

	for name, doc := range map[string]string{
		"a probe that would read a byte":   sampleBytesProbe,
		"a document that does not compile": `{"kind":"CEL"`,
		"the saved draft":                  "",
	} {
		req := &PayloadDecoderTestRequest{ProfileToken: "meter", SampleId: &id}
		if doc != "" {
			req.Document = str(doc)
		}
		res, err := f.api.TestPayloadDecoder(reader, req)
		assert.Nil(t, res, name)
		assert.ErrorIs(t, err, auth.ErrForbidden, name)
	}
	// And a caller limited to part of the tenant is refused even with DeviceWrite.
	scoped := asCaller(f.ctx, []string{"customer:acme-corp"}, auth.DeviceRead, auth.DeviceWrite)
	_, err := f.api.TestPayloadDecoder(scoped, &PayloadDecoderTestRequest{ProfileToken: "meter", Document: str(sampleBytesProbe), SampleId: &id})
	assert.ErrorIs(t, err, auth.ErrScopeUnsupported)
	// And no identity at all.
	_, err = f.api.TestPayloadDecoder(f.ctx, &PayloadDecoderTestRequest{ProfileToken: "meter", Document: str(sampleBytesProbe), SampleId: &id})
	assert.Error(t, err)
}

// A17 negative control: another tenant's sample id is not found, not forbidden and not evaluated.
func TestASampleOfAnotherTenantIsNotFound(t *testing.T) {
	f := newDecoderFixture(t)
	acmeSample := seedSample(t, f.api, f.ctx, "meter")

	other := core.WithTenant(context.Background(), "other")
	seedDeviceProfile(t, f.api, other, "meter")
	f.metric2(other, "door", "INT")
	otherWriter := asCaller(other, nil, auth.DeviceRead, auth.DeviceWrite)

	res, err := f.api.TestPayloadDecoder(otherWriter, &PayloadDecoderTestRequest{ProfileToken: "meter",
		Document: str(sampleBytesProbe), SampleId: &acmeSample})
	assert.Nil(t, res)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "tenant other reached tenant acme's sample")

	// Contrast: the other tenant's own sample is evaluated.
	own := seedSample(t, f.api, other, "meter")
	res, err = f.api.TestPayloadDecoder(otherWriter, &PayloadDecoderTestRequest{ProfileToken: "meter",
		Document: str(sampleBytesProbe), SampleId: &own})
	require.NoError(t, err)
	assert.Equal(t, "42", readings(res)["door"])
}

// A sample is also bound to ITS profile: an id of a sibling profile in the same tenant is not found.
func TestASampleOfAnotherProfileIsNotFound(t *testing.T) {
	f := newDecoderFixture(t)
	seedDeviceProfile(t, f.api, f.ctx, "sibling")
	siblingSample := seedSample(t, f.api, f.ctx, "sibling")
	writer := asCaller(f.ctx, nil, auth.DeviceRead, auth.DeviceWrite)
	_, err := f.api.TestPayloadDecoder(writer, &PayloadDecoderTestRequest{ProfileToken: "meter",
		Document: str(sampleBytesProbe), SampleId: &siblingSample})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	missing := uint(99999)
	_, err = f.api.TestPayloadDecoder(writer, &PayloadDecoderTestRequest{ProfileToken: "meter",
		Document: str(sampleBytesProbe), SampleId: &missing})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func (f *decoderFixture) metric2(ctx context.Context, key, dataType string) {
	f.t.Helper()
	_, err := f.api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
		Token: "md-" + key, DeviceProfileToken: "meter", MetricKey: key, DataType: dataType})
	require.NoError(f.t, err)
}
