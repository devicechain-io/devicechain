// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package otafleet_test

import (
	"context"
	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	"github.com/devicechain-io/dc-simulator/otafleet"
	"testing"
)

func TestNamedModesOverrideIndexFaults(t *testing.T) {
	opts := otafleet.Options{InitialVersion: "2.4.0", AllowUnsigned: true, Faults: otafleet.Faults{BadHash: 1}, Modes: map[string]otafleet.Mode{"dev-a": otafleet.Healthy, "dev-b": otafleet.Offline}}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	if opts.ModeFor("dev-a", 0) != otafleet.Healthy || opts.ModeFor("dev-b", 1) != otafleet.Offline || opts.ModeFor("other", 0) != otafleet.BadHash {
		t.Fatal("named selection did not precede index selection")
	}
	b := fakemqtt.New(t)
	f, err := otafleet.New(instance, tenant, b.URL(), nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Attach(context.Background(), "dev-a", "a", 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Attach(context.Background(), "dev-b", "b", 1); err != nil {
		t.Fatal(err)
	}
	if r := f.Report(); r.Modes[otafleet.Healthy] != 1 || r.Modes[otafleet.Offline] != 1 {
		t.Fatalf("attached modes %+v", r.Modes)
	}
}

func TestNamedModesValidated(t *testing.T) {
	for token, mode := range map[string]otafleet.Mode{"device": "typo", "": otafleet.Healthy} {
		o := otafleet.Options{InitialVersion: "1", AllowUnsigned: true, Modes: map[string]otafleet.Mode{token: mode}}
		if err := o.Validate(); err == nil {
			t.Fatalf("accepted mode %q for %q", mode, token)
		}
	}
	for _, mode := range []otafleet.Mode{otafleet.Healthy, otafleet.BadHash, otafleet.BadSignature, otafleet.Interrupted, otafleet.Offline} {
		o := otafleet.Options{InitialVersion: "1", AllowUnsigned: true, Modes: map[string]otafleet.Mode{"device": mode}}
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
