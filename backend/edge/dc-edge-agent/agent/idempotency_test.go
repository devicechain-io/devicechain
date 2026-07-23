// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// storedAt is a fixed store-time used across the stamping tests; the value doesn't
// matter, only that the stamp reproduces it.
var storedAt = time.Date(2026, 7, 22, 10, 30, 0, 123456789, time.UTC)

// parseStamped is a small helper: unmarshal the forwarded bytes and return the two
// idempotency fields as (altId, occurredTime, restOfObject).
func parseStamped(t *testing.T, b []byte) (altId, occurred string, obj map[string]json.RawMessage) {
	t.Helper()
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatalf("stamped output is not a JSON object: %v (%s)", err, b)
	}
	if raw, ok := obj["altId"]; ok {
		_ = json.Unmarshal(raw, &altId)
	}
	if raw, ok := obj["occurredTime"]; ok {
		_ = json.Unmarshal(raw, &occurred)
	}
	return altId, occurred, obj
}

// TestStampMintsWhenAbsent: a device that omits both keys gets both minted, the altId
// in the edge:<installId>:<epoch>:<seq> shape and the occurredTime = the stream store
// time. The rest of the payload is preserved.
func TestStampMintsWhenAbsent(t *testing.T) {
	in := []byte(`{"eventType":"measurement","values":{"temp":21.5}}`)
	out, err := stampIdempotency(in, "install-uuid", "1721642400000000000", 7, storedAt)
	if err != nil {
		t.Fatalf("stampIdempotency: %v", err)
	}
	altId, occurred, obj := parseStamped(t, out)

	if want := "edge:install-uuid:1721642400000000000:7"; altId != want {
		t.Errorf("altId = %q, want %q", altId, want)
	}
	// occurredTime must round-trip to the exact store instant (RFC3339Nano).
	got, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatalf("occurredTime %q not RFC3339Nano: %v", occurred, err)
	}
	if !got.Equal(storedAt) {
		t.Errorf("occurredTime = %s, want %s", got, storedAt)
	}
	// The device's own field must survive untouched.
	if _, ok := obj["values"]; !ok {
		t.Error("device payload field 'values' was dropped by stamping")
	}
}

// TestStampTreatsNullAsAbsent (MAJOR-1): a device emitting explicit null for the
// optional fields must be minted over, not skipped — else the cloud re-randomises the
// key with time.Now() on every redelivery and the partial index never applies.
func TestStampTreatsNullAsAbsent(t *testing.T) {
	in := []byte(`{"altId":null,"occurredTime":null,"eventType":"measurement"}`)
	out, err := stampIdempotency(in, "iid", "42", 3, storedAt)
	if err != nil {
		t.Fatalf("stampIdempotency: %v", err)
	}
	altId, occurred, _ := parseStamped(t, out)
	if altId != "edge:iid:42:3" {
		t.Errorf("altId = %q, want minted (null was treated as device-set)", altId)
	}
	if occurred == "" {
		t.Error("occurredTime not stamped (null was treated as device-set)")
	}
}

// TestStampPreservesDeviceSetValues: a device that supplies its own altId and
// occurredTime keeps BOTH — the device's idempotency intent wins — and the bytes are
// returned verbatim (no needless re-marshal).
func TestStampPreservesDeviceSetValues(t *testing.T) {
	in := []byte(`{"altId":"dev-key-1","occurredTime":"2026-01-02T03:04:05Z","eventType":"x"}`)
	out, err := stampIdempotency(in, "iid", "42", 3, storedAt)
	if err != nil {
		t.Fatalf("stampIdempotency: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Errorf("device-set payload was modified:\n in = %s\nout = %s", in, out)
	}
}

// TestStampStampsOccurredEvenWhenAltIdSet: the two carve-outs are independent — a
// device-set altId with NO occurredTime still needs occurredTime stamped, or the
// cloud's time.Now() default re-randomises the key despite the stable altId.
func TestStampStampsOccurredEvenWhenAltIdSet(t *testing.T) {
	in := []byte(`{"altId":"dev-key-1","eventType":"x"}`)
	out, err := stampIdempotency(in, "iid", "42", 3, storedAt)
	if err != nil {
		t.Fatalf("stampIdempotency: %v", err)
	}
	altId, occurred, _ := parseStamped(t, out)
	if altId != "dev-key-1" {
		t.Errorf("device altId = %q, want preserved", altId)
	}
	if occurred == "" {
		t.Error("occurredTime not stamped for a device-set-altId/omitted-occurredTime event")
	}
}

// TestStampMintsAltIdEvenWhenOccurredSet: the mirror case — device-set occurredTime,
// omitted altId — mints altId while preserving the device's occurredTime.
func TestStampMintsAltIdEvenWhenOccurredSet(t *testing.T) {
	in := []byte(`{"occurredTime":"2026-01-02T03:04:05Z","eventType":"x"}`)
	out, err := stampIdempotency(in, "iid", "42", 9, storedAt)
	if err != nil {
		t.Fatalf("stampIdempotency: %v", err)
	}
	altId, occurred, _ := parseStamped(t, out)
	if altId != "edge:iid:42:9" {
		t.Errorf("altId = %q, want minted", altId)
	}
	if occurred != "2026-01-02T03:04:05Z" {
		t.Errorf("occurredTime = %q, want the device's value preserved", occurred)
	}
}

// TestStampForwardsNonObjectVerbatim: exactly-once is a JSON-object property. A payload
// that is not a JSON object (array, scalar, or non-JSON binary) is forwarded verbatim.
func TestStampForwardsNonObjectVerbatim(t *testing.T) {
	for _, in := range [][]byte{
		[]byte(`[1,2,3]`),
		[]byte(`"just a string"`),
		[]byte(`42`),
		[]byte("\x00\x01not json at all"),
		[]byte(``),
		// A literal JSON null unmarshals into a nil map with NO error — the one
		// non-object input that must not panic the drain (a 4-byte poison pill).
		[]byte(`null`),
		[]byte(`  null  `),
	} {
		out, err := stampIdempotency(in, "iid", "42", 3, storedAt)
		if err != nil {
			t.Fatalf("stampIdempotency(%q): %v", in, err)
		}
		if !bytes.Equal(in, out) {
			t.Errorf("non-object payload %q was modified to %q", in, out)
		}
	}
}

// TestStampIsReplayStable: the same stored-message inputs (seq + store-time) always
// produce the byte-identical stamp. This is the property the whole exactly-once scheme
// rides on — a redelivery must re-derive the same key. Guards against any accidental
// use of a per-call clock/random.
func TestStampIsReplayStable(t *testing.T) {
	in := []byte(`{"eventType":"measurement"}`)
	a, err := stampIdempotency(in, "iid", "42", 5, storedAt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := stampIdempotency(in, "iid", "42", 5, storedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("stamp not replay-stable:\n a = %s\n b = %s", a, b)
	}
}
