// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package arena

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxScenarioBytes bounds a scenario document.
const maxScenarioBytes = 1 << 20

// Work bounds, so a scenario cannot make one reset do unbounded simulation: an episode
// plays at most maxEpisodeSeconds of virtual time, and zones x seconds is capped. A reset
// simulates the episode three times (agent, baseline, ceiling).
const (
	maxEpisodeSeconds = 6 * 3600
	maxEpisodeWork    = 4_000_000 // zone-seconds per simulated run
)

// Fixed measurement names of the plant. The plant models a zone controller, so the arena
// reads and writes exactly these on the controlled kind; a scenario that names others is
// refused at load rather than graded against a plant that does not model them.
const (
	metricTemp     = "temp_c"
	metricSetpoint = "setpoint_c"
	metricDamper   = "damper_pct"
	metricOccupied = "occupancy"
)

// Command keys the plant knows how to apply. A scenario may declare any commands on the
// profile; one whose key is not in this set is a valid, declared command the plant cannot
// execute, and the load refuses it (an unimplemented command must fail loudly, not be
// accepted and ignored).
const (
	CmdSetSetpoint  = "set-setpoint"
	CmdBoostCooling = "boost-cooling"
)

// ParamDef is one parameter of a typed command, in the shape the device profile publishes
// its parameterSchema in (so a productized arena loads the real definitions unchanged).
type ParamDef struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind,omitempty"`
	DataType string   `json:"dataType"`
	Unit     string   `json:"unit,omitempty"`
	Required bool     `json:"required,omitempty"`
	Min      *float64 `json:"minValue,omitempty"`
	Max      *float64 `json:"maxValue,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

// CommandDef is one typed command a device kind accepts.
type CommandDef struct {
	DeviceKind      string     `json:"deviceKind"`
	Key             string     `json:"key"`
	Name            string     `json:"name"`
	ParameterSchema []ParamDef `json:"parameterSchema"`
}

// RuleSpec is the detection rule an episode is graded with: a windowed average of one
// measurement above a threshold while another is above a floor. It is the shape of the
// shipped zone-hot rule (a five-minute average above a line while the room is occupied).
type RuleSpec struct {
	Key        string  `json:"key"`
	Severity   string  `json:"severity"`
	Metric     string  `json:"metric"`
	WindowSec  float64 `json:"windowSec"`
	Above      float64 `json:"above"`
	WhileAbove float64 `json:"whileAbove"` // the occupancy floor: the rule is armed only while occupancy is above it
}

// PlantParams are the first-order zone's constants, in degrees Celsius per MINUTE. The
// defaults are the towerpulse simulator's own.
type PlantParams struct {
	CoolCapCPerMin  float64 `json:"coolCapCPerMin"`
	BoostCapCPerMin float64 `json:"boostCapCPerMin"`
	ControlBase     float64 `json:"controlBase"`
	ControlGain     float64 `json:"controlGain"`
	DamperFloor     float64 `json:"damperFloor"`
	AtSetpointC     float64 `json:"atSetpointC"`
}

// DefaultPlant is the towerpulse zone model's constants.
func DefaultPlant() PlantParams {
	return PlantParams{CoolCapCPerMin: 0.18, BoostCapCPerMin: 1.20, ControlBase: 20, ControlGain: 40, DamperFloor: 10, AtSetpointC: 0.15}
}

// EpisodeSpec is one playable window of the recording.
type EpisodeSpec struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Brief       string  `json:"brief"`
	T0Sec       float64 `json:"t0Sec"`
	DurationSec float64 `json:"durationSec"`
}

// Scenario is a recording plus everything needed to play it as a test: the typed commands,
// the rule that defines an alarm, the plant, and the windows (episodes) that make up a
// suite.
type Scenario struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Suite string `json:"suite"`
	// Tenants lists the tenants allowed to play this scenario, because a recording is a
	// tenant's operational history. SharedWithAllTenants is the explicit alternative for
	// an operator-installed fixture that is no tenant's data. Exactly one must be set: a
	// scenario that names neither is refused rather than served to everybody.
	Tenants              []string      `json:"tenants,omitempty"`
	SharedWithAllTenants bool          `json:"sharedWithAllTenants,omitempty"`
	Recording            string        `json:"recording"`
	ControlKind          string        `json:"controlKind"`
	Rule                 RuleSpec      `json:"rule"`
	Plant                *PlantParams  `json:"plant,omitempty"`
	Commands             []CommandDef  `json:"commands"`
	CommandBudget        int           `json:"commandBudget"`
	Episodes             []EpisodeSpec `json:"episodes"`

	rec   *Recording
	plant PlantParams
	zones []Device
}

// Rec returns the parsed recording.
func (s *Scenario) Rec() *Recording { return s.rec }

// VisibleTo reports whether a tenant may see and play this scenario.
func (s *Scenario) VisibleTo(tenant string) bool {
	if s.SharedWithAllTenants {
		return true
	}
	for _, t := range s.Tenants {
		if t == tenant {
			return true
		}
	}
	return false
}

// ID is the handle an agent passes to reset: scenario/episode.
func (s *Scenario) ID(e EpisodeSpec) string { return s.Name + "/" + e.ID }

// ParseScenario reads a scenario document and binds it to its recording. Unknown keys are
// refused: an ignored key is a field somebody believed was being honoured.
func ParseScenario(doc []byte, rec *Recording) (*Scenario, error) {
	if len(doc) > maxScenarioBytes {
		return nil, fmt.Errorf("scenario is %d bytes; the arena reads at most %d", len(doc), maxScenarioBytes)
	}
	var s Scenario
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	s.rec = rec
	s.plant = DefaultPlant()
	if s.Plant != nil {
		s.plant = *s.Plant
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("scenario %q: %w", s.Name, err)
	}
	return &s, nil
}

func (s *Scenario) validate() error {
	if s.Name == "" || strings.ContainsAny(s.Name, "/ ") {
		return fmt.Errorf("name must be non-empty with no slash or space")
	}
	if s.Suite == "" {
		return fmt.Errorf("suite is required")
	}
	if s.SharedWithAllTenants == (len(s.Tenants) > 0) {
		return fmt.Errorf("set exactly one of tenants (who may play it) or sharedWithAllTenants (an operator fixture that is no tenant's data)")
	}
	for _, t := range s.Tenants {
		if t == "" {
			return fmt.Errorf("tenants may not contain an empty name")
		}
	}
	if s.ControlKind == "" {
		return fmt.Errorf("controlKind is required")
	}
	for _, d := range s.rec.Devices {
		if d.Kind == s.ControlKind {
			s.zones = append(s.zones, d)
		}
	}
	if len(s.zones) == 0 {
		return fmt.Errorf("the recording has no device of kind %q", s.ControlKind)
	}
	for _, z := range s.zones {
		for _, m := range []string{metricTemp, metricSetpoint} {
			if len(s.rec.Series(z.Token, m)) == 0 {
				return fmt.Errorf("zone %q has no recorded %q", z.Token, m)
			}
		}
	}
	p := s.plant
	if p.CoolCapCPerMin <= 0 || p.BoostCapCPerMin <= p.CoolCapCPerMin || p.ControlGain <= 0 ||
		p.DamperFloor < 0 || p.DamperFloor > 100 || p.AtSetpointC < 0 {
		return fmt.Errorf("plant constants are not physical (cool %v, boost %v, gain %v, floor %v)",
			p.CoolCapCPerMin, p.BoostCapCPerMin, p.ControlGain, p.DamperFloor)
	}
	r := s.Rule
	if r.Key == "" || r.Metric != metricTemp || r.WindowSec <= 0 {
		return fmt.Errorf("rule needs a key, metric %q and a positive windowSec", metricTemp)
	}
	switch r.Severity {
	case "MINOR", "MAJOR", "CRITICAL":
	default:
		return fmt.Errorf("rule severity %q is not MINOR, MAJOR or CRITICAL", r.Severity)
	}
	if len(s.Commands) == 0 {
		return fmt.Errorf("no commands declared; an arena with nothing to do grades nothing")
	}
	seen := map[string]bool{}
	for _, c := range s.Commands {
		if c.DeviceKind != s.ControlKind {
			return fmt.Errorf("command %q is declared on kind %q, but only %q is controlled", c.Key, c.DeviceKind, s.ControlKind)
		}
		if c.Key != CmdSetSetpoint && c.Key != CmdBoostCooling {
			return fmt.Errorf("command %q is declared but the plant cannot execute it (it models %s and %s)", c.Key, CmdSetSetpoint, CmdBoostCooling)
		}
		if seen[c.Key] {
			return fmt.Errorf("command %q is declared twice", c.Key)
		}
		seen[c.Key] = true
		if len(c.ParameterSchema) != 1 {
			return fmt.Errorf("command %q must declare exactly one parameter", c.Key)
		}
		pd := c.ParameterSchema[0]
		if !pd.Required {
			return fmt.Errorf("command %q parameter %q must be required: an optional bounded parameter, left out, would apply a default the bounds never saw", c.Key, pd.Name)
		}
		if pd.Min == nil || pd.Max == nil || *pd.Min >= *pd.Max {
			return fmt.Errorf("command %q parameter %q must declare minValue < maxValue: the bounds are what the arena grades safety against", c.Key, pd.Name)
		}
	}
	if s.CommandBudget <= 0 {
		return fmt.Errorf("commandBudget must be positive")
	}
	if len(s.Episodes) == 0 {
		return fmt.Errorf("no episodes")
	}
	ids := map[string]bool{}
	for _, e := range s.Episodes {
		if e.ID == "" || strings.ContainsAny(e.ID, "/ ") || ids[e.ID] {
			return fmt.Errorf("episode id %q is empty, malformed or repeated", e.ID)
		}
		ids[e.ID] = true
		if e.T0Sec < 0 || e.DurationSec <= 0 || e.T0Sec+e.DurationSec > s.rec.DurationSec {
			return fmt.Errorf("episode %q window [%v, %v] is outside the recording [0, %v]", e.ID, e.T0Sec, e.T0Sec+e.DurationSec, s.rec.DurationSec)
		}
		if e.DurationSec > maxEpisodeSeconds || e.DurationSec*float64(len(s.zones)) > maxEpisodeWork {
			return fmt.Errorf("episode %q plays %.0f s over %d zones; the arena bounds an episode to %d s and %d zone-seconds", e.ID, e.DurationSec, len(s.zones), maxEpisodeSeconds, maxEpisodeWork)
		}
	}
	return nil
}

// Command returns the typed definition of a command key.
func (s *Scenario) Command(key string) (CommandDef, bool) {
	for _, c := range s.Commands {
		if c.Key == key {
			return c, true
		}
	}
	return CommandDef{}, false
}

// Validate checks a command's arguments against its typed parameter schema, the same
// closed check a profile's command definition gives the platform: every required
// parameter present, no parameter the definition does not name, the declared type, the
// declared bounds and the declared enum. It returns the numeric value of the command's
// one parameter.
//
// 🔴 A BOUNDS FAILURE IS A DISTINCT RESULT FROM A MALFORMED CALL. The caller counts the
// first as an unsafe attempt (the agent tried to drive the plant outside its typed range)
// and the second as a refusal; conflating them would grade a typo as recklessness.
func (c CommandDef) Validate(params map[string]any) (value float64, outOfBounds bool, err error) {
	defs := map[string]ParamDef{}
	for _, p := range c.ParameterSchema {
		defs[p.Name] = p
	}
	for name := range params {
		if _, ok := defs[name]; !ok {
			return 0, false, fmt.Errorf("command %q has no parameter %q", c.Key, name)
		}
	}
	for _, p := range c.ParameterSchema {
		raw, present := params[p.Name]
		if !present {
			if p.Required {
				return 0, false, fmt.Errorf("parameter %q is required", p.Name)
			}
			continue
		}
		switch strings.ToUpper(p.DataType) {
		case "DOUBLE", "INT":
			f, ok := toFloat(raw)
			if !ok {
				return 0, false, fmt.Errorf("parameter %q must be a number", p.Name)
			}
			if strings.ToUpper(p.DataType) == "INT" && f != math.Trunc(f) {
				return 0, false, fmt.Errorf("parameter %q must be a whole number", p.Name)
			}
			if (p.Min != nil && f < *p.Min) || (p.Max != nil && f > *p.Max) {
				return f, true, fmt.Errorf("parameter %q = %v is outside the typed bounds [%v, %v]", p.Name, f, deref(p.Min), deref(p.Max))
			}
			value = f
		default:
			return 0, false, fmt.Errorf("parameter %q has dataType %q, which the arena does not execute", p.Name, p.DataType)
		}
	}
	if len(c.ParameterSchema) > 0 && c.ParameterSchema[0].Required {
		if _, ok := params[c.ParameterSchema[0].Name]; !ok {
			return 0, false, fmt.Errorf("parameter %q is required", c.ParameterSchema[0].Name)
		}
	}
	return value, false, nil
}

func deref(p *float64) any {
	if p == nil {
		return "-"
	}
	return *p
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// Catalog is the set of scenarios an arena serves.
type Catalog struct {
	scenarios map[string]*Scenario
}

// LoadCatalog reads every *.scenario.json in dir, each with the recording it names. The
// recording path is resolved inside dir and may not leave it.
func LoadCatalog(dir string) (*Catalog, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.scenario.json"))
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no *.scenario.json in %q", dir)
	}
	cat := &Catalog{scenarios: map[string]*Scenario{}}
	for _, path := range entries {
		doc, err := readCapped(path, maxScenarioBytes)
		if err != nil {
			return nil, err
		}
		var head struct {
			Recording string `json:"recording"`
		}
		if err := json.Unmarshal(doc, &head); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		recPath, err := insideDir(dir, head.Recording)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		recData, err := readCapped(recPath, maxRecordingBytes)
		if err != nil {
			return nil, err
		}
		rec, err := ParseRecording(recData)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", recPath, err)
		}
		s, err := ParseScenario(doc, rec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := cat.scenarios[s.Name]; dup {
			return nil, fmt.Errorf("scenario name %q is declared twice", s.Name)
		}
		cat.scenarios[s.Name] = s
	}
	return cat, nil
}

// NewCatalog builds a catalog from scenarios already parsed.
func NewCatalog(scenarios ...*Scenario) (*Catalog, error) {
	cat := &Catalog{scenarios: map[string]*Scenario{}}
	for _, s := range scenarios {
		if _, dup := cat.scenarios[s.Name]; dup {
			return nil, fmt.Errorf("scenario name %q is declared twice", s.Name)
		}
		cat.scenarios[s.Name] = s
	}
	return cat, nil
}

func readCapped(path string, max int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes; the arena reads at most %d", path, st.Size(), max)
	}
	return os.ReadFile(path)
}

func insideDir(dir, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("recording is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("recording %q must be relative to the scenario directory", rel)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(abs, rel)
	back, err := filepath.Rel(abs, full)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("recording %q leaves the scenario directory", rel)
	}
	return full, nil
}

// Resolve finds the scenario and episode a handle (scenario/episode) names for a tenant.
// A scenario the tenant may not play answers exactly as one that does not exist, so the
// catalog cannot be probed for another tenant's recordings.
func (c *Catalog) Resolve(tenant, id string) (*Scenario, EpisodeSpec, error) {
	name, ep, ok := strings.Cut(id, "/")
	if !ok {
		return nil, EpisodeSpec{}, fmt.Errorf("scenario %q is not of the form <scenario>/<episode>; call reset with no scenario to list them", clip(id))
	}
	s := c.scenarios[name]
	if s == nil || !s.VisibleTo(tenant) {
		return nil, EpisodeSpec{}, fmt.Errorf("no scenario %q; call reset with no scenario to list them", clip(name))
	}
	for _, e := range s.Episodes {
		if e.ID == ep {
			return s, e, nil
		}
	}
	return nil, EpisodeSpec{}, fmt.Errorf("scenario %q has no episode %q", name, clip(ep))
}

// Listing is one playable episode, as reset's catalog shows it.
type Listing struct {
	Scenario    string  `json:"scenario"`
	Suite       string  `json:"suite"`
	Title       string  `json:"title"`
	Brief       string  `json:"brief"`
	DurationSec float64 `json:"durationSec"`
}

// List shows every episode a tenant may play, sorted.
func (c *Catalog) List(tenant string) []Listing {
	var out []Listing
	for _, s := range c.scenarios {
		if !s.VisibleTo(tenant) {
			continue
		}
		for _, e := range s.Episodes {
			out = append(out, Listing{Scenario: s.ID(e), Suite: s.Suite, Title: e.Title, Brief: e.Brief, DurationSec: e.DurationSec})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scenario < out[j].Scenario })
	return out
}

// Suite returns the scenario handles of a suite that a tenant may play, sorted.
func (c *Catalog) Suite(tenant, suite string) []string {
	var out []string
	for _, l := range c.List(tenant) {
		if l.Suite == suite {
			out = append(out, l.Scenario)
		}
	}
	return out
}

// HasSuite reports whether any scenario, for any tenant, belongs to the suite; the
// configuration check uses it to refuse a policy for a suite that does not exist.
func (c *Catalog) HasSuite(suite string) bool {
	for _, s := range c.scenarios {
		if s.Suite == suite {
			return true
		}
	}
	return false
}
