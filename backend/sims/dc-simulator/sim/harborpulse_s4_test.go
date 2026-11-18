// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/schedule"
	"github.com/devicechain-io/dc-microservice/solar"
	"github.com/devicechain-io/dc-microservice/userclient"
)

func harborWith(t *testing.T, defects ...string) *harborpulse {
	t.Helper()
	s := NewHarborpulse(1, Load{}).(*harborpulse)
	if err := s.ConfigureScenario(ScenarioOptions{Defects: defects}); err != nil {
		t.Fatal(err)
	}
	return s
}

// selectorLeaf is the one shape the scenario's selectors use: attr["k"] == "v" or == true.
var selectorLeaf = regexp.MustCompile(`^attr\["([a-z]+)"\] == (?:"([^"]*)"|(true|false))$`)

// membersOf evaluates a selector over the devices' declared attributes, as the platform's lowering
// does for this shape: every leaf must hold, and an absent attribute holds no leaf.
func membersOf(t *testing.T, selector string, devices []DeviceInstance) []string {
	t.Helper()
	var out []string
	for _, d := range devices {
		attrs := map[string]AttributeSpec{}
		for _, a := range d.Attributes {
			attrs[a.Key] = a
		}
		in := true
		for _, leaf := range strings.Split(selector, " && ") {
			m := selectorLeaf.FindStringSubmatch(leaf)
			if m == nil {
				t.Fatalf("selector leaf %q is not a shape this test evaluates", leaf)
			}
			a, ok := attrs[m[1]]
			switch {
			case !ok:
				in = false
			case m[3] != "":
				in = in && a.ValueType == "BOOLEAN" && a.Value == m[3]
			default:
				in = in && a.ValueType == "STRING" && a.Value == m[2]
			}
		}
		if in {
			out = append(out, d.Token)
		}
	}
	return out
}

func TestHarborpulseGroupsHoldWhatTheirAttributesSay(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	want := HarborpulseGroupMembers()
	devices := m.Expand(1)
	for _, g := range m.EntityGroups {
		got := membersOf(t, g.Selector, devices)
		if strings.Join(got, ",") != strings.Join(want[g.Token], ",") {
			t.Errorf("group %s selects %v, want %v", g.Token, got, want[g.Token])
		}
	}
	if len(want[HarborpulseAllLightsGroup]) != 12 || len(want[HarborpulseEastLightsGroup]) != 4 ||
		len(want[HarborpulseCriticalPumpsGroup]) != 2 || len(want[HarborpulseBasinPumpsGroup]) != 1 {
		t.Fatalf("group sizes %v", want)
	}

	// The mislabelled lamp leaves East Quay's group, and nothing else moves.
	bad := harborWith(t, HarborpulseDefectEastLampMislabelled).Manifest()
	for _, g := range bad.EntityGroups {
		got := membersOf(t, g.Selector, bad.Expand(1))
		if g.Token == HarborpulseEastLightsGroup {
			if len(got) != 3 || sliceContains(got, harborMislabelledLamp) {
				t.Fatalf("east-lamp-mislabelled: East Quay's group is %v", got)
			}
		} else if strings.Join(got, ",") != strings.Join(want[g.Token], ",") {
			t.Fatalf("east-lamp-mislabelled moved group %s too: %v", g.Token, got)
		}
	}
}

// The watch must be scoped, its threshold crossed by EVERY pump's storm (so the scope is what
// excludes a pump), and the unscoped defect must drop the scope only.
func TestHarborpulseWellWatchIsScopedAndCrossedByEveryPump(t *testing.T) {
	ruleOf := func(s *harborpulse) DetectionRuleSpec {
		for _, p := range s.Manifest().Profiles {
			for _, r := range p.DetectionRules {
				if r.Token == HarborpulseWellWatchRule {
					return r
				}
			}
		}
		t.Fatal("no well watch rule")
		return DetectionRuleSpec{}
	}
	clean, bad := ruleOf(harborWith(t)), ruleOf(harborWith(t, HarborpulseDefectUnscopedWatch))
	if clean.Group != HarborpulseCriticalPumpsGroup || bad.Group != "" || clean.Definition != bad.Definition {
		t.Fatalf("clean scope %q, defect scope %q, definitions equal %v", clean.Group, bad.Group, clean.Definition == bad.Definition)
	}
	model := newHarborModel(1)
	peak := map[string]float64{}
	for e := time.Duration(0); e < harborpulseDayLength; e += time.Second {
		model.advanceTo(e)
		for _, p := range harborTokens(harborPump, "") {
			if r := model.reading(p, e, time.Now()); r[HarborpulseWetWellKey] > peak[p] {
				peak[p] = r[HarborpulseWetWellKey]
			}
		}
		// The platform starts the pumps at the wet-well level, as the rule does.
		for _, p := range harborTokens(harborPump, "") {
			if model.pumps[p].well > HarborpulseWetWellM && !model.pumps[p].running {
				model.command(p, HarborpulseStartPumpKey, json.RawMessage(`{"duty_pct":100}`))
			}
		}
	}
	for p, v := range peak {
		if v <= HarborpulseWellWatchM {
			t.Errorf("%s peaks at %.2f m, never above the watch's %.1f m", p, v, HarborpulseWellWatchM)
		}
	}
}

// The day's jobs land on whole minutes at their sim times, the flush a minute before the window
// opens, and the platform's own trigger code plans exactly one cycle for each at that instant.
func TestHarborpulseDayJobsFireInsideTheirSimDay(t *testing.T) {
	s := harborWith(t)
	started := time.Date(2026, 11, 17, 9, 0, 17, 0, time.UTC)
	for d := 0; d < 3; d++ {
		jobs, campaigns, slots := s.dayJobs(started, d)
		flush, dim := jobs[0], jobs[1]
		parse := func(v string) time.Time {
			at, err := time.Parse(time.RFC3339, v)
			if err != nil {
				t.Fatal(err)
			}
			return at
		}
		fire, opens, closes, dimAt := parse(flush.FireAt), parse(flush.WindowOpens), parse(flush.WindowCloses), parse(dim.FireAt)
		for name, c := range map[string]struct {
			at   time.Time
			hour float64
		}{"flush": {fire, HarborpulseFlushAtHour}, "window": {opens, HarborpulseWindowAtHour}, "dim": {dimAt, HarborpulseNightDimHour}} {
			w := harborWall(started, s.dayLength, d, c.hour)
			if c.at.Before(w) || c.at.Sub(w) >= time.Minute || c.at.Second() != 0 {
				t.Errorf("day %d %s at %s, want the first whole minute at or after %s", d, name, c.at, w)
			}
		}
		if opens.Sub(fire) != time.Minute || closes.Sub(opens) != harborWindowMinutes*time.Minute {
			t.Errorf("day %d: flush %s, window %s..%s", d, fire, opens, closes)
		}
		if slots[0].Start != opens.Format("15:04") || slots[0].Day != strings.ToUpper(opens.Weekday().String()) {
			t.Errorf("day %d: slot %+v for an opening at %s", d, slots[0], opens)
		}
		// The sim day's beats: the dim after the outage, the window before the storm's pumping.
		if dimAt.Before(harborWall(started, s.dayLength, d, harborOutageTo)) || closes.After(harborWall(started, s.dayLength, d, 18)) {
			t.Errorf("day %d: the dim (%s) or the window's close (%s) is outside its beat", d, dimAt, closes)
		}
		for i, c := range campaigns {
			trigger, err := schedule.New(schedule.Spec{Kind: schedule.Kind(c.Trigger.Kind), Zone: c.Trigger.Zone,
				Dates: c.Trigger.Dates, Times: c.Trigger.Times})
			if err != nil {
				t.Fatalf("day %d campaign %s: the platform's trigger code refuses it: %v", d, c.Token, err)
			}
			want := parse(jobs[i].FireAt)
			cycles, err := trigger.Cycles(started, started.Add(36*time.Hour))
			if err != nil || len(cycles) != 1 || !cycles[0].At.Equal(want) {
				t.Fatalf("day %d campaign %s plans %+v (%v), want one cycle at %s", d, c.Token, cycles, err, want)
			}
		}
	}
	night := harborWith(t, HarborpulseDefectNightDimAllLights)
	if _, c, _ := night.dayJobs(started, 0); c[1].GroupToken != HarborpulseAllLightsGroup {
		t.Fatalf("night-dim-all-lights aims the dim at %s", c[1].GroupToken)
	}
}

func TestHarborpulseFlushExpiryCoversWindowAndDelivery(t *testing.T) {
	started := time.Date(2026, 11, 17, 23, 0, 17, 0, time.UTC)
	for _, dayLength := range []time.Duration{HarborpulseMinDayLength, 12 * time.Hour, 13 * time.Hour, HarborpulseMaxDayLength} {
		t.Run(dayLength.String(), func(t *testing.T) {
			s := NewHarborpulse(1, Load{}).(*harborpulse)
			if err := s.ConfigureScenario(ScenarioOptions{DayLength: dayLength}); err != nil {
				t.Fatal(err)
			}
			for d := 0; d < 3; d++ {
				jobs, campaigns, _ := s.dayJobs(started, d)
				flush := jobs[0]
				if flush.Name != HarborpulseJobFlush || campaigns[0].Token != flush.Campaign {
					t.Fatal("flush job and campaign do not match")
				}
				fire, err := time.Parse(time.RFC3339, flush.FireAt)
				if err != nil {
					t.Fatal(err)
				}
				closes, err := time.Parse(time.RFC3339, flush.WindowCloses)
				if err != nil {
					t.Fatal(err)
				}
				ttl := time.Duration(campaigns[0].ExpiresInSeconds) * time.Second
				expires := fire.Add(ttl)
				if expires.Before(closes) {
					t.Errorf("day %d: flush expires at %s before window closes at %s", d, expires, closes)
				}
				if expires.Before(closes.Add(30 * time.Minute)) {
					t.Errorf("day %d: flush expiry %s must include 30 minutes for delivery after window closes at %s", d, expires, closes)
				}
				if ttl < 30*time.Minute {
					t.Errorf("day %d: flush expiry duration %s is below the 30-minute floor", d, ttl)
				}
			}
		})
	}
}

// The SOLAR triggers are valid to the platform's trigger code, the holiday is tomorrow in the
// town, and the lamps' planned instants run east to west, each a whole second or more apart.
func TestHarborpulseSolarCampaignsMakeAWave(t *testing.T) {
	now := time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	s := harborWith(t)
	s.now = func() time.Time { return now }
	m := s.Manifest()
	if len(m.Campaigns) != 2 {
		t.Fatalf("%d static campaigns", len(m.Campaigns))
	}
	for _, c := range m.Campaigns {
		var spec schedule.Spec
		if err := json.Unmarshal([]byte(c.triggerJSON()), &spec); err != nil {
			t.Fatal(err)
		}
		if _, err := schedule.New(spec); err != nil {
			t.Fatalf("%s: the platform's trigger code refuses %s: %v", c.Token, c.triggerJSON(), err)
		}
	}
	if h := m.Campaigns[0].Trigger.Holidays; len(h) != 1 || h[0] != "2026-11-18" {
		t.Fatalf("dusk holidays %v, want the town's tomorrow", h)
	}
	if len(m.Campaigns[1].Trigger.Holidays) != 0 {
		t.Fatal("the dawn campaign declares a holiday; it is the control that plans the day")
	}
	if h := harborWith(t, HarborpulseDefectHolidayDropped).Manifest().Campaigns[0].Trigger.Holidays; len(h) != 0 {
		t.Fatalf("holiday-dropped kept %v", h)
	}

	type sun struct {
		lamp string
		at   time.Time
		lon  float64
	}
	var wave []sun
	for _, d := range m.Expand(1) {
		if d.DeviceTypeToken != HarborpulseLightType {
			continue
		}
		if d.Position == nil {
			t.Fatalf("%s has no position", d.Token)
		}
		ev, err := solar.Compute(2026, 11, 17, d.Position.Latitude, d.Position.Longitude)
		if err != nil || ev.Status != solar.Normal {
			t.Fatal(err)
		}
		wave = append(wave, sun{d.Token, ev.Set, d.Position.Longitude})
	}
	sort.Slice(wave, func(i, j int) bool { return wave[i].lon > wave[j].lon })
	for i := 1; i < len(wave); i++ {
		if wave[i].at.Sub(wave[i-1].at) < time.Second {
			t.Fatalf("%s sets %s after %s, which stands east of it: the platform plans whole seconds", wave[i].lamp,
				wave[i].at.Sub(wave[i-1].at), wave[i-1].lamp)
		}
	}
	if spread := wave[len(wave)-1].at.Sub(wave[0].at); spread < 10*time.Second {
		t.Fatalf("the wave spans %s", spread)
	}
	for _, d := range harborWith(t, HarborpulseDefectLampUnsited).Manifest().Expand(1) {
		if (d.Position == nil) != (d.Token == harborUnsitedLamp) && d.DeviceTypeToken == HarborpulseLightType {
			t.Fatalf("lamp-unsited: %s position %v", d.Token, d.Position)
		}
	}
}

func TestHarborpulseHierarchyFeedsAndWindowDefects(t *testing.T) {
	clean := NewHarborpulse(1, Load{}).Manifest()
	parent := func(m SimManifest, asset string) string {
		for _, a := range m.Assets {
			if a.Token == asset {
				return a.Parent
			}
		}
		return "?"
	}
	if parent(clean, HarborpulseLoftsBuilding) != HarborpulseQuaySite(HarborpulseNorthQuay) ||
		parent(harborWith(t, HarborpulseDefectLoftsUnderBasin).Manifest(), HarborpulseLoftsBuilding) != HarborpulseQuaySite(HarborpulseBasin) {
		t.Fatal("the Lofts building is not under North Quay, or lofts-under-basin does not move it")
	}
	feeder := func(m SimManifest, device string) string {
		for _, r := range m.Relationships {
			if r.Target == device {
				return r.Source
			}
		}
		return ""
	}
	if feeder(clean, harborMiswiredLamp) != HarborpulseFeeder(HarborpulseEastQuay) ||
		feeder(harborWith(t, HarborpulseDefectFeederMiswired).Manifest(), harborMiswiredLamp) != HarborpulseFeeder(HarborpulseNorthQuay) {
		t.Fatal("feeder-miswired does not move the lamp's feeder")
	}
	if len(clean.Relationships) != 18 {
		t.Fatalf("%d feed edges, want the 12 lights and 6 chargers", len(clean.Relationships))
	}
	window := func(m SimManifest) string {
		for _, dt := range m.DeviceTypes {
			if dt.Token == HarborpulsePumpType {
				return dt.MaintenanceWindowToken
			}
		}
		return "?"
	}
	if window(clean) != HarborpulsePumpWindow || window(harborWith(t, HarborpulseDefectFlushUngated).Manifest()) != "" {
		t.Fatal("flush-ungated does not drop the pump type's window")
	}
	for _, d := range clean.Expand(1) {
		for _, a := range d.Assignments {
			if a.TargetType == "asset" && d.DeviceTypeToken != HarborpulsePumpType && d.DeviceTypeToken != HarborpulseMeterType {
				t.Errorf("%s is assigned to asset %s", d.Token, a.TargetToken)
			}
		}
	}
	// The flush is disruptive, and the controller carries it out; a lamp does not.
	model := newHarborModel(1)
	if !model.command(harborBasinPump, HarborpulseFlushKey, nil) || model.command("hp-light-north-1", HarborpulseFlushKey, nil) {
		t.Fatal("flush-well: the pump must accept it and a lamp refuse it")
	}
}

func TestValidateHoldsTheFleetOps(t *testing.T) {
	base := func() SimManifest { return NewHarborpulse(1, Load{}).Manifest() }
	cases := map[string]func(*SimManifest){
		"rule scoped to an unknown group": func(m *SimManifest) {
			for i, p := range m.Profiles {
				for j, r := range p.DetectionRules {
					if r.Group != "" {
						m.Profiles[i].DetectionRules[j].Group = "nope"
					}
				}
			}
		},
		"campaign on an unknown group":    func(m *SimManifest) { m.Campaigns[0].GroupToken = "nope" },
		"campaign sending an unknown key": func(m *SimManifest) { m.Campaigns[0].Steps[0].Command = "nope" },
		"device type with an unknown window": func(m *SimManifest) {
			for i := range m.DeviceTypes {
				m.DeviceTypes[i].MaintenanceWindowToken = "nope"
			}
		},
		"an asset hierarchy cycle": func(m *SimManifest) {
			for i := range m.Assets {
				if m.Assets[i].Token == HarborpulseDistrictAsset {
					m.Assets[i].Parent = HarborpulseLoftsBuilding
				}
			}
		},
		"a relationship to an unknown device": func(m *SimManifest) { m.Relationships[0].Target = "nope" },
		"a window slot on no day":             func(m *SimManifest) { m.MaintenanceWindows[0].Slots[0].Day = "SOMEDAY" },
		"an unknown impact": func(m *SimManifest) {
			for i, p := range m.Profiles {
				for j := range p.Commands {
					m.Profiles[i].Commands[j].Impact = "LOUD"
				}
			}
		},
	}
	for name, mutate := range cases {
		m := base()
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

// A scoped rule's group version and a command's impact are published content: changing either
// changes the marker, and an unscoped, impact-free profile keeps the marker it had.
func TestTheScopeAndImpactAreInTheProfileMarker(t *testing.T) {
	var pump ProfileSpec
	for _, p := range NewHarborpulse(1, Load{}).Manifest().Profiles {
		if p.Token == HarborpulsePumpProfile {
			pump = p
		}
	}
	v1 := profileContentMarkerScoped(pump, map[string]int{HarborpulseCriticalPumpsGroup: 1})
	v2 := profileContentMarkerScoped(pump, map[string]int{HarborpulseCriticalPumpsGroup: 2})
	if v1 == v2 {
		t.Fatal("re-pinning the watch to a new group version did not change the marker")
	}
	plain := pump
	plain.Commands = append([]CommandSpec(nil), pump.Commands...)
	for i := range plain.Commands {
		plain.Commands[i].Impact = ""
	}
	if profileContentMarkerScoped(plain, map[string]int{HarborpulseCriticalPumpsGroup: 1}) == v1 {
		t.Fatal("dropping the flush's impact did not change the marker")
	}
	gauge := harborpulseGaugeProfile(false, false)
	if profileContentMarker(gauge) != profileContentMarkerScoped(gauge, map[string]int{"x": 9}) {
		t.Fatal("a profile with no scoped rule depends on group versions")
	}
}

// Provisioning may cross the town's midnight after the manifest was built.
func TestHarborpulseHolidayUsesCampaignCreationClock(t *testing.T) {
	zone, err := time.LoadLocation(HarborpulseTownZone)
	if err != nil {
		t.Fatal(err)
	}
	// In summer the town's midnight differs from UTC's.
	now := time.Date(2026, 7, 16, 23, 59, 59, 0, zone)
	s := harborWith(t)
	s.now = func() time.Time { return now }
	manifest := s.Manifest()
	var stored *storedCampaign
	creates, deletes := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), 400)
			return
		}
		data := map[string]any{}
		switch {
		case strings.Contains(req.Query, "login("), strings.Contains(req.Query, "selectTenant("):
			data, _ = (&fakeProfileServer{}).dispatch(req.Query, req.Variables)
		case strings.Contains(req.Query, "refresh("):
			data["refresh"] = map[string]any{"accessToken": "access", "refreshToken": "refresh"}
		case strings.Contains(req.Query, "createCampaign("):
			creates++
			raw, _ := json.Marshal(req.Variables["request"])
			stored = &storedCampaign{}
			if err := json.Unmarshal(raw, stored); err != nil {
				t.Error(err)
			}
			data["createCampaign"] = map[string]any{"campaign": map[string]any{"token": stored.Token}}
		case strings.Contains(req.Query, "deleteCampaign("):
			deletes++
			stored = nil
			data["deleteCampaign"] = true
		case strings.Contains(req.Query, "campaign("):
			data["campaign"] = stored
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	rt := &Runtime{Endpoints: Endpoints{CommandMgmtGraphQL: srv.URL},
		Session: userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.invalid", "pw", "acme")}
	now = now.Add(2 * time.Second)
	if err := ensureCampaign(context.Background(), rt, manifest.Campaigns[0]); err != nil {
		t.Fatal(err)
	}
	var trigger CampaignTrigger
	if err := json.Unmarshal([]byte(stored.Trigger), &trigger); err != nil {
		t.Fatal(err)
	}
	if len(trigger.Holidays) != 1 || trigger.Holidays[0] != "2026-07-18" {
		t.Fatalf("created dusk holidays %v, want the day after creation (2026-07-18)", trigger.Holidays)
	}
	original := stored.Trigger
	now = now.Add(24 * time.Hour)
	if err := ensureCampaign(context.Background(), rt, s.Manifest().Campaigns[0]); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || deletes != 0 || stored.Trigger != original {
		t.Fatalf("converge rewrote the holiday: creates=%d deletes=%d trigger=%s", creates, deletes, stored.Trigger)
	}
	broken := harborWith(t, HarborpulseDefectHolidayDropped)
	broken.now = s.now
	if err := ensureCampaign(context.Background(), rt, broken.Manifest().Campaigns[0]); err != nil {
		t.Fatal(err)
	}
	var dropped CampaignTrigger
	if err := json.Unmarshal([]byte(stored.Trigger), &dropped); err != nil {
		t.Fatal(err)
	}
	if len(dropped.Holidays) != 0 || creates != 2 || deletes != 1 {
		t.Fatalf("holiday-dropped did not replace just the holiday: creates=%d deletes=%d trigger=%s", creates, deletes, stored.Trigger)
	}
}
