// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"gorm.io/gorm"
)

// PersistLocationEvents is the last link in the location chain that nothing read
// values through.
//
// The hops on either side are covered: model/location_values_test.go proves a
// create request round-trips to the database and back, and device-management's
// tests prove a resolved payload keeps its values through resolution and protobuf.
// This function is the JOIN between them — it parses seven strings off a resolved
// entry into a create request — and its only existing coverage builds a
// three-field fixture and asserts that persistence did not error. Dropping a field
// here would therefore lose it silently between two well-tested neighbours, which
// is exactly how the location spine came to have a value-shaped hole in the first
// place.
//
// The Api is faked rather than mocked so the create requests can be READ. The
// package's MockApi calls Mock.Called() with no arguments, so testify records
// nothing about what was passed to it — a test built on it could only assert that
// the call happened, which is the assertion that already exists and already misses
// this.
type capturingLocationApi struct {
	*emtest.MockApi
	captured []*model.LocationEventCreateRequest
	err      error
}

func (api *capturingLocationApi) CreateLocationEvents(ctx context.Context, db *gorm.DB,
	requests []*model.LocationEventCreateRequest) ([]*model.LocationEvent, error) {
	api.captured = requests
	if api.err != nil {
		return nil, api.err
	}
	return []*model.LocationEvent{}, nil
}

func persistLocations(t *testing.T, payload dmmodel.ResolvedLocationsPayload) (*capturingLocationApi, error) {
	t.Helper()
	api := &capturingLocationApi{MockApi: new(emtest.MockApi)}
	worker := &EventPersistenceWorker{WorkerId: 1, Api: api}
	_, err := worker.PersistLocationEvents(context.Background(), nil, model.Event{}, payload)
	return api, err
}

func TestPersistLocationEventsCarriesEveryFixField(t *testing.T) {
	s := func(v string) *string { return &v }

	// Distinct, non-round values throughout. Six fields all set to 1 would let a
	// mapping that wrote accuracy into speed pass, and three of these fields were
	// added at once, which is precisely when that mistake is made.
	api, err := persistLocations(t, dmmodel.ResolvedLocationsPayload{
		Entries: []dmmodel.ResolvedLocationEntry{{
			Latitude:  s("33.749"),
			Longitude: s("-84.388"),
			Elevation: s("320.5"),
			Accuracy:  s("4.2"),
			Speed:     s("1.75"),
			Heading:   s("271.5"),
		}},
	})
	if err != nil {
		t.Fatalf("persisting a well-formed fix: %v", err)
	}
	if len(api.captured) != 1 {
		t.Fatalf("got %d create requests, want 1", len(api.captured))
	}
	got := api.captured[0]
	for _, f := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"latitude", got.Latitude, 33.749},
		{"longitude", got.Longitude, -84.388},
		{"elevation", got.Elevation, 320.5},
		{"accuracy", got.Accuracy, 4.2},
		{"speed", got.Speed, 1.75},
		{"heading", got.Heading, 271.5},
	} {
		if f.got == nil {
			t.Fatalf("%s: got nil, want %v", f.name, f.want)
		}
		if *f.got != f.want {
			t.Fatalf("%s: got %v, want %v", f.name, *f.got, f.want)
		}
	}
}

// An unreported field stays nil rather than becoming zero. A location with no
// accuracy is not a location with perfect accuracy, and a nil heading is not
// north.
func TestPersistLocationEventsLeavesUnreportedFieldsNil(t *testing.T) {
	s := func(v string) *string { return &v }

	api, err := persistLocations(t, dmmodel.ResolvedLocationsPayload{
		Entries: []dmmodel.ResolvedLocationEntry{{Latitude: s("33.749"), Longitude: s("-84.388")}},
	})
	if err != nil {
		t.Fatalf("persisting a minimal fix: %v", err)
	}
	got := api.captured[0]
	for _, f := range []struct {
		name string
		got  *float64
	}{
		{"elevation", got.Elevation},
		{"accuracy", got.Accuracy},
		{"speed", got.Speed},
		{"heading", got.Heading},
	} {
		if f.got != nil {
			t.Fatalf("%s: got %v, want nil", f.name, *f.got)
		}
	}
}

// Each entry in a batch keeps its own values, in order. A loop that reuses one
// entry variable passes the single-entry test above and fails this one.
func TestPersistLocationEventsKeepsPerEntryValues(t *testing.T) {
	s := func(v string) *string { return &v }

	api, err := persistLocations(t, dmmodel.ResolvedLocationsPayload{
		Entries: []dmmodel.ResolvedLocationEntry{
			{Latitude: s("1.5"), Longitude: s("2.5"), Speed: s("10")},
			{Latitude: s("3.5"), Longitude: s("4.5"), Speed: s("20")},
		},
	})
	if err != nil {
		t.Fatalf("persisting two fixes: %v", err)
	}
	if len(api.captured) != 2 {
		t.Fatalf("got %d create requests, want 2", len(api.captured))
	}
	for i, want := range []struct{ lat, speed float64 }{{1.5, 10}, {3.5, 20}} {
		if *api.captured[i].Latitude != want.lat || *api.captured[i].Speed != want.speed {
			t.Fatalf("entry %d: got lat=%v speed=%v, want lat=%v speed=%v",
				i, *api.captured[i].Latitude, *api.captured[i].Speed, want.lat, want.speed)
		}
	}
}

// A non-numeric value in ANY of the six fields is a deterministic failure, so the
// event dead-letters on its first delivery instead of retrying to the cap.
//
// The three fields added later are the point of the table: each needs its own
// parse-and-wrap, and a field mapped straight across without one would be caught
// here and nowhere else.
func TestPersistLocationEventsRejectsANonNumericValueDeterministically(t *testing.T) {
	s := func(v string) *string { return &v }

	for _, tc := range []struct {
		name  string
		entry dmmodel.ResolvedLocationEntry
	}{
		{"latitude", dmmodel.ResolvedLocationEntry{Latitude: s("north"), Longitude: s("-84.388")}},
		{"longitude", dmmodel.ResolvedLocationEntry{Latitude: s("33.749"), Longitude: s("west")}},
		{"elevation", dmmodel.ResolvedLocationEntry{Latitude: s("33.749"), Longitude: s("-84.388"), Elevation: s("high")}},
		{"accuracy", dmmodel.ResolvedLocationEntry{Latitude: s("33.749"), Longitude: s("-84.388"), Accuracy: s("good")}},
		{"speed", dmmodel.ResolvedLocationEntry{Latitude: s("33.749"), Longitude: s("-84.388"), Speed: s("fast")}},
		{"heading", dmmodel.ResolvedLocationEntry{Latitude: s("33.749"), Longitude: s("-84.388"), Heading: s("nne")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := persistLocations(t, dmmodel.ResolvedLocationsPayload{
				Entries: []dmmodel.ResolvedLocationEntry{tc.entry},
			})
			if err == nil {
				t.Fatal("a non-numeric value must fail persistence")
			}
			if !errors.Is(err, ErrDeterministic) {
				t.Fatalf("must be deterministic so it dead-letters on first delivery, got: %v", err)
			}
		})
	}
}
