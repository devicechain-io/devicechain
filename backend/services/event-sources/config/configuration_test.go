// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// Loading an empty document populates the default MQTT + HTTP sources so the
// service ingests events out of the box (ADR-022 decision 1 defaulting via
// core.LoadConfiguration). An empty source list is load-bearing.
func TestLoadDefaultsEventSources(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Len(t, cfg.EventSources, 2)

	byId := map[string]EventSource{}
	for _, src := range cfg.EventSources {
		byId[src.Id] = src
	}

	mqttSrc := byId["mqtt1"]
	assert.Equal(t, "mqtt", mqttSrc.Type)
	assert.Equal(t, "json", mqttSrc.Decoder.Type)

	httpSrc := byId["http1"]
	assert.Equal(t, "http", httpSrc.Type)
	assert.Equal(t, "json", httpSrc.Decoder.Type)
	assert.Equal(t, "8081", httpSrc.Configuration["port"])

	assert.Equal(t, 100, cfg.InboundEventBatching.MaxBatchSize)
	assert.Equal(t, 100, cfg.InboundEventBatching.BatchTimeoutMs)

	// The per-tenant ingest ceiling defaults to the platform rate, never unlimited.
	assert.Equal(t, float64(DefaultIngestMessagesPerSecond), cfg.IngestRateLimit.MessagesPerSecond)
	assert.Equal(t, DefaultIngestBurst, cfg.IngestRateLimit.Burst)
	assert.NoError(t, cfg.Validate())
}

// A non-positive ingest limit falls back to the platform default (fail-safe: an
// omitted or zeroed limit still meters every tenant, never unlimited). An
// explicit positive value is preserved.
func TestIngestRateLimitDefaulting(t *testing.T) {
	// Zeroed (omitted in the document) => platform default.
	zeroed := &EventSourcesConfiguration{}
	zeroed.ApplyDefaults()
	assert.Equal(t, float64(DefaultIngestMessagesPerSecond), zeroed.IngestRateLimit.MessagesPerSecond)
	assert.Equal(t, DefaultIngestBurst, zeroed.IngestRateLimit.Burst)

	// Explicit values survive defaulting.
	explicit := &EventSourcesConfiguration{
		IngestRateLimit: IngestRateLimit{MessagesPerSecond: 5, Burst: 10},
	}
	explicit.ApplyDefaults()
	assert.Equal(t, float64(5), explicit.IngestRateLimit.MessagesPerSecond)
	assert.Equal(t, 10, explicit.IngestRateLimit.Burst)
}

// The constructor and the load path share one source of defaults.
func TestDefaultConfigurationValid(t *testing.T) {
	cfg := NewEventSourcesConfiguration()
	assert.Len(t, cfg.EventSources, 2)
	assert.NoError(t, cfg.Validate())
}

// TestContentionManualFloorValidation pins the ADR-063 floor is a fail-closed [0,3]
// level. 0 is the intended default AND an explicit legal value — crucially it is NOT
// defaulted away, so "contention off" stays expressible; a value outside the ladder
// fails the load closed rather than being silently clamped.
func TestContentionManualFloorValidation(t *testing.T) {
	// Default (omitted) is 0 and valid — contention off, and ApplyDefaults must NOT
	// rewrite it (a "<=0 → default" clause would make an explicit 0 unreachable).
	def := &EventSourcesConfiguration{}
	def.ApplyDefaults()
	assert.Equal(t, 0, def.Contention.ManualFloor)
	assert.NoError(t, def.Validate())

	for _, level := range []int{0, 1, 2, 3} {
		cfg := NewEventSourcesConfiguration()
		cfg.Contention.ManualFloor = level
		assert.NoError(t, cfg.Validate(), "level %d is on the ladder", level)
	}
	for _, bad := range []int{-1, 4, 7} {
		cfg := NewEventSourcesConfiguration()
		cfg.Contention.ManualFloor = bad
		assert.Error(t, cfg.Validate(), "level %d is off the ladder and must fail closed", bad)
	}
}

// ── Source id validation ───────────────────────────────────────────────────
//
// 🔴 AN EventSource.Id IS A STORED KEY, NOT A LABEL. It is stamped on every event this
// source ingests as the projected `source`, lands in device_states.source, is read back by
// the asserted-presence reconciler through exact SQL equality, and is a Prometheus label
// value. These tests pin the narrow set of ids that cannot serve as that value.

// The shipped defaults must survive their own validation — a rule that rejects the
// out-of-the-box configuration is a rule nobody gets to see working.
func TestDefaultSourceIdsAreValid(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.NoError(t, cfg.Validate())

	// And the reason they survive is worth pinning: the defaults are "mqtt1"/"http1", not
	// the bare transport words. If a future default were renamed to "mqtt", it would still
	// be valid — "mqtt" is not a name the platform mints — but the count below would change.
	assert.Len(t, cfg.EventSources, 2)
}

// 🔴🔴 THE RESERVATION IS ON THE CLASSIFICATION, NOT ON THE SPELLING. Every consumer asking
// "what transport is this device on?" cuts the source at the first ":", so a bare "sparkplug"
// and a qualified "sparkplug:plant-a" are equally a Sparkplug device to all of them. Against
// the existing DENY list that collision stops the operator's own commands; against the ALLOW
// list the stranded-SENT reconciler needs, it would make a foreign device look like a member
// and be ACTED ON.
func TestValidateRejectsAnIdThatReadsAsAMintedTransport(t *testing.T) {
	for _, id := range []string{"sparkplug", "lwm2m", "sparkplug:plant-a", "lwm2m:site-a"} {
		cfg := &EventSourcesConfiguration{}
		assert.NoError(t, core.LoadConfiguration([]byte(``), cfg))
		cfg.EventSources[0].Id = id

		err := cfg.Validate()

		assert.Error(t, err, "id %q must be rejected", id)
		assert.Contains(t, err.Error(), "reserved")
	}
}

// 🔴 THE COUNTERWEIGHT, and it is the half a spelling rule would get wrong. Rejecting every
// id with a colon, or every id merely CONTAINING a transport word, would refuse ids that
// collide with nothing — and rejecting an id an operator is already running strands every
// asserted device_states row filed under it, with no backfill pre-GA.
func TestValidateAcceptsIdsThatCollideWithNothing(t *testing.T) {
	for _, id := range []string{
		"mqtt1", "http1", // the shipped defaults
		"mqtt", "http", // NOT reserved: the platform mints no such source value
		"sparkplug-test", "sparkplugin", // near-misses a prefix match would condemn
		"acme:line3", // an operator's own namespaced id — the colon is theirs to use
		"plant-a",
	} {
		cfg := &EventSourcesConfiguration{}
		assert.NoError(t, core.LoadConfiguration([]byte(``), cfg))
		// One source only: the shipped defaults are mqtt1 AND http1, so leaving both in
		// place would make "http1" trip the DUPLICATE rule and look like a reservation.
		cfg.EventSources = cfg.EventSources[:1]
		cfg.EventSources[0].Id = id

		assert.NoError(t, cfg.Validate(), "id %q collides with nothing and must be accepted", id)
	}
}

// An empty id projects an empty source, and the projection deliberately never lets an empty
// overwrite a non-empty — so the source would silently fail to update the field it exists to
// set. That is not "unset", it is permanently invisible.
func TestValidateRejectsAnEmptySourceId(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	cfg.EventSources[0].Id = ""

	err := cfg.Validate()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

// Two sources sharing an id are one source to everything downstream: one projected value,
// one merged Prometheus series, and one reconciler scope in which each re-files the other's
// asserted rows.
func TestValidateRejectsDuplicateSourceIds(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	cfg.EventSources[1].Id = cfg.EventSources[0].Id

	err := cfg.Validate()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicates")
}
