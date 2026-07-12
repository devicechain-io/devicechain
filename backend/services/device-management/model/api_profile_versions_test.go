// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// A published version snapshot is Go-internal JSON: the same types write and read
// it. Resolution (metric ingest, alarm evaluation, command vocabulary) depends on
// every field a consumer reads surviving the round-trip, so pin it — a marshaler
// asymmetry here would silently corrupt what devices resolve. buildProfileSnapshot
// marshals exactly this ProfileSnapshot, so testing json.Marshal → parse covers the
// production encoding without a database.
func TestProfileSnapshotRoundTrip(t *testing.T) {
	enum := datatypes.JSON([]byte(`["LOW","HIGH"]`))
	schema := datatypes.JSON([]byte(`[{"key":"level","type":"INT"}]`))

	metric := &MetricDefinition{
		Model:           gorm.Model{ID: 42},
		TenantScoped:    rdb.TenantScoped{TenantId: "acme"},
		TokenReference:  rdb.TokenReference{Token: "temp"},
		DeviceProfileId: 7,
		MetricKey:       "temperature",
		DataType:        string(MetricDouble),
		Unit:            sql.NullString{String: "Cel", Valid: true},
		MinValue:        sql.NullFloat64{Float64: -40, Valid: true},
		MaxValue:        sql.NullFloat64{}, // deliberately unset
		Enum:            &enum,
		Descriptor:      sql.NullString{}, // deliberately unset
	}
	command := &CommandDefinition{
		Model:           gorm.Model{ID: 43},
		TokenReference:  rdb.TokenReference{Token: "setpoint"},
		DeviceProfileId: 7,
		CommandKey:      "set_point",
		ParameterSchema: &schema,
	}
	raw, err := json.Marshal(ProfileSnapshot{
		Metrics:  []*MetricDefinition{metric},
		Commands: []*CommandDefinition{command},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := parseProfileSnapshot(datatypes.JSON(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(got.Metrics) != 1 || len(got.Commands) != 1 {
		t.Fatalf("lengths = %d/%d, want 1/1", len(got.Metrics), len(got.Commands))
	}

	gm := got.Metrics[0]
	if gm.ID != 42 || gm.MetricKey != "temperature" || gm.DataType != string(MetricDouble) {
		t.Errorf("metric identity/type lost: id=%d key=%q type=%q", gm.ID, gm.MetricKey, gm.DataType)
	}
	if !gm.Unit.Valid || gm.Unit.String != "Cel" || !gm.MinValue.Valid || gm.MinValue.Float64 != -40 {
		t.Errorf("metric null-scalars lost: unit=%+v min=%+v", gm.Unit, gm.MinValue)
	}
	if gm.MaxValue.Valid || gm.Descriptor.Valid {
		t.Errorf("unset metric null-scalars became set: max=%+v desc=%+v", gm.MaxValue, gm.Descriptor)
	}
	if gm.Enum == nil || string(*gm.Enum) != `["LOW","HIGH"]` {
		t.Errorf("metric enum JSON lost: %v", gm.Enum)
	}

	gc := got.Commands[0]
	if gc.CommandKey != "set_point" || gc.ParameterSchema == nil ||
		string(*gc.ParameterSchema) != `[{"key":"level","type":"INT"}]` {
		t.Errorf("command fields lost: key=%q schema=%v", gc.CommandKey, gc.ParameterSchema)
	}
}

// parseProfileSnapshot normalizes nil/empty input to non-nil empty slices so the
// resolution loaders (and their callers) never dereference nil.
func TestParseProfileSnapshotEmpty(t *testing.T) {
	for name, raw := range map[string]datatypes.JSON{
		"nil":        nil,
		"empty":      datatypes.JSON([]byte(``)),
		"emptyLists": datatypes.JSON([]byte(`{}`)),
	} {
		got, err := parseProfileSnapshot(raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if got.Metrics == nil || got.Commands == nil {
			t.Errorf("%s: nil slice(s): %+v", name, got)
		}
		if len(got.Metrics) != 0 || len(got.Commands) != 0 {
			t.Errorf("%s: non-empty: %+v", name, got)
		}
	}
}
