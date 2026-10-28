// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
)

// ---- Command completion ------------------------------------------------------------------------
//
// The device side of the two-way contract answers a command when its JOB is done, not when it
// arrives. These tests drive the command engine against the model second by second, because the
// property under test is a relation between two clocks: when the platform was told SUCCESSFUL, and
// when the zone actually reached its setpoint.

type tpAnswer struct {
	token string
	ok    bool
	at    float64
}

type tpRig struct {
	t       *testing.T
	model   *towerpulseModel
	cmds    *towerpulseCommands
	mu      sync.Mutex
	answers []tpAnswer
}

func newTPRig(t *testing.T) *tpRig {
	t.Helper()
	model := newTowerpulseModel(buildTowerpulseLayout())
	cmds := newTowerpulseCommands(model)
	// Answers run inline so a test reads the outcome straight after the step that settled it.
	cmds.dispatch = func(f func()) { f() }
	return &tpRig{t: t, model: model, cmds: cmds}
}

func (r *tpRig) now() float64 {
	r.model.mu.Lock()
	defer r.model.mu.Unlock()
	return r.model.simT
}

func (r *tpRig) send(device, token, key string, payload any) {
	r.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		r.t.Fatal(err)
	}
	r.cmds.receive(device, token, key, raw, func(ok bool) {
		at := r.now()
		r.mu.Lock()
		r.answers = append(r.answers, tpAnswer{token, ok, at})
		r.mu.Unlock()
	})
}

// step advances the model to `to` in increments of `every`, settling after each as Tick does.
func (r *tpRig) step(to, every float64, each func(now float64)) {
	for now := r.now() + every; now <= to; now += every {
		r.model.advanceTo(now)
		r.cmds.settle()
		if each != nil {
			each(now)
		}
	}
}

func (r *tpRig) answered(token string) (tpAnswer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.answers {
		if a.token == token {
			return a, true
		}
	}
	return tpAnswer{}, false
}

func (r *tpRig) count(token string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.answers {
		if a.token == token {
			n++
		}
	}
	return n
}

func (r *tpRig) zone(token string) *towerpulseZone {
	return r.model.byToken[token].(*towerpulseZone)
}

// reached reports whether the zone's latest boost has brought it to its setpoint.
func (r *tpRig) reached(token string) bool {
	r.model.mu.Lock()
	defer r.model.mu.Unlock()
	z := r.zone(token)
	return z.boostSeq > 0 && z.reachedSeq == z.boostSeq
}

func (r *tpRig) temp(token string) (temp, setpoint float64) {
	r.model.mu.Lock()
	defer r.model.mu.Unlock()
	z := r.zone(token)
	return z.temp, z.setpoint
}

// hotAt is the second R1 raises on the meeting room, read off the published rule.
func hotAt(t *testing.T) float64 {
	t.Helper()
	series, _ := tpRun(t, nil, 900, 5)
	at := tpRaisedAt(tpEval(t, tpRules(t)[TowerpulseZoneHotRuleToken].Spec, series[TowerpulseHotZone], 900))
	if at < 0 {
		t.Fatal("R1 never raised on the meeting room, so there is no hot room to boost")
	}
	return at
}

// THE HEADLINE. boost-cooling is SUCCESSFUL only once the zone reaches its setpoint: not on
// arrival, and not while the room is still warm. Checked at the production cadence (a tick every
// five seconds), at every tick: the platform is told SUCCESSFUL at exactly the tick the zone
// reached its setpoint and never before.
func TestBoostCoolingIsSuccessfulOnlyOnceTheZoneReachesItsSetpoint(t *testing.T) {
	fire := hotAt(t)
	r := newTPRig(t)
	r.step(fire, 5, nil)

	if temp, sp := r.temp(TowerpulseHotZone); temp < sp+2 {
		t.Fatalf("the room is at %.2f C against a %.1f C setpoint when the rule raises; there is nothing to cool", temp, sp)
	}
	r.send(TowerpulseHotZone, "boost-1", "boost-cooling", map[string]any{"minutes": 15})
	if _, done := r.answered("boost-1"); done {
		t.Fatal("the command was answered on arrival, before any cooling happened")
	}
	if r.cmds.pendingCount() != 1 {
		t.Fatalf("%d commands pending, want the boost", r.cmds.pendingCount())
	}

	var doneAt float64
	r.step(fire+15*60, 5, func(now float64) {
		a, done := r.answered("boost-1")
		reached := r.reached(TowerpulseHotZone)
		if done != reached {
			t.Fatalf("at %v s the command answered=%v but the zone reached its setpoint=%v: the answer must come with the cooling, no sooner and no later", now, done, reached)
		}
		if done && doneAt == 0 {
			doneAt = a.at
			if !a.ok {
				t.Fatalf("the boost reached its setpoint and was answered FAILED")
			}
		}
	})
	if doneAt == 0 {
		t.Fatal("the boost never completed")
	}
	// It took the room real minutes to cool: a boost reported at once would be the false success.
	if took := doneAt - fire; took < 240 {
		t.Errorf("the boost completed %v s after it was sent; cooling several degrees takes minutes", took)
	}
	if r.cmds.pendingCount() != 0 {
		t.Error("the settled command is still pending")
	}
	if r.count("boost-1") != 1 {
		t.Errorf("the platform was answered %d times, want once", r.count("boost-1"))
	}
}

// A boost issued while the meeting is still on reaches the setpoint and then the room warms again
// within seconds, because the boost has ended and the people are still there. The command still
// completes SUCCESSFULLY: the setpoint WAS reached, and the answer reads that fact rather than the
// temperature at some later tick.
func TestABoostThatReachedItsSetpointStaysSuccessfulWhenTheRoomWarmsAgain(t *testing.T) {
	r := newTPRig(t)
	r.step(300, 5, nil)
	r.send(TowerpulseHotZone, "boost-early", "boost-cooling", map[string]any{"minutes": 60})
	r.step(900, 5, nil)
	a, done := r.answered("boost-early")
	if !done || !a.ok {
		t.Fatalf("answer = %+v done=%v, want SUCCESSFUL: the room did reach its setpoint during the meeting", a, done)
	}
	if a.at >= towerpulseMeetingEndSecond {
		t.Fatalf("answered at %v s, after the meeting ended; the test no longer covers the room warming again", a.at)
	}
	if temp, sp := r.temp(TowerpulseHotZone); temp <= sp+towerpulseAtSetpoint {
		t.Fatalf("the room is at %.2f C at 900 s; it was meant to be warming again by now, otherwise this is not the case under test", temp)
	}
}

// A boost that runs out of time before the room is cool is FAILED, at the moment it runs out: the
// device does not claim a success it did not achieve, and does not leave the platform waiting.
func TestABoostThatRunsOutOfTimeFails(t *testing.T) {
	fire := hotAt(t)
	r := newTPRig(t)
	r.step(fire, 5, nil)
	r.send(TowerpulseHotZone, "boost-short", "boost-cooling", map[string]any{"minutes": 1})

	r.step(fire+55, 5, nil)
	if _, done := r.answered("boost-short"); done {
		t.Fatal("answered before the minute was up")
	}
	r.step(fire+70, 5, nil)
	a, done := r.answered("boost-short")
	if !done {
		t.Fatal("a boost that has run out of time was never answered")
	}
	if a.ok {
		t.Error("a one-minute boost cannot cool a room by five degrees, and was answered SUCCESSFUL")
	}
	if a.at < fire+60 {
		t.Errorf("failed at %v s, before its minute was up (sent at %v s)", a.at, fire)
	}
	if temp, sp := r.temp(TowerpulseHotZone); temp <= sp+towerpulseAtSetpoint {
		t.Errorf("the room is at %.2f C, at its setpoint, so the failure is not the one under test", temp)
	}
}

// The setpoint takes effect at once and the reading follows; an out-of-range value is refused.
func TestSetSetpointAppliesAtOnceAndRefusesOutOfRange(t *testing.T) {
	r := newTPRig(t)
	const zone = "tp-vav-tower-f1-north"
	r.send(zone, "sp-1", "set-setpoint", map[string]any{"celsius": 21.0})
	if a, done := r.answered("sp-1"); !done || !a.ok {
		t.Fatalf("answer = %+v done=%v, want an immediate SUCCESS", a, done)
	}
	if got := r.model.reading(zone)[TowerpulseSetpointKey]; got != 21.0 {
		t.Errorf("the zone reports setpoint %v after being set to 21", got)
	}
	for i, bad := range []any{map[string]any{"celsius": 3.0}, map[string]any{"celsius": 90.0}, map[string]any{"celsius": "warm"}, map[string]any{}} {
		token := "sp-bad-" + string(rune('a'+i))
		r.send(zone, token, "set-setpoint", bad)
		if a, done := r.answered(token); !done || a.ok {
			t.Errorf("payload %v: answer = %+v done=%v, want an immediate FAILURE", bad, a, done)
		}
	}
	if got := r.model.reading(zone)[TowerpulseSetpointKey]; got != 21.0 {
		t.Errorf("a refused command changed the setpoint to %v", got)
	}
}

// A command the device cannot do is answered FAILED at once, never left pending and never
// answered as a success.
func TestACommandTheDeviceCannotDoIsFailedAtOnce(t *testing.T) {
	r := newTPRig(t)
	cases := []struct{ device, key string }{
		{"tp-chiller-tower", "boost-cooling"},
		{"tp-vav-tower-f1-north", "restart"},
		{"tp-vav-tower-f1-north", "no-such-command"},
		{"tp-meter-tower", "restart"},
		{"tp-no-such-device", "restart"},
	}
	for i, c := range cases {
		token := "bad-" + string(rune('a'+i))
		r.send(c.device, token, c.key, map[string]any{"minutes": 5})
		if a, done := r.answered(token); !done || a.ok {
			t.Errorf("%s %s: answer = %+v done=%v, want FAILED", c.device, c.key, a, done)
		}
	}
	for i, bad := range []any{map[string]any{"minutes": 0}, map[string]any{"minutes": -3}, map[string]any{}, "not an object"} {
		token := "badboost-" + string(rune('a'+i))
		r.send(TowerpulseHotZone, token, "boost-cooling", bad)
		if a, done := r.answered(token); !done || a.ok {
			t.Errorf("boost payload %v: answer = %+v done=%v, want FAILED", bad, a, done)
		}
	}
	if r.cmds.pendingCount() != 0 {
		t.Errorf("%d refused commands are pending", r.cmds.pendingCount())
	}
}

// The platform delivers at least once. A redelivery of a command still being worked is ignored
// (the answer comes when the work is done), and one of a command already answered is answered
// again with the SAME outcome, because the first answer may be the thing that was lost.
func TestARedeliveredCommandIsNotWorkedTwiceAndIsAnsweredTheSameWay(t *testing.T) {
	fire := hotAt(t)
	r := newTPRig(t)
	r.step(fire, 5, nil)
	r.send(TowerpulseHotZone, "boost-r", "boost-cooling", map[string]any{"minutes": 15})
	r.send(TowerpulseHotZone, "boost-r", "boost-cooling", map[string]any{"minutes": 15})
	if r.cmds.pendingCount() != 1 {
		t.Fatalf("a redelivery of a pending command created %d pending commands", r.cmds.pendingCount())
	}
	if seq := r.zone(TowerpulseHotZone).boostSeq; seq != 1 {
		t.Errorf("the zone was handed %d boosts for one command", seq)
	}
	r.step(fire+15*60, 5, nil)
	if r.count("boost-r") != 1 {
		t.Fatalf("answered %d times before any redelivery, want 1", r.count("boost-r"))
	}
	r.send(TowerpulseHotZone, "boost-r", "boost-cooling", map[string]any{"minutes": 15})
	if r.count("boost-r") != 2 {
		t.Errorf("a redelivery of a settled command was answered %d times in total, want 2", r.count("boost-r"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answers[0].ok != r.answers[1].ok {
		t.Error("the redelivery was answered differently from the first answer")
	}
}

// restart brings a silent chiller back. It is answered SUCCESSFUL only once the chiller is up and
// reporting again, which takes time, and the script does not knock it over a second time.
func TestRestartBringsADroppedChillerBack(t *testing.T) {
	r := newTPRig(t)
	r.step(towerpulseChillerDropSecond+30, 5, nil)
	if r.model.reading(TowerpulseDropChiller) != nil {
		t.Fatal("the annex chiller is still reporting after its drop")
	}
	sentAt := r.now()
	r.send(TowerpulseDropChiller, "restart-1", "restart", map[string]any{})
	r.step(sentAt+towerpulseRestartSeconds-10, 1, nil)
	if _, done := r.answered("restart-1"); done {
		t.Fatal("the restart was answered before the chiller was back")
	}
	if r.model.reading(TowerpulseDropChiller) != nil {
		t.Fatal("the chiller reported before its restart completed")
	}
	r.step(sentAt+towerpulseRestartSeconds+10, 1, nil)
	if a, done := r.answered("restart-1"); !done || !a.ok {
		t.Fatalf("answer = %+v done=%v, want SUCCESSFUL once the chiller is back", a, done)
	}
	if r.model.reading(TowerpulseDropChiller) == nil {
		t.Error("the restart was answered SUCCESSFUL but the chiller is still silent")
	}
	r.step(r.now()+600, 5, nil)
	if r.model.reading(TowerpulseDropChiller) == nil {
		t.Error("the script dropped the chiller again after it was restarted")
	}
}

// Restarting a healthy chiller rides through: its telemetry never stops, and the command still
// takes its time.
func TestRestartOfAHealthyChillerRidesThrough(t *testing.T) {
	r := newTPRig(t)
	r.step(100, 5, nil)
	sentAt := r.now()
	r.send("tp-chiller-tower", "restart-ok", "restart", map[string]any{})
	r.step(sentAt+towerpulseRideThroughRestart+5, 1, func(now float64) {
		if r.model.reading("tp-chiller-tower") == nil {
			t.Fatalf("a healthy chiller went silent at %v s during a restart", now)
		}
	})
	a, done := r.answered("restart-ok")
	if !done || !a.ok || a.at < sentAt+towerpulseRideThroughRestart {
		t.Errorf("answer = %+v done=%v, want SUCCESSFUL no sooner than %v s after it was sent", a, done, towerpulseRideThroughRestart)
	}
}

func TestSetModeChangesTheAirHandlerAndRefusesAnUnknownMode(t *testing.T) {
	r := newTPRig(t)
	const ahu = "tp-ahu-tower-1"
	r.send(ahu, "mode-1", "set-mode", map[string]any{"mode": "economy"})
	if a, done := r.answered("mode-1"); !done || !a.ok {
		t.Fatalf("answer = %+v done=%v", a, done)
	}
	if got := r.model.reading(ahu)[TowerpulseAHUModeKey]; got != 1 {
		t.Errorf("mode = %v after economy, want 1", got)
	}
	r.send(ahu, "mode-2", "set-mode", map[string]any{"mode": "turbo"})
	if a, done := r.answered("mode-2"); !done || a.ok {
		t.Errorf("an unknown mode was answered %+v", a)
	}
	if got := r.model.reading(ahu)[TowerpulseAHUModeKey]; got != 1 {
		t.Errorf("a refused mode changed the handler to %v", got)
	}
}

// ---- The handler the far end is given ----------------------------------------------------------

// The Sim hands the cmdreceiver a handler that routes frames into the engine, and that handler
// answers through the func the receiver gave it, with the outcome the engine decided.
func TestTheSimsHandlerRoutesFramesIntoTheCommandEngine(t *testing.T) {
	s := NewTowerpulse(1, Load{}).(*towerpulse)
	h := s.CommandHandler()
	got := make(chan bool, 1)
	h.Receive("tp-ahu-tower-1", cmdreceiver.Command{Token: "c1", Name: "set-mode", Payload: json.RawMessage(`{"mode":"boost"}`)},
		func(ok bool) { got <- ok })
	select {
	case ok := <-got:
		if !ok {
			t.Error("a valid set-mode was answered FAILED")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never answered a command that takes effect at once")
	}
	if mode := s.model.reading("tp-ahu-tower-1")[TowerpulseAHUModeKey]; mode != 2 {
		t.Errorf("mode = %v, want 2 (boost)", mode)
	}
	// And the same engine is shared: asking for the handler again does not build a second campus.
	if s.CommandHandler().(towerpulseHandler).commands != s.commands {
		t.Error("CommandHandler built a second command engine")
	}
}

// ---- Tick ---------------------------------------------------------------------------------------

type tickPost struct {
	device       string
	measurements map[string]string
}

// Tick posts one reading per device, measured metrics only, and a silent device posts nothing.
func TestTickPublishesEveryDeviceExceptASilentOne(t *testing.T) {
	m := towerpulseManifest(t)
	devices := m.Expand(m.Seed)

	var mu sync.Mutex
	var posts []tickPost
	rt := fakeIngress(t, 0, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Device  string `json:"device"`
			Payload struct {
				Entries []struct {
					Measurements map[string]string `json:"measurements"`
				} `json:"entries"`
			} `json:"payload"`
		}
		decodeJSON(t, r, &body)
		mu.Lock()
		for _, e := range body.Payload.Entries {
			posts = append(posts, tickPost{body.Device, e.Measurements})
		}
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	rt.Devices = devices
	rt.Load = Load{Concurrency: 4}

	clock := time.Unix(1_700_000_000, 0)
	s := NewTowerpulse(1, Load{}).(*towerpulse)
	s.now = func() time.Time { return clock }

	if err := s.Tick(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	byDevice := map[string]map[string]string{}
	for _, p := range posts {
		byDevice[p.device] = p.measurements
	}
	if len(posts) != 31 || len(byDevice) != 31 {
		t.Fatalf("%d posts from %d devices, want one each from 31", len(posts), len(byDevice))
	}
	for _, d := range devices {
		if _, ok := byDevice[d.Token]["cop"]; ok {
			t.Errorf("%s posted a calculated metric", d.Token)
		}
		if len(byDevice[d.Token]) == 0 {
			t.Errorf("%s posted an empty measurement", d.Token)
		}
	}

	// Well past the drop: the annex chiller is silent and the tower chiller is not.
	posts = nil
	clock = clock.Add(time.Duration(towerpulseChillerDropSecond+60) * time.Second)
	if err := s.Tick(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	var who []string
	for _, p := range posts {
		who = append(who, p.device)
	}
	sort.Strings(who)
	if len(posts) != 30 {
		t.Errorf("%d posts after the drop, want 30", len(posts))
	}
	for _, d := range who {
		if d == TowerpulseDropChiller {
			t.Error("the dropped chiller still posted")
		}
	}
	if !contains(who, "tp-chiller-tower") {
		t.Error("the tower chiller stopped posting")
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// The model is not built until it is needed, and then once: a Tick and a CommandHandler asked for
// in either order share one campus, or a command would act on a building nobody is reading.
func TestTickAndTheHandlerShareOneCampus(t *testing.T) {
	s := NewTowerpulse(1, Load{}).(*towerpulse)
	model1, cmds1 := s.ensureModel()
	model2, cmds2 := s.ensureModel()
	if model1 != model2 || cmds1 != cmds2 {
		t.Error("ensureModel built a second campus")
	}
	if err := s.DropChiller("tp-chiller-tower"); err != nil {
		t.Fatal(err)
	}
	if model1.reading("tp-chiller-tower") != nil {
		t.Error("DropChiller on the Sim did not reach the campus the Tick reads")
	}
}
