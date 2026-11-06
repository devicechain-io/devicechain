// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// A published profile version's Snapshot is the one document in this area that is
// DURABLE, IMMUTABLE and DECODED INTO LIVE MODELS. Those three together are what makes
// it worth a guard of its own.
//
// 🔴 THE PREMISE THAT MAKES IT LOOK SAFE IS FALSE, AND IT IS WRITTEN DOWN. ProfileSnapshot
// once said the encoding "need only be self-consistent — a value round-trips because the
// same Go types read it back that wrote it." That is true within one binary and false for
// every row that outlives one. A version is frozen at publish and never rewritten, so a
// snapshot written by v0.11.0 is still being read by whatever is deployed years later —
// by THAT release's structs, not the ones that wrote it. It is the same false premise, in
// the same shape, that shipped the geofence archive without its backfill: a claim scoped
// to one situation, read as general.
//
// TestProfileSnapshotRoundTrip cannot see this. It marshals and unmarshals with the SAME
// types in the SAME binary, so it is symmetric by construction: rename a field and both
// halves rename together and it still passes. It pins that the encoding is faithful, which
// is a different and also worthwhile property. It is not this one.
//
// So pin the STORED KEYS. Two edits break a stored snapshot, and neither is loud:
//
//   - ADDING a field. Every already-published version decodes it as the zero value. That
//     is sometimes right (a nil LocationDeclaration means "declares no position", which is
//     the correct reading of a snapshot written before ADR-078) and sometimes silently
//     wrong. It is a decision, and it must be made rather than defaulted into.
//   - RENAMING a Go field. 🔴 These structs carry NO json tags, so the wire key IS the Go
//     identifier. A rename is the most ordinary refactor there is, the compiler verifies
//     every use of it, and it silently drops the field from every stored snapshot. This is
//     the only place in the area where the compiler's approval is not enough, and nothing
//     else says so.
//
// A field's TYPE changing is mostly self-announcing (sql.NullString serializes as an
// object; decoding that into a *string errors, loudly, at the point of the read), which is
// why this guard covers the key set rather than the full shape.
//
// WHEN THIS TEST FAILS, the fix is not to update the list. It is to answer: what does a
// snapshot published by an EARLIER release decode to now, and is that value defensible for
// a device pinned to that version? Then update the list, and say in the struct's comment
// what the old snapshots now mean.
func TestProfileSnapshotStoredKeysArePinned(t *testing.T) {
	// The embedded rdb/gorm mixins are part of the stored shape too, not incidental —
	// a device resolving an old version reads Token and MetricKey out of these bytes.
	wantMetric := []string{
		"CreatedAt", "DataType", "DeletedAt", "Description", "DeviceProfile",
		"DeviceProfileId", "Descriptor", "Enum", "Expression", "ID", "MaxValue", "Metadata",
		"MetricKey", "MinValue", "Name", "TenantId", "Token", "Unit", "UpdatedAt",
	}
	// Expression (ADR-082): a snapshot published before it existed carries no such key and
	// decodes to an invalid NullString — a MEASURED metric, which is exactly what every
	// metric of such a version was.
	wantCommand := []string{
		"CommandKey", "CreatedAt", "DeletedAt", "Description", "DeviceProfile",
		"DeviceProfileId", "ID", "Impact", "Metadata", "Name", "ParameterSchema", "TenantId",
		"Token", "UpdatedAt",
	}
	// AuthoringGraph is deliberately ABSENT: it is `json:"-"`, so publishing does not
	// freeze the canvas layout. Checked, not assumed — RollbackDeviceProfileVersion only
	// flips ActiveVersion, and nothing anywhere rehydrates draft rows from a snapshot, so
	// the graph lives on in the draft and losing it here costs nothing. If a restore-draft-
	// from-version path is ever added, this absence becomes data loss and this is the line
	// that should stop it.
	wantRule := []string{
		"CreatedAt", "DeletedAt", "Definition", "Description", "DeviceProfile",
		"DeviceProfileId", "Enabled", "EntityGroupToken", "EntityGroupVersion", "ID",
		"Metadata", "Name", "TenantId", "Token", "UpdatedAt",
	}
	// LocationDeclaration is the pattern the three lists above are not: a purpose-built
	// struct with explicit json tags. `omitempty` on both fields means an all-null
	// declaration stores as `{}`, so the key set is exercised with both set.
	wantLocation := []string{"expectedAccuracyMeters", "expectedUpdateIntervalSeconds"}
	// ConfigurationKey is another purpose-built, explicitly tagged struct; description is
	// omitempty, so it is set below to exercise the full key set. A snapshot published
	// without the "configuration" key reads as nothing declared, which is correct.
	wantConfiguration := []string{"description", "key", "valueType"}
	// The decoder is carried as the core library's own document, verbatim, so ITS json tags are
	// part of the stored shape: a rename there silently empties every stored decoder. Every
	// optional field is set below so the whole key set is exercised. A snapshot published
	// without the "decoder" key reads as no decoder, which is correct: such a profile's uplinks
	// were always flattened.
	wantDecoder := []string{"envVersion", "input", "kind", "let", "sections", "table", "vectors"}
	wantDecoderLet := []string{"expr", "name"}
	wantDecoderSection := []string{"ignore", "name", "outputs", "when"}
	wantDecoderOutput := []string{"key", "expr", "map", "maxKeys", "prefix", "when"}
	wantDecoderVector := []string{"expect", "input", "name"}
	wantDecoderVectorInput := []string{"decoded", "fCnt", "fPort", "frameBase64", "frameHex", "json", "link"}
	wantTop := []string{"commands", "configuration", "decoder", "location", "metrics", "rules"}

	enum := datatypes.JSON([]byte(`["LOW","HIGH"]`))
	schema := datatypes.JSON([]byte(`[{"key":"level","type":"INT"}]`))
	graph := datatypes.JSON([]byte(`{"nodes":[]}`))
	accuracy := 5.0
	interval := int32(60)

	snap := ProfileSnapshot{
		Metrics: []*MetricDefinition{{
			Model:           gorm.Model{ID: 1},
			TenantScoped:    rdb.TenantScoped{TenantId: "acme"},
			TokenReference:  rdb.TokenReference{Token: "temp"},
			DeviceProfileId: 7,
			MetricKey:       "temperature",
			DataType:        string(MetricDouble),
			Unit:            sql.NullString{String: "Cel", Valid: true},
			MinValue:        sql.NullFloat64{Float64: -40, Valid: true},
			MaxValue:        sql.NullFloat64{Float64: 120, Valid: true},
			Enum:            &enum,
			Descriptor:      sql.NullString{String: "TemperatureSensor", Valid: true},
		}},
		Commands: []*CommandDefinition{{
			Model:           gorm.Model{ID: 2},
			TenantScoped:    rdb.TenantScoped{TenantId: "acme"},
			TokenReference:  rdb.TokenReference{Token: "setpoint"},
			DeviceProfileId: 7,
			CommandKey:      "set_point",
			ParameterSchema: &schema,
		}},
		Rules: []*DetectionRule{{
			Model:           gorm.Model{ID: 3},
			TenantScoped:    rdb.TenantScoped{TenantId: "acme"},
			TokenReference:  rdb.TokenReference{Token: "overheat"},
			DeviceProfileId: 7,
			Definition:      datatypes.JSON([]byte(`{"type":"threshold"}`)),
			AuthoringGraph:  graph,
			Enabled:         true,
		}},
		Location: &LocationDeclaration{
			ExpectedAccuracyMeters:        &accuracy,
			ExpectedUpdateIntervalSeconds: &interval,
		},
		Configuration: []ConfigurationKey{{Key: "mode", ValueType: "STRING", Description: "d"}},
		Decoder:       fullDecoderDocument(t),
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the snapshot is not a JSON object: %v", err)
	}
	checkKeys(t, "the snapshot document", doc, wantTop)

	for _, list := range []struct {
		key  string
		want []string
	}{
		{"metrics", wantMetric},
		{"commands", wantCommand},
		{"rules", wantRule},
	} {
		var objs []map[string]json.RawMessage
		if err := json.Unmarshal(doc[list.key], &objs); err != nil {
			t.Fatalf("%s is not a list of objects: %v", list.key, err)
		}
		if len(objs) != 1 {
			t.Fatalf("%s: seeded 1, marshaled %d", list.key, len(objs))
		}
		checkKeys(t, "a stored "+strings.TrimSuffix(list.key, "s"), objs[0], list.want)
	}

	var loc map[string]json.RawMessage
	if err := json.Unmarshal(doc["location"], &loc); err != nil {
		t.Fatalf("location is not an object: %v", err)
	}
	checkKeys(t, "the stored location declaration", loc, wantLocation)

	var cfg []map[string]json.RawMessage
	if err := json.Unmarshal(doc["configuration"], &cfg); err != nil || len(cfg) != 1 {
		t.Fatalf("configuration is not a one-element list of objects: %v", err)
	}
	checkKeys(t, "a stored configuration key", cfg[0], wantConfiguration)

	var dec map[string]json.RawMessage
	if err := json.Unmarshal(doc["decoder"], &dec); err != nil {
		t.Fatalf("decoder is not an object: %v", err)
	}
	checkKeys(t, "the stored decoder document", dec, wantDecoder)
	first := func(what string, raw json.RawMessage) map[string]json.RawMessage {
		var objs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &objs); err != nil || len(objs) == 0 {
			t.Fatalf("%s is not a non-empty list of objects: %v", what, err)
		}
		return objs[0]
	}
	checkKeys(t, "a stored decoder let", first("let", dec["let"]), wantDecoderLet)
	section := first("sections", dec["sections"])
	checkKeys(t, "a stored decoder section", section, wantDecoderSection)
	checkKeys(t, "a stored decoder output", first("outputs", section["outputs"]), wantDecoderOutput)
	vector := first("vectors", dec["vectors"])
	checkKeys(t, "a stored decoder vector", vector, wantDecoderVector)
	var vin map[string]json.RawMessage
	if err := json.Unmarshal(vector["input"], &vin); err != nil {
		t.Fatalf("vector input is not an object: %v", err)
	}
	checkKeys(t, "a stored decoder vector input", vin, wantDecoderVectorInput)
}

// fullDecoderDocument is a decoder with every field of the document set, built from the core
// library's own types so that a field added there appears here and fails the pin until someone
// decides what an old snapshot without it means.
func fullDecoderDocument(t *testing.T) json.RawMessage {
	t.Helper()
	port, cnt := 2, 9
	raw, err := json.Marshal(payloaddecode.Document{
		Kind: payloaddecode.KindCEL, EnvVersion: payloaddecode.EnvVersion, Input: payloaddecode.InputFrame,
		Let: []payloaddecode.Let{{Name: "hdr", Expr: "u8(frame, 0)"}},
		Sections: []payloaddecode.Section{{Name: "s", When: "fPort == 2", Ignore: true, Outputs: []payloaddecode.Output{{
			Key: "k", Expr: "1", When: "true", Map: "m", Prefix: "p.", MaxKeys: 3}}}},
		Vectors: []payloaddecode.Vector{{Name: "v", Expect: map[string]json.Number{"k": "1"},
			Input: payloaddecode.VectorInput{FrameHex: str("00"), FrameBase64: str("AA=="), JSON: json.RawMessage(`{}`),
				Decoded: json.RawMessage(`{}`), FPort: &port, FCnt: &cnt, Link: map[string]float64{"rssi": 1}}}},
		Table: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal decoder: %v", err)
	}
	return raw
}

// checkKeys reports the difference as ADDED and REMOVED rather than as two sorted lists to
// eyeball, because the two mean different things to whoever reads the failure: an added key
// asks what an old snapshot without it decodes to, and a removed one says a stored value is
// no longer being read at all.
func checkKeys(t *testing.T, what string, got map[string]json.RawMessage, want []string) {
	t.Helper()
	pinned := map[string]bool{}
	for _, k := range want {
		pinned[k] = true
	}
	var added, removed []string
	for k := range got {
		if !pinned[k] {
			added = append(added, k)
		}
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	if len(added) == 0 && len(removed) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString(what + ": the STORED key set changed.\n")
	if len(added) > 0 {
		b.WriteString("  ADDED:   " + strings.Join(added, ", ") +
			"\n    Every version published before this change has no such key, so it decodes\n" +
			"    as the zero value. Is that the right reading for a device pinned to one of\n" +
			"    those versions? If not, the snapshots need repairing, not the list.\n")
	}
	if len(removed) > 0 {
		b.WriteString("  REMOVED: " + strings.Join(removed, ", ") +
			"\n    Stored snapshots still carry this key and nothing reads it any more. If this\n" +
			"    was a RENAME, the value is silently gone from every published version: these\n" +
			"    structs have no json tags, so the wire key is the Go identifier.\n")
	}
	t.Error(b.String())
}
