// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The undeclared-position warning (ADR-078). Two properties are under test and they
// pull against each other, which is why both are here: the warning must be VISIBLE (a
// profile that never declared a location is a real gap an operator should hear about)
// and it must be BOUNDED (a 600-device fleet at 1 Hz would otherwise emit 600 warnings
// a second, which is a silent log, not a loud one).
//
// A third property outranks both: the event is STORED regardless. The declaration
// describes and displays; it never permits.

// warnBuffer captures the global zerolog output for the duration of a test. The
// resolver worker pool writes from several goroutines, so the buffer is synchronized.
type warnBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *warnBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *warnBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// undeclaredWarnings counts the undeclared-position warnings emitted so far. It matches
// on the message's distinctive phrase rather than on the level, so an unrelated warning
// from elsewhere in resolution cannot inflate the count and make a broken bound look fine.
func (b *warnBuffer) undeclaredWarnings() int {
	return strings.Count(b.String(), "reported a position its profile does not declare")
}

// captureWarnings redirects the global logger into a buffer for the duration of a
// test.
//
// 🔴 It also forces the global LEVEL, which is not belt-and-braces: another suite in
// this package sets zerolog's global level to Disabled and (until this slice) never
// restored it, so every test that ran after it saw an empty buffer. That is the
// worst possible failure mode for a test that counts log lines — "the warning fired
// zero times" is exactly what a correct bound looks like, so a muted logger reads as
// a PASS on every assertion of the form "must not warn", and only the assertions that
// demand a warning notice anything is wrong. Setting the level here makes the
// instrument's state a property of this test rather than of whichever test ran before
// it.
func captureWarnings(t *testing.T) *warnBuffer {
	t.Helper()
	buf := &warnBuffer{}
	prevLogger, prevLevel := log.Logger, zerolog.GlobalLevel()
	log.Logger = zerolog.New(buf)
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() {
		log.Logger = prevLogger
		zerolog.SetGlobalLevel(prevLevel)
	})
	return buf
}

// locationFixEvent is one position report, carrying real coordinates so a test can
// assert the fix SURVIVED rather than merely that an envelope did.
func locationFixEvent(deviceToken string) *esmodel.UnresolvedEvent {
	lat, lon := "33.749", "-84.388"
	return &esmodel.UnresolvedEvent{
		Source:    "test-source",
		Device:    deviceToken,
		EventType: esmodel.Location,
		Payload: &esmodel.UnresolvedLocationsPayload{
			Entries: []esmodel.UnresolvedLocationEntry{{Latitude: &lat, Longitude: &lon}},
		},
	}
}

// locationTestApi is a mock whose profile scope names a published version and whose
// location declaration is undeclared by default — the state the warning fires on.
func locationTestApi(versionToken string) *dmtest.MockApi {
	api := new(dmtest.MockApi)
	api.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)
	api.ProfileScopeResult = &dmodel.ProfileScope{DeviceTypeToken: "tracker-type", ProfileVersionToken: versionToken}
	return api
}

func locationTestDevice() *dmodel.Device {
	device := &dmodel.Device{
		Model:          gorm.Model{ID: 1},
		TokenReference: rdb.TokenReference{Token: "TRACKER-1"},
	}
	device.DeviceTypeId = 77
	return device
}

func locationTestCtx() context.Context {
	return core.WithTenant(context.Background(), "acme")
}

// 🔴 The line nobody may later "tighten". A device whose profile does not declare a
// location still has its fix RESOLVED AND STORED, in full, with its coordinates intact.
// The declaration governs description and display; it is not a permission, and ingest
// does not consult it to decide whether data lives.
func TestUndeclaredLocationEventIsStoredNotRejected(t *testing.T) {
	api := locationTestApi("tracker-profile@1")
	api.LocationDeclarationResult = nil // undeclared
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())

	results, reason, err := resolver.HandleStandardEvent(locationTestCtx(), locationTestDevice(), locationFixEvent("TRACKER-1"))

	require.NoError(t, err, "an undeclared location must not fail resolution")
	assert.Equal(t, uint(0), reason, "an undeclared location must not carry a failure reason")
	require.Len(t, results, 1, "an undeclared location must produce a resolved event, not be dropped")

	payload, ok := results[0].Resolved.Payload.(*dmodel.ResolvedLocationsPayload)
	require.True(t, ok, "expected a resolved locations payload, got %T", results[0].Resolved.Payload)
	require.Len(t, payload.Entries, 1, "the fix was filtered out of an event that was otherwise kept")
	require.NotNil(t, payload.Entries[0].Latitude, "the coordinates were stripped from a stored fix")
	assert.Equal(t, "33.749", *payload.Entries[0].Latitude)
	require.NotNil(t, payload.Entries[0].Longitude)
	assert.Equal(t, "-84.388", *payload.Entries[0].Longitude)
}

// The warning is bounded to ONE line per (device, profile version), not one per event.
// Fifty fixes is a few seconds of a single 1 Hz tracker; the untenable version of this
// feature emits fifty lines.
func TestUndeclaredLocationWarnsOncePerDeviceAndVersion(t *testing.T) {
	logs := captureWarnings(t)
	api := locationTestApi("tracker-profile@1")
	api.LocationDeclarationResult = nil
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())
	ctx := locationTestCtx()

	for i := 0; i < 50; i++ {
		_, _, err := resolver.HandleStandardEvent(ctx, locationTestDevice(), locationFixEvent("TRACKER-1"))
		require.NoError(t, err)
	}

	assert.Equal(t, 1, logs.undeclaredWarnings(),
		"the warning must fire once per (device, profile version), not once per event")
	// And the memo spares the hot path the LOOKUP too, not just the log line: a
	// per-event query would be a database read added by a logging feature.
	assert.Equal(t, 1, api.LocationDeclarationCalls,
		"the declaration was re-read per event; the memo is bounding the log but not the query")
}

// Re-publishing the profile is exactly the moment an operator wants to be told again —
// they just edited the thing the warning is about, and a warning muted forever by the
// first event a device ever sent could never report that the edit did not fix it.
func TestUndeclaredLocationWarnsAgainAfterRepublish(t *testing.T) {
	logs := captureWarnings(t)
	api := locationTestApi("tracker-profile@1")
	api.LocationDeclarationResult = nil
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())
	ctx := locationTestCtx()

	for i := 0; i < 5; i++ {
		_, _, err := resolver.HandleStandardEvent(ctx, locationTestDevice(), locationFixEvent("TRACKER-1"))
		require.NoError(t, err)
	}
	require.Equal(t, 1, logs.undeclaredWarnings(), "precondition: the first version warns exactly once")

	// The profile is re-published — still without a location declaration. Every device
	// event now carries the new version token.
	api.ProfileScopeResult = &dmodel.ProfileScope{DeviceTypeToken: "tracker-type", ProfileVersionToken: "tracker-profile@2"}
	for i := 0; i < 5; i++ {
		_, _, err := resolver.HandleStandardEvent(ctx, locationTestDevice(), locationFixEvent("TRACKER-1"))
		require.NoError(t, err)
	}

	assert.Equal(t, 2, logs.undeclaredWarnings(),
		"a re-published profile must warn again — the version is part of the key precisely so it does")
}

// A profile that DOES declare a location is silent. This is the counterweight to the
// tests above: a warning that fires for everyone is not a signal.
func TestDeclaredLocationNeverWarns(t *testing.T) {
	logs := captureWarnings(t)
	api := locationTestApi("tracker-profile@1")
	accuracy := 2.5
	api.LocationDeclarationResult = &dmodel.LocationDeclaration{ExpectedAccuracyMeters: &accuracy}
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())
	ctx := locationTestCtx()

	for i := 0; i < 20; i++ {
		_, _, err := resolver.HandleStandardEvent(ctx, locationTestDevice(), locationFixEvent("TRACKER-1"))
		require.NoError(t, err)
	}

	assert.Equal(t, 0, logs.undeclaredWarnings(), "a declared location must produce no warning at all")
}

// A declaration that states NO expectations still declares a position, so it is silent
// too. This is the ingest-side face of the nil-versus-empty distinction: if the resolver
// tested emptiness rather than presence, this would warn.
func TestDeclaredButEmptyLocationNeverWarns(t *testing.T) {
	logs := captureWarnings(t)
	api := locationTestApi("tracker-profile@1")
	api.LocationDeclarationResult = &dmodel.LocationDeclaration{}
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())
	ctx := locationTestCtx()

	for i := 0; i < 10; i++ {
		_, _, err := resolver.HandleStandardEvent(ctx, locationTestDevice(), locationFixEvent("TRACKER-1"))
		require.NoError(t, err)
	}

	assert.Equal(t, 0, logs.undeclaredWarnings(),
		"a declared-but-unconfigured location is still a declaration — warning here would mean the check reads field values instead of presence")
}

// The warning keys on the DEVICE, so two devices sharing a profile are two separate
// reports. A per-profile bound would mute every device but the first — the operator
// would fix one and never learn about the rest.
func TestUndeclaredLocationWarnsPerDevice(t *testing.T) {
	logs := captureWarnings(t)
	api := locationTestApi("tracker-profile@1")
	api.LocationDeclarationResult = nil
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())
	ctx := locationTestCtx()

	for _, token := range []string{"TRACKER-1", "TRACKER-2", "TRACKER-3"} {
		device := locationTestDevice()
		device.Token = token
		for i := 0; i < 4; i++ {
			_, _, err := resolver.HandleStandardEvent(ctx, device, locationFixEvent(token))
			require.NoError(t, err)
		}
	}

	assert.Equal(t, 3, logs.undeclaredWarnings(), "each device with an undeclared position gets its own single warning")
}

// A measurement event must not trigger the check at all — it is a location-only
// concern, and running the lookup for every measurement would put a query on the
// busiest path in the system.
func TestNonLocationEventDoesNotConsultTheDeclaration(t *testing.T) {
	logs := captureWarnings(t)
	api := locationTestApi("tracker-profile@1")
	api.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	api.LocationDeclarationResult = nil
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())

	measurement := &esmodel.UnresolvedEvent{
		Source:    "test-source",
		Device:    "TRACKER-1",
		EventType: esmodel.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: map[string]string{"temp": "42"}}},
		},
	}
	_, _, err := resolver.HandleStandardEvent(locationTestCtx(), locationTestDevice(), measurement)

	require.NoError(t, err)
	assert.Equal(t, 0, api.LocationDeclarationCalls, "a measurement must not consult the location declaration")
	assert.Equal(t, 0, logs.undeclaredWarnings())
}

// A declaration lookup that FAILS must degrade to silence and leave the event
// untouched — the check is an observer, and an observer that cannot observe has no
// business failing an event or claiming the warn slot.
func TestDeclarationLookupFailureLeavesTheEventAlone(t *testing.T) {
	logs := captureWarnings(t)
	api := locationTestApi("tracker-profile@1")
	api.LocationDeclarationErr = errors.New("declaration lookup unavailable")
	resolver := NewEventResolver(1, api, config.AuthModeOptional, nil, nil, nil, nil, nil, newUndeclaredLocationMemo())

	results, reason, err := resolver.HandleStandardEvent(locationTestCtx(), locationTestDevice(), locationFixEvent("TRACKER-1"))

	require.NoError(t, err, "a failed description lookup must not fail the event")
	assert.Equal(t, uint(0), reason)
	require.Len(t, results, 1)
	assert.Equal(t, 0, logs.undeclaredWarnings(), "an unanswerable check must not claim to have found a gap")
}

// The memos are bounded. Devices are unbounded over time, so a plain map keyed by
// device token is a slow leak in a process meant to run for months; the cost of the
// bound is a repeated warning after eviction, never a missed one.
func TestLocationMemosAreBounded(t *testing.T) {
	memo := newUndeclaredLocationMemo()
	for i := 0; i < undeclaredLocationWarnCapacity*2; i++ {
		memo.claimWarning("acme", "device-"+string(rune('a'+i%26))+string(rune(i)), "prof@1")
		memo.rememberDeclared(uint(i), "prof@1", false)
	}
	assert.LessOrEqual(t, memo.warned.len(), undeclaredLocationWarnCapacity,
		"the warn memo grew past its capacity — that is an unbounded map wearing an LRU's name")
	assert.LessOrEqual(t, memo.answers.len(), undeclaredLocationAnswerCapacity,
		"the declaration-answer cache grew past its capacity")
}

// claimWarning is a claim, not a check: exactly one caller wins for a given key even
// when the whole resolver pool races on the same device.
func TestClaimWarningIsExclusiveUnderConcurrency(t *testing.T) {
	memo := newUndeclaredLocationMemo()
	const racers = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if memo.claimWarning("acme", "TRACKER-1", "prof@1") {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, wins, "more than one caller claimed the same warning — the check and the insert are not atomic")
}

// Two tenants can each have a device called "gateway-1". Muting one because the other
// was already reported would hide a real gap in someone else's fleet.
func TestWarnKeyIsTenantScoped(t *testing.T) {
	memo := newUndeclaredLocationMemo()
	assert.True(t, memo.claimWarning("acme", "gateway-1", "prof@1"))
	assert.True(t, memo.claimWarning("globex", "gateway-1", "prof@1"),
		"a second tenant's identically-named device must get its own warning")
	assert.False(t, memo.claimWarning("acme", "gateway-1", "prof@1"))
}
