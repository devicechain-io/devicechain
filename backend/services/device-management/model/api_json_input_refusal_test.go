// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// What a malformed JSON field does to a write, proved against a real database rather than
// against the converter in isolation.
//
// 🔴 THE CONVERTER'S OWN UNIT TESTS CANNOT REACH THE DEFECT, AND THAT IS WHY THIS FILE
// EXISTS. rdb.JSONInputOf is tested in core, and its predecessor was tested there too —
// correctly, and green — while the behaviour those tests described was "returns nil for a
// malformed value". Nil is a perfectly good return value in isolation. It only becomes data
// loss one layer up, where the caller assigns it to a column that already held something and
// saves the row. The defect lived in the SEAM, so the test has to span the seam: write a
// value, send a bad one, read the row back.
//
// The measure of how invisible it was: converting all 52 call sites to the refusing form
// changed no test result anywhere in the workspace. Every one of them passed before and
// after, because not one of them sent a malformed JSON field through an API at all.

func seedTypeWithMetadata(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-a"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{
		Token:        "sensor",
		Name:         strp("Original name"),
		ProfileToken: strp("profile-a"),
		Metadata:     strp(`{"fleet":"north"}`),
	}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
}

func metadataOf(t *testing.T, api *Api, ctx context.Context) string {
	t.Helper()
	matches, err := api.DeviceTypesByToken(ctx, []string{"sensor"})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one device type, got %d", len(matches))
	}
	if matches[0].Metadata == nil {
		return "<NULL>"
	}
	return string(*matches[0].Metadata)
}

// 🔴 THE DEFECT ITSELF. This is the assertion the whole change exists to make true: a
// request carrying a malformed metadata value must not ERASE the metadata that is already
// there. Before the fix this update returned no error and left the column NULL — the caller
// was told the write succeeded and the value they never meant to touch was gone, with
// nothing anywhere to indicate it.
func TestMalformedMetadataDoesNotEraseTheStoredValue(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedTypeWithMetadata(t, api, ctx)

	_, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
		Metadata: dcgraphql.OptionalStringOf("acknowledged by livedevice"),
	})
	if err == nil {
		t.Error("a malformed metadata value was accepted; it must be refused")
	}
	if got := metadataOf(t, api, ctx); got != `{"fleet":"north"}` {
		t.Errorf("stored metadata = %s, want it untouched. A refused write must change nothing; "+
			"NULL here means the malformed value erased what was already stored", got)
	}
}

// The same shape on the FULL-REPLACE path, which is still how the other update mutations
// work. Partial-update semantics do not subsume this defect and never did — a malformed
// value is a value, so it arrives as "set", and the old converter silently turned it into
// "clear". Both paths need the refusal, so both are pinned.
func TestMalformedMetadataIsRefusedOnAFullReplaceUpdate(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedTypeWithMetadata(t, api, ctx)

	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{
		Token:           "dev-1",
		DeviceTypeToken: "sensor",
		Metadata:        strp(`{"site":"yard"}`),
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	_, err := api.UpdateDevice(ctx, "dev-1", &DeviceCreateRequest{
		Token:           "dev-1",
		DeviceTypeToken: "sensor",
		Metadata:        strp("{unclosed"),
	})
	if err == nil {
		t.Error("a malformed metadata value was accepted on the full-replace path")
	}
	found, ferr := api.DevicesByToken(ctx, []string{"dev-1"})
	if ferr != nil || len(found) != 1 {
		t.Fatalf("reload device: %v (%d matches)", ferr, len(found))
	}
	if found[0].Metadata == nil || string(*found[0].Metadata) != `{"site":"yard"}` {
		t.Errorf("stored metadata = %v, want it untouched by the refused write", found[0].Metadata)
	}
}

// A refused create must leave NO row. The refusal happens before the insert, so the failure
// is clean rather than half-applied — worth pinning separately, because a validation that
// runs after the write would satisfy the "returns an error" assertion perfectly.
func TestMalformedMetadataOnCreateWritesNoRow(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-a"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{
		Token:        "sensor",
		ProfileToken: strp("profile-a"),
		Metadata:     strp("not json at all"),
	}); err == nil {
		t.Error("a malformed metadata value was accepted on create")
	}
	matches, err := api.DeviceTypesByToken(ctx, []string{"sensor"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("a refused create left %d row(s) behind", len(matches))
	}
}

// A request can carry more than one JSON-typed field, so the error has to say WHICH one is
// wrong. Here metadata is fine and enum is not; an error naming "metadata" would send the
// caller to correct the field that was already correct.
//
// The first draft of this test used a command definition's parameterSchema and FAILED —
// because that field already had its own validator upstream ("invalid parameter schema:
// parameter schema is not valid JSON"), so it never reached the converter at all and was
// never part of this defect. Worth recording: the JSON-typed fields were not uniformly
// unguarded. Some had a real check, some had the silent one, and nothing distinguished them
// from the outside. enum is one of the silent ones.
func TestTheErrorNamesTheOffendingFieldNotJustTheFirstOne(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-a"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	_, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
		Token:              "metric-1",
		DeviceProfileToken: "profile-a",
		MetricKey:          "tempC",
		DataType:           string(MetricDouble),
		Metadata:           strp(`{"ok":true}`),
		Enum:               strp("<xml>not json</xml>"),
	})
	if err == nil {
		t.Fatal("a malformed enum was accepted")
	}
	if !strings.Contains(err.Error(), "enum") {
		t.Errorf("error %q must name enum; naming another field sends the caller to fix "+
			"something that was already right", err)
	}
}

// THE COUNTERWEIGHT, and it is not optional. Every assertion above is satisfied by a
// converter that refuses everything, which would make every JSON field on the platform
// unwritable. Valid JSON must still land, and clearing must still work.
func TestValidAndEmptyJSONStillBehaveExactlyAsBefore(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedTypeWithMetadata(t, api, ctx)

	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
		Metadata: dcgraphql.OptionalStringOf(`{"fleet":"south","rev":2}`),
	}); err != nil {
		t.Fatalf("a valid metadata value was refused: %v", err)
	}
	if got := metadataOf(t, api, ctx); got != `{"fleet":"south","rev":2}` {
		t.Errorf("stored metadata = %s, want the new value", got)
	}

	// An empty string clears, exactly as it did before this change. Refusing it would be a
	// second behaviour change riding along with the fix, hitting callers doing nothing wrong.
	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
		Metadata: dcgraphql.OptionalStringOf(""),
	}); err != nil {
		t.Fatalf("an empty metadata value was refused: %v", err)
	}
	if got := metadataOf(t, api, ctx); got != "<NULL>" {
		t.Errorf("stored metadata = %s, want it cleared by the empty value", got)
	}

	// And an OMITTED field still leaves the stored value alone — the partial-update contract
	// is unaffected by the refusal, including when the stored value is re-read and re-checked
	// on its way through.
	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
		Metadata: dcgraphql.OptionalStringOf(`{"fleet":"north"}`),
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := metadataOf(t, api, ctx); got != `{"fleet":"north"}` {
		t.Errorf("stored metadata = %s after a rename that never mentioned metadata", got)
	}
}
