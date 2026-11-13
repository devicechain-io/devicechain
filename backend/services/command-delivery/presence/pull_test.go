// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"testing"
)

type fixedReader struct {
	states map[string]State
	err    error
}

func (f fixedReader) StatesFor(context.Context, []string) (map[string]State, error) {
	return f.states, f.err
}

type fixedPull struct {
	pull map[string]bool
	err  error
}

func (f fixedPull) PullDevices(context.Context, []string) (map[string]bool, error) {
	return f.pull, f.err
}

// TestAPullDeviceIsHeldWhateverIsKnownOfIt: held when active, when inactive, when asserted, and
// when the projection has never heard of it. Each of these dispatches for any other device.
func TestAPullDeviceIsHeldWhateverIsKnownOfIt(t *testing.T) {
	for name, s := range map[string]State{
		"unknown":         {},
		"known and live":  {Known: true, Active: true, Source: mqttSource},
		"known and quiet": {Known: true, Active: false, Source: mqttSource},
		"asserted away":   {Known: true, Asserted: true, Active: false, Source: mqttSource},
		"asserted live":   {Known: true, Asserted: true, Active: true, Source: mqttSource},
	} {
		s.PullChannel = true
		if got := Decide(s, Capabilities{}); got != Hold {
			t.Errorf("%s: pull device verdict = %v, want Hold", name, got)
		}
		if got := Decide(s, Capabilities{Sparkplug: true}); got != Hold {
			t.Errorf("%s (sparkplug commands on): pull device verdict = %v, want Hold", name, got)
		}
		s.PullChannel = false
		if name == "unknown" || name == "known and live" || name == "known and quiet" || name == "asserted live" {
			if got := Decide(s, Capabilities{}); got != Dispatch {
				t.Errorf("%s: a device off the pull channel changed verdict: %v, want Dispatch", name, got)
			}
		}
	}
}

func TestWithPullChannelsMarksOnlyThePullDevices(t *testing.T) {
	inner := fixedReader{states: map[string]State{
		"a": {Known: true, Active: true, Source: mqttSource},
		"b": {Known: true, Active: true, Source: mqttSource},
	}}
	r := WithPullChannels(inner, fixedPull{pull: map[string]bool{"a": true, "c": true, "b": false}})
	got, err := r.StatesFor(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if !got["a"].PullChannel || got["b"].PullChannel {
		t.Errorf("a pull=%v b pull=%v; want true, false", got["a"].PullChannel, got["b"].PullChannel)
	}
	// c has no presence row but is a pull device: it still appears, unknown and held.
	if c, ok := got["c"]; !ok || !c.PullChannel || c.Known {
		t.Errorf("c = %+v (present %v); want a pull device the projection does not know", c, ok)
	}
	if !got["a"].Known || got["a"].Source != mqttSource {
		t.Errorf("the projection's own fields were lost: %+v", got["a"])
	}
	if Decide(got["c"], Capabilities{}) != Hold {
		t.Errorf("a pull device with no presence row must be held")
	}
}

// TestWithPullChannelsHoldsEverythingWhenItCannotAsk is the fail-closed direction, and the error
// is reported so the hold reconciler declines to release.
func TestWithPullChannelsHoldsEverythingWhenItCannotAsk(t *testing.T) {
	boom := errors.New("device-management unreachable")
	r := WithPullChannels(fixedReader{states: map[string]State{"a": {Known: true, Active: true}}}, fixedPull{err: boom})
	got, err := r.StatesFor(context.Background(), []string{"a", "b"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the resolver's", err)
	}
	for _, d := range []string{"a", "b"} {
		if Decide(got[d], Capabilities{}) != Hold {
			t.Errorf("%s: an undecided device must be held, got %v", d, Decide(got[d], Capabilities{}))
		}
	}
	if Resolved(got, "b", err) != true {
		t.Errorf("a device the read answered about (held) should count as resolved only via the map")
	}
}

// TestWithPullChannelsKeepsTheInnerError: a partial presence read still reports as one.
func TestWithPullChannelsKeepsTheInnerError(t *testing.T) {
	inner := errors.New("device-state blip")
	r := WithPullChannels(fixedReader{states: map[string]State{}, err: inner}, fixedPull{pull: map[string]bool{}})
	_, err := r.StatesFor(context.Background(), []string{"a"})
	if !errors.Is(err, inner) {
		t.Fatalf("err = %v, want the projection's", err)
	}
}

func TestWithPullChannelsLayersOverNoProjection(t *testing.T) {
	r := WithPullChannels(nil, fixedPull{pull: map[string]bool{"a": true}})
	got, err := r.StatesFor(context.Background(), []string{"a", "z"})
	if err != nil {
		t.Fatal(err)
	}
	if Decide(got["a"], Capabilities{}) != Hold || Decide(got["z"], Capabilities{}) != Dispatch {
		t.Errorf("a pull device must hold and any other must dispatch with no projection: %v %v",
			Decide(got["a"], Capabilities{}), Decide(got["z"], Capabilities{}))
	}
}

func TestWithPullChannelsWithoutAResolverIsTheInnerReader(t *testing.T) {
	inner := &fixedReader{}
	if got := WithPullChannels(inner, nil); got != Reader(inner) {
		t.Errorf("no resolver must leave the reader untouched")
	}
}
