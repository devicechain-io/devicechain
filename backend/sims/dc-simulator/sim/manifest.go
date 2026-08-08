// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

// MetricSpec is one numeric, unit-bearing metric a ProfileSpec declares (ADR-016).
// DataType uses the device-management MetricDataType vocabulary verbatim
// ("DOUBLE"/"INT"/"BOOLEAN"/"STRING") so bootstrap.go can pass it straight
// through to createMetricDefinition without translation.
type MetricSpec struct {
	Key      string // metricKey — the wire name a measurement entry keys by
	Name     string
	DataType string
	Unit     string
}

// CommandSpec is one typed command a profile declares (ADR-043): the key a
// sendCommand action or a dashboard's command-button issues, plus the JSON
// parameter schema that drives the console's typed form.
//
// ParameterSchema is the wire format verbatim — a JSON array of parameter
// descriptors — because that is what the platform stores and what a dashboard
// widget bakes in at author time. Modelling it as Go structs here would give two
// shapes to keep in step for no gain: nothing in the sim reads it back.
type CommandSpec struct {
	Token           string
	CommandKey      string
	Name            string
	ParameterSchema string
}

// DetectionRuleSpec is one DETECT rule authored on a profile (ADR-051/057).
// Definition is the opaque rules.Rule JSON; event-processing decodes it with
// DisallowUnknownFields, so a stray key is rejected at publish.
//
// It is written here as an untyped map and this module cannot import the type that
// decodes it, so a misspelled key costs nothing on this side — it marshals, publishes,
// and the rule simply never fires.
//
// So a rule declared on any REGISTERED scenario (sim.Registry) is published to
// backend/testdata/authored-rules by loadtest/authored_rules_fixture_test.go, which walks
// the registry rather than naming scenarios, and is then run through event-processing's
// REAL publish gate by event-processing/graphql/authored_sim_rules_test.go. Change a
// definition and regenerate the fixture; that test is what decides whether the new rule is
// valid. A scenario that is NOT in the registry is not reachable by a handshake either, so
// it is out of scope rather than a gap.
//
// Metric is NOT part of that JSON — it is a copy of the metric key the rule's
// predicate reads, declared separately so Validate can check the profile actually
// declares it. A rule referencing a metric no device reports is accepted by the
// platform and simply never fires: the scenario looks provisioned and is inert.
type DetectionRuleSpec struct {
	Token      string
	Name       string
	Definition string
	Metric     string
	Enabled    bool
}

// ProfileSpec is a static-singleton device profile (+ its metric definitions,
// commands and detection rules) a manifest provisions once and publishes.
// Profiles are shared capability contracts (ADR-045) — a manifest declares few of
// them even when it fans out many devices.
//
// Alarm authoring lives on DetectionRules, not AlarmDefinition: ADR-057 made the
// DETECT rule the one authoring path, and createAlarmDefinition no longer exists.
// A scenario that wants an alarm declares a rule whose action raises one.
type ProfileSpec struct {
	Token          string
	Name           string
	Category       string
	Metrics        []MetricSpec
	Commands       []CommandSpec
	DetectionRules []DetectionRuleSpec
}

// EnabledDetectionRuleCount is how many ENABLED detection rules this manifest publishes
// across all its profiles.
//
// Only enabled rules count, everywhere this number is used: a disabled rule is inert by
// design — the publish gate does not submit it and the liveness check does not require
// it — so a scenario that deliberately parks one must not be treated as depending on the
// engine.
//
// It is DERIVED rather than declared. A declared "this scenario needs event-processing"
// flag would be a second statement of the same fact with nothing to say that the first
// one does not: unlike FixedTopology and CommandFarEnd — which exist because INTENT can
// legitimately diverge from content ("one population" is not "resizable"; "has a command
// vocabulary" is not "intends to answer commands") — there is no scenario that publishes
// an enabled rule without needing the engine, nor one that needs the engine without
// publishing a rule. A flag whose validation would force it to equal the inference is
// inference wearing a declaration's clothes, plus one more thing to forget.
func (m SimManifest) EnabledDetectionRuleCount() int {
	n := 0
	for _, p := range m.Profiles {
		for _, r := range p.DetectionRules {
			if r.Enabled {
				n++
			}
		}
	}
	return n
}

// DeviceTypeSpec is a static-singleton device type referencing a profile.
type DeviceTypeSpec struct {
	Token        string
	Name         string
	ProfileToken string
}

// CustomerTypeSpec / CustomerSpec, AreaTypeSpec / AreaSpec, and AssetTypeSpec /
// AssetSpec are the same "classifier type, then instance" shape device profiles
// already use, applied to the three other entity-type hierarchies device
// assignments anchor against (ADR-013/044). Like DeviceTypeSpec/ProfileSpec,
// these are static singletons a manifest declares directly (not fanned out by
// a population) — a building-automation scenario has few customers/areas/asset
// types even with many devices.
type CustomerTypeSpec struct {
	Token string
	Name  string
}

type CustomerSpec struct {
	Token             string
	Name              string
	CustomerTypeToken string
}

type AreaTypeSpec struct {
	Token string
	Name  string
}

type AreaSpec struct {
	Token         string
	Name          string
	AreaTypeToken string
}

type AssetTypeSpec struct {
	Token string
	Name  string
}

type AssetSpec struct {
	Token          string
	Name           string
	AssetTypeToken string
}

// DashboardSpec is one dashboard a manifest provisions and publishes. Unlike
// the other specs here, DashboardSpec carries its full content (Definition) —
// the JSON document dashboard-management stores opaquely — because that content
// is scenario-specific and typically depends on the manifest's own Expand()'d
// devices (e.g. binding a chart to a specific device token). A scenario builds
// Definition in Go (see dashboard.go's buildBuildingpulseDashboard) before
// assembling its SimManifest, so this struct itself stays a plain data holder.
type DashboardSpec struct {
	Token       string
	Name        string
	Description string
	Definition  string
}

// PopulationSpec is the populations seam (ADR-050): a declarative "N devices of
// this type" spec, rendered deterministically by Expand. TokenPattern and
// ExternalIdPattern each support one "{n:0Wd}" placeholder (W = zero-pad width;
// "{n}" with no width is unpadded), filled with the 1-based instance index.
//
// DistributeAcross spreads a population's devices across the manifest's own
// instances of another entity type, rendering an Assignment per device (see
// Expand). Only the single value "area" is implemented — round-robin across
// SimManifest.Areas by (n-1) mod len(areas) — so devices land evenly across a
// building-automation scenario's buildings. "customer"/"asset" spreading is an
// intentionally documented extension, not built: with today's single-customer
// scenario a customer assignment doesn't need spreading (see Expand), and
// asset-tier assignment anchors are deferred entirely (assets are provisioned
// but never assignment targets yet).
type PopulationSpec struct {
	OfType            string // device-type token this population instantiates
	Count             int
	TokenPattern      string
	ExternalIdPattern string
	DistributeAcross  []string
}

// Assignment is one rendered device->target "assigned" EntityRelationship
// (ADR-013/044): TargetType is an entity-type registry string ("area",
// "asset", "customer" — the ones this slice models; the registry also has
// "device" and the *group variants, out of scope here), TargetToken is that
// target's own token, and RelationshipToken is the assignment's own
// deterministic token so bootstrap can create-or-get it idempotently exactly
// like every other *ByToken-then-create step.
type Assignment struct {
	TargetType        string
	TargetToken       string
	RelationshipToken string
}

// DeviceInstance is one concrete device Expand renders from a PopulationSpec:
// its addressing token, its business-id externalId (ADR-049), the device type
// it belongs to, the ACCESS_TOKEN credential bootstrap.go will provision for it
// (CredentialToken is the DeviceCredential's own token; CredentialId is the
// bearer value a device presents, ADR-014), and any Assignments its population's
// DistributeAcross (plus the manifest's fixed customer, if any) renders for it.
type DeviceInstance struct {
	Token           string
	ExternalId      string
	DeviceTypeToken string
	CredentialToken string
	CredentialId    string
	Assignments     []Assignment
}

// CommandFarEndMode says WHO answers a scenario's commands. It is a mode rather
// than a boolean because "does this scenario answer commands" and "does THIS
// PROCESS answer them" are different questions, and a boolean can only carry one
// of them — which forces a scenario whose far end is a presentation client (a
// Unity player being the device, sitepulse) to pick between two settings that are
// both wrong: off refuses its command widget outright, and on attaches the Go
// cmdreceiver, which answers SUCCESSFUL while the real far end never acted. The
// second is the false-success the whole seam exists to prevent, running backwards
// — and it is worse than the original defect, because a command that expires at
// SENT at least looks broken.
type CommandFarEndMode string

const (
	// FarEndNone: nothing answers. The devices only emit telemetry, and Validate
	// refuses a board carrying a command widget — the button would enqueue a real
	// command that reaches SENT and expires unanswered a week later.
	FarEndNone CommandFarEndMode = "none"
	// FarEndInternal: this process answers. Bootstrap attaches a cmdreceiver to
	// every expanded device over the MQTT gateway, so a command issued against any
	// of them completes QUEUED -> SENT -> SUCCESSFUL, and REFUSES to come up if it
	// cannot — see attachCommandFarEnd.
	FarEndInternal CommandFarEndMode = "internal"
	// FarEndExternal: a presentation client outside the simulator is the device —
	// it holds the MQTT session, receives the command and acts on it. This process
	// attaches NOTHING; attaching a Go receiver alongside it would answer for a
	// device it has no idea what happened to. The scenario must still declare a
	// command vocabulary on a profile (Validate enforces it), because that is what
	// the external client subscribes for and what the board's widget enqueues
	// against.
	//
	// 🔴 The simulator cannot tell "an external client is listening" from "nothing
	// is listening" — both are the absence of a local subscription — so this mode
	// is a STATEMENT OF INTENT that /status reports as such, never evidence that
	// anyone is there. See farEndStatus.
	FarEndExternal CommandFarEndMode = "external"
)

// validFarEndModes is the closed set Validate admits, "" included because the zero
// value of an omitted field means FarEndNone. Kept as a set rather than a switch in
// Validate so the mode constants and the accepted values cannot drift apart.
var validFarEndModes = map[CommandFarEndMode]bool{
	"": true, FarEndNone: true, FarEndInternal: true, FarEndExternal: true,
}

// FarEndMode is the manifest's far-end mode with the zero value resolved, and it is
// how EVERY consumer must read the field.
//
// The normalization exists to keep "" from crossing the boundary at all: a manifest
// that omits the field is a scenario with no far end, but `m.CommandFarEnd ==
// FarEndNone` is false for it. A caller comparing against the constants would then
// take neither the none branch nor the internal one — the code path nobody wrote a
// case for, reached by every manifest that simply did not mention commands. Validate
// is the one exception: it reads the RAW field, because "" is only legal on the way
// in, and normalizing before checking would accept any garbage that normalized.
func (m SimManifest) FarEndMode() CommandFarEndMode {
	if m.CommandFarEnd == "" {
		return FarEndNone
	}
	return m.CommandFarEnd
}

// SimManifest is the whole declarative shape of one sim scenario: the static
// singletons (profiles + device types + the customer/area/asset hierarchies +
// dashboards) provisioned once, and the populations that fan out into concrete
// devices. Seed is the ONLY source of non-pattern randomness (credential
// material derivation) — Expand never consults the system clock or crypto/rand,
// so (manifest, seed) deterministically reproduces the same tokens/externalIds/
// credentials/assignments on every run (ADR-050), which is what makes
// bootstrap's create-or-ignore idempotent across resets.
type SimManifest struct {
	Name string
	Seed int64
	// FixedTopology marks a scenario whose device set is a composed FIXTURE
	// rather than a scale knob: its dashboards bind named devices in named
	// areas, so resizing it leaves a board pointing at a device that no longer
	// exists — a dashboard that renders an empty pane on a topology nobody
	// asked for. withDeviceCount refuses an override against such a manifest.
	//
	// It is declared rather than inferred from the population count on purpose.
	// "One population" and "resizable" are different facts that happen to
	// coincide today, and inferring one from the other means a scenario becomes
	// silently un-resizable the day it grows a second population for an
	// unrelated reason.
	FixedTopology bool
	// DevicesPublishTheirOwnTelemetry marks a scenario whose devices are NOT this
	// process: they hold their own device-plane sessions and publish their own
	// measurements, so its Sim.Tick is bootstrap-only and emits nothing BY DESIGN.
	//
	// 🔴 IT EXISTS BECAUSE "RESIZABLE" SILENTLY MEANT "LOAD-DRIVABLE" until the first
	// such scenario joined the registry, and the failure is a true one reported as the
	// wrong cause. A load run drives Sim.Tick directly and reconciles what the DRIVER
	// accepted; against a scenario that emits nothing it provisions, holds for the full
	// window, applies zero load, and fails the MinAccepted floor with a message about a
	// job that lost its load flags. Nothing in that chain mentions the scenario, and the
	// operator had no way to know not to start it — cmd/loadtest-contention builds its
	// --manifest offering FROM THE REGISTRY, and cmd/loadtest, -monitor and -selftest
	// default the id from the HANDSHAKE, so both paths reach it with nobody having
	// chosen it.
	//
	// Declared rather than inferred from CommandFarEnd, and conflating the two would be
	// a second boolean wearing the first one's clothes: an external far end says who
	// ANSWERS COMMANDS, not who PUBLISHES TELEMETRY. A scenario may perfectly well
	// generate its telemetry here and hand only its command channel to a presentation
	// client, and inferring one from the other would refuse that scenario's load run on
	// the day someone writes it — silently, since the refusal would look correct.
	//
	// Read by LoadDrivableManifestIds (what a load tool may OFFER) and by
	// loadtest.Profile.Validate (what a load run may START).
	DevicesPublishTheirOwnTelemetry bool
	// CommandFarEnd says WHO answers this scenario's commands — see
	// CommandFarEndMode for what each setting means and why the answer cannot be
	// a boolean. Read it through FarEndMode(), never directly: the zero value is
	// "none" and every consumer that compares against a constant has to see the
	// normalized value, not "".
	//
	// It exists because the sim's device model is otherwise one-way: it POSTs to
	// the HTTP ingress and subscribes to nothing. A scenario that provisions
	// CommandDefinitions and puts a command-button on a dashboard therefore ships
	// a control surface with NO FAR END — the button enqueues a real command
	// against a real definition, the platform dispatches it correctly, and it sits
	// at SENT until it expires seven days later. That is a demo of the platform
	// working and the scenario not, and the two are indistinguishable on the board.
	//
	// Declared rather than inferred from "does any profile declare commands"
	// for the FixedTopology reason above: a profile may carry a command vocabulary
	// for provisioning coverage without the scenario intending to answer one, and
	// attaching a broker connection per device is not something to acquire by
	// accident. Validate refuses the inverse — a far end with nothing to answer.
	CommandFarEnd CommandFarEndMode
	CustomerTypes []CustomerTypeSpec
	Customers     []CustomerSpec
	AreaTypes     []AreaTypeSpec
	Areas         []AreaSpec
	AssetTypes    []AssetTypeSpec
	Assets        []AssetSpec
	Profiles      []ProfileSpec
	DeviceTypes   []DeviceTypeSpec
	Populations   []PopulationSpec
	Dashboards    []DashboardSpec
}

// placeholderPattern matches "{n}" or "{n:0Wd}" (W one or more digits). The
// RENDERING of these placeholders now lives in core.RenderTemplate (shared with
// device-management's bulk device create, so the two never drift); this regex
// remains only for structurally SPLITTING a pattern into its literal segments,
// which a test does to derive the fixed prefix/suffix around the index.
var placeholderPattern = regexp.MustCompile(`\{n(?::0(\d+)d)?\}`)

// renderPattern fills a TokenPattern/ExternalIdPattern's "{n}"/"{n:0Wd}"
// placeholder with a 1-based index. The sim's populations never use "{random}",
// so it renders through core.RenderTemplate with no random source — pure string
// formatting, deterministic given (pattern, n).
func renderPattern(pattern string, n int) string {
	return core.RenderTemplate(pattern, n, nil)
}

// deriveCredential deterministically derives an ACCESS_TOKEN credential's own
// token and bearer id from (seed, deviceToken). Both are lowercase hex — always
// grammar-safe (core.ValidateToken) regardless of what characters the device
// token itself contains, and stable across resets since they depend only on
// values already fixed by the manifest + seed.
func deriveCredential(seed int64, deviceToken string) (credentialToken, credentialId string) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:cred", seed, deviceToken)))
	digest := hex.EncodeToString(sum[:])
	return deviceToken + "-cred", digest[:32]
}

// assignmentToken derives an assignment relationship's own token from the two
// ends it connects — deterministic and grammar-safe by construction as long as
// both tokens are (Validate checks the rendered result regardless).
func assignmentToken(deviceToken, targetToken string) string {
	return "assign-" + deviceToken + "-" + targetToken
}

// sliceContains reports whether v is present in ss. Small local helper — this
// package has no need for a general-purpose slice-utility dependency.
func sliceContains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// renderAssignments builds one device's Assignment set from its population's
// DistributeAcross and the manifest's fixed customer (if any). Pure index math
// over already-materialized manifest slices — no clock/rand — so it is exactly
// as deterministic as the rest of Expand.
func (m SimManifest) renderAssignments(pop PopulationSpec, deviceToken string, n int) []Assignment {
	var assignments []Assignment

	if sliceContains(pop.DistributeAcross, "area") && len(m.Areas) > 0 {
		area := m.Areas[(n-1)%len(m.Areas)]
		assignments = append(assignments, Assignment{
			TargetType:        "area",
			TargetToken:       area.Token,
			RelationshipToken: assignmentToken(deviceToken, area.Token),
		})
	}

	// The customer assignment is a fixed target on every device, not something
	// DistributeAcross governs: today's scenarios declare at most one customer,
	// so there is nothing to spread across yet. Spreading across >1 customers is
	// the documented "customer" DistributeAcross extension, deliberately unbuilt.
	if len(m.Customers) > 0 {
		customer := m.Customers[0]
		assignments = append(assignments, Assignment{
			TargetType:        "customer",
			TargetToken:       customer.Token,
			RelationshipToken: assignmentToken(deviceToken, customer.Token),
		})
	}

	return assignments
}

// Expand deterministically renders every PopulationSpec into concrete
// DeviceInstances, including each device's Assignment set. The same (manifest,
// seed) always yields identical output — no unseeded randomness anywhere in
// this path (ADR-050).
func (m SimManifest) Expand(seed int64) []DeviceInstance {
	instances := make([]DeviceInstance, 0)
	for _, pop := range m.Populations {
		for n := 1; n <= pop.Count; n++ {
			token := renderPattern(pop.TokenPattern, n)
			externalId := ""
			if strings.TrimSpace(pop.ExternalIdPattern) != "" {
				externalId = renderPattern(pop.ExternalIdPattern, n)
			}
			credToken, credId := deriveCredential(seed, token)
			instances = append(instances, DeviceInstance{
				Token:           token,
				ExternalId:      externalId,
				DeviceTypeToken: pop.OfType,
				CredentialToken: credToken,
				CredentialId:    credId,
				Assignments:     m.renderAssignments(pop, token, n),
			})
		}
	}
	return instances
}

// DeviceCount reports how many devices m expands to, without expanding it.
//
// Expand materializes every DeviceInstance and derives a SHA-256 credential per
// device; callers that only need the SIZE (sizing a connection pool, logging
// the resolved topology) should not pay for a whole population to learn it.
func DeviceCount(m SimManifest) int {
	total := 0
	for _, pop := range m.Populations {
		total += pop.Count
	}
	return total
}

// entityAssignmentTargetTypes are the entity-type registry strings (mirrored
// here as literals — device-management's model.entityref.go is the source of
// truth — since the sim only ever speaks the wire) a rendered Assignment may
// target in this slice. The registry also has "device" and *group variants;
// they are out of scope until a scenario needs them.
var entityAssignmentTargetTypes = map[string]bool{
	"area":     true,
	"asset":    true,
	"customer": true,
}

// Validate checks the manifest's static shape and every token/assignment
// Expand would render, so a malformed pattern or a dangling reference (e.g. an
// assignment to an area that doesn't exist) fails fast at bootstrap rather than
// surfacing as an opaque GraphQL error deep in provisioning.
func (m SimManifest) Validate() error {
	// Fail closed on the far-end mode before anything reads it. A mode this package
	// does not know is not a scenario with an unusual far end, it is a typo —
	// "externl" — and every consumer below reads through FarEndMode(), which passes
	// an unknown value straight through to comparisons that all miss. The scenario
	// would then behave as none (no attach, no widget) while its manifest says it
	// has an external far end, and nothing anywhere would mention the typo.
	if !validFarEndModes[m.CommandFarEnd] {
		return fmt.Errorf("manifest %q has CommandFarEnd %q, which is not one of %q, %q or %q",
			m.Name, m.CommandFarEnd, FarEndNone, FarEndInternal, FarEndExternal)
	}

	customerTypeTokens := make(map[string]bool, len(m.CustomerTypes))
	for _, ct := range m.CustomerTypes {
		if err := core.ValidateToken(ct.Token); err != nil {
			return fmt.Errorf("customer type token: %w", err)
		}
		customerTypeTokens[ct.Token] = true
	}
	customerTokens := make(map[string]bool, len(m.Customers))
	for _, c := range m.Customers {
		if err := core.ValidateToken(c.Token); err != nil {
			return fmt.Errorf("customer token: %w", err)
		}
		if !customerTypeTokens[c.CustomerTypeToken] {
			return fmt.Errorf("customer %q references unknown customer type %q", c.Token, c.CustomerTypeToken)
		}
		customerTokens[c.Token] = true
	}

	areaTypeTokens := make(map[string]bool, len(m.AreaTypes))
	for _, at := range m.AreaTypes {
		if err := core.ValidateToken(at.Token); err != nil {
			return fmt.Errorf("area type token: %w", err)
		}
		areaTypeTokens[at.Token] = true
	}
	areaTokens := make(map[string]bool, len(m.Areas))
	for _, a := range m.Areas {
		if err := core.ValidateToken(a.Token); err != nil {
			return fmt.Errorf("area token: %w", err)
		}
		if !areaTypeTokens[a.AreaTypeToken] {
			return fmt.Errorf("area %q references unknown area type %q", a.Token, a.AreaTypeToken)
		}
		areaTokens[a.Token] = true
	}

	assetTypeTokens := make(map[string]bool, len(m.AssetTypes))
	for _, at := range m.AssetTypes {
		if err := core.ValidateToken(at.Token); err != nil {
			return fmt.Errorf("asset type token: %w", err)
		}
		assetTypeTokens[at.Token] = true
	}
	assetTokens := make(map[string]bool, len(m.Assets))
	for _, a := range m.Assets {
		if err := core.ValidateToken(a.Token); err != nil {
			return fmt.Errorf("asset token: %w", err)
		}
		if !assetTypeTokens[a.AssetTypeToken] {
			return fmt.Errorf("asset %q references unknown asset type %q", a.Token, a.AssetTypeToken)
		}
		assetTokens[a.Token] = true
	}

	profileTokens := make(map[string]bool, len(m.Profiles))
	// Command and rule tokens share one namespace across the whole manifest, unlike
	// metric tokens, which ensureMetricDefinition prefixes with their profile.
	versionContentTokens := make(map[string]bool)
	for _, p := range m.Profiles {
		if err := core.ValidateToken(p.Token); err != nil {
			return fmt.Errorf("profile token: %w", err)
		}
		metricKeys := make(map[string]bool, len(p.Metrics))
		for _, mx := range p.Metrics {
			metricKeys[mx.Key] = true
		}
		// The keys a sendCommand action on THIS profile's rules may name — the CommandKey,
		// which is a different string from the CommandSpec.Token validated just below and
		// from the display Name. Collected here so the rule loop further down can check
		// against it; see the cross-check there for why the scope is one profile.
		commandKeys := make(map[string]bool, len(p.Commands))
		for _, c := range p.Commands {
			if err := core.ValidateToken(c.Token); err != nil {
				return fmt.Errorf("command definition token: %w", err)
			}
			if c.CommandKey == "" {
				return fmt.Errorf("command %q declares no commandKey", c.Token)
			}
			// The schema must be a JSON ARRAY of descriptors. The platform decodes
			// it strictly and rejects anything else at create, so this is early
			// rather than the difference between a failure and none — but the
			// message here names the manifest field, and the one from a GraphQL
			// round-trip does not.
			if c.ParameterSchema != "" {
				var params []any
				if err := json.Unmarshal([]byte(c.ParameterSchema), &params); err != nil {
					return fmt.Errorf("command %q has a parameterSchema that is not a JSON array "+
						"of parameter descriptors: %w", c.Token, err)
				}
			}
			// Command and rule tokens are NOT profile-prefixed the way metric tokens
			// are, so two profiles can name the same one. The second profile's
			// reconciler then finds the token already present, matches its content,
			// and does nothing — so that profile publishes WITHOUT the command, and
			// no step anywhere reports a failure.
			if versionContentTokens[c.Token] {
				return fmt.Errorf("command token %q is declared twice; the second profile would "+
					"silently publish without it", c.Token)
			}
			versionContentTokens[c.Token] = true
			commandKeys[c.CommandKey] = true
		}
		for _, r := range p.DetectionRules {
			if err := core.ValidateToken(r.Token); err != nil {
				return fmt.Errorf("detection rule token: %w", err)
			}
			if versionContentTokens[r.Token] {
				return fmt.Errorf("detection rule token %q is declared twice; the second profile "+
					"would silently publish without it", r.Token)
			}
			versionContentTokens[r.Token] = true
			if !json.Valid([]byte(r.Definition)) {
				return fmt.Errorf("detection rule %q has a definition that is not valid JSON", r.Token)
			}
			// The seam this check exists for: a rule reading a metric the profile
			// does not declare is accepted by the platform and never fires, so the
			// scenario provisions cleanly and produces no alarms at all.
			if r.Metric == "" {
				return fmt.Errorf("detection rule %q names no metric, so nothing checks that its "+
					"predicate reads something the profile reports", r.Token)
			}
			if !metricKeys[r.Metric] {
				return fmt.Errorf("detection rule %q reads metric %q, which profile %q does not "+
					"declare — the rule would be published and never fire", r.Token, r.Metric, p.Token)
			}
			// Metric is a hand-written copy of a value that also lives INSIDE the
			// opaque definition, which is the two-lists-that-must-agree shape: the
			// check above would happily bless a rule whose declared metric is
			// reported and whose predicate reads something else entirely.
			//
			// Only a top-level `when.metric` is read back. A composite rule has no
			// single metric there, and inventing a traversal of a JSON shape this
			// package deliberately treats as opaque would reject rules it cannot
			// actually judge. So: check the shape we can see, stay quiet on the rest.
			if predicate := ruleTopLevelMetric(r.Definition); predicate != "" && predicate != r.Metric {
				return fmt.Errorf("detection rule %q declares metric %q but its predicate reads %q; "+
					"the declared one is what Validate checks against the profile, so the rule "+
					"would pass this check and still never fire", r.Token, r.Metric, predicate)
			}
			// The sendCommand twin of the metric checks above, and the seam it closes is
			// open on BOTH sides of the wire: nothing anywhere else compares a rule's
			// command key against a command vocabulary. event-processing's compiler is
			// deliberately state-free and validates only that the key is a grammar-valid
			// token; this manifest otherwise judges Definition as "is it JSON". So a typo
			// publishes clean, lands in the authored-rules fixture, compiles, and reports
			// ACTIVE to the post-publish liveness assert — which proves the rule COMPILED
			// and nothing more. It fails for the first time when the rule FIRES, as a
			// dead-letter several services away from the line that caused it.
			//
			// SCOPE: the rule's OWN profile, not every command key in the manifest — the
			// same scope as the metric check above, for the same reason. A rule is
			// profile-version content, it fires for devices carrying that profile, and its
			// sendCommand goes to the device the detection fired for; a key declared on
			// some OTHER profile is therefore just as dead as one declared nowhere, and a
			// manifest-wide lookup would bless exactly that case. The cost of the narrower
			// scope is a false positive should a scenario ever legitimately need a
			// cross-profile command — and that direction is the safe one: a loud bootstrap
			// error naming the rule and the key, not a silent misbehaviour, with widening
			// the lookup then a deliberate change rather than a gap nobody knew was there.
			for _, key := range ruleSendCommandKeys(r.Definition) {
				if !commandKeys[key] {
					return fmt.Errorf("detection rule %q sends command %q, which profile %q does "+
						"not declare as a commandKey — the rule would publish, compile and report "+
						"%s, then dead-letter on its first firing when the enqueue gate rejects "+
						"the key", r.Token, key, p.Token, RuleStatusActiveWire)
				}
			}
		}
		for _, mx := range p.Metrics {
			// Mirrors bootstrap.go's ensureMetricDefinition token derivation
			// (profileToken + "-" + metricKey) so a grammar-unsafe metric key
			// fails here rather than deep inside a GraphQL round-trip.
			if err := core.ValidateToken(p.Token + "-" + mx.Key); err != nil {
				return fmt.Errorf("metric definition token: %w", err)
			}
		}
		profileTokens[p.Token] = true
	}

	typeTokens := make(map[string]bool, len(m.DeviceTypes))
	for _, dt := range m.DeviceTypes {
		if err := core.ValidateToken(dt.Token); err != nil {
			return fmt.Errorf("device type token: %w", err)
		}
		if dt.ProfileToken != "" && !profileTokens[dt.ProfileToken] {
			return fmt.Errorf("device type %q references unknown profile %q", dt.Token, dt.ProfileToken)
		}
		typeTokens[dt.Token] = true
	}

	for _, pop := range m.Populations {
		if !typeTokens[pop.OfType] {
			return fmt.Errorf("population references unknown device type %q", pop.OfType)
		}
		if pop.Count < 0 {
			return fmt.Errorf("population for %q has a negative count", pop.OfType)
		}
		for _, da := range pop.DistributeAcross {
			switch da {
			case "area":
				if len(m.Areas) == 0 {
					return fmt.Errorf("population for %q has distributeAcross:[\"area\"] but the manifest declares no areas", pop.OfType)
				}
			default:
				return fmt.Errorf("population for %q has unsupported distributeAcross value %q (only \"area\" is implemented)", pop.OfType, da)
			}
		}
	}

	for _, ds := range m.Dashboards {
		if err := core.ValidateToken(ds.Token); err != nil {
			return fmt.Errorf("dashboard token: %w", err)
		}
		if strings.TrimSpace(ds.Definition) == "" {
			return fmt.Errorf("dashboard %q has an empty definition", ds.Token)
		}
		// A board carrying a control widget with no far end is the defect this
		// gate exists for, and it is invisible everywhere else: the widget renders,
		// the enqueue succeeds against a real command definition, the platform
		// dispatches correctly, and the command sits at SENT until it expires. Every
		// layer reports success and the demonstration is worthless.
		//
		// This is the ONE place the manifest reads inside a definition it otherwise
		// treats as opaque. The justification is that the two facts genuinely live
		// on opposite sides of that boundary — "this board offers control" is in the
		// definition, "these devices answer" is in the manifest — so nothing else
		// can see both. A definition that does not parse is left to the dashboard
		// tests, not re-reported here.
		//
		// The gate is on FarEndNone specifically, not on "no cmdreceiver will
		// attach": FarEndExternal attaches nothing in this process either, but a
		// board offering control is exactly right for it — the presentation client
		// IS the device. What the gate refuses is a control surface no one at all
		// stands behind.
		if m.FarEndMode() == FarEndNone {
			var parsed dashboardDefinition
			if err := json.Unmarshal([]byte(ds.Definition), &parsed); err == nil {
				for _, w := range parsed.Widgets {
					if w.Type == commandWidgetType {
						return fmt.Errorf("dashboard %q carries a %s widget (%q) but the manifest "+
							"declares CommandFarEnd %q: nothing in the scenario would answer "+
							"the command, so it would reach SENT and expire",
							ds.Token, commandWidgetType, w.Id, m.FarEndMode())
					}
				}
			}
		}
	}

	// The inverse: a far end is a live broker connection per device. Acquiring one
	// for a scenario that publishes no command vocabulary means every device
	// subscribes to a topic nothing can ever publish to.
	//
	// It applies to BOTH far-end modes, and external is the case that needs saying
	// out loud, because there the cost is not a wasted connection: the command
	// vocabulary is the entire contract between this manifest and a client that
	// lives in another process. With no CommandDefinition published there is nothing
	// for the external client to subscribe for and nothing for a board's widget to
	// enqueue against, so the mode declares a far end that could not be reached even
	// if it were running.
	if m.FarEndMode() != FarEndNone {
		commands := 0
		for _, p := range m.Profiles {
			commands += len(p.Commands)
		}
		if commands == 0 {
			return fmt.Errorf("manifest %q declares CommandFarEnd %q but no profile declares a "+
				"command definition, so there is nothing for the far end to answer",
				m.Name, m.FarEndMode())
		}
	}

	for _, d := range m.Expand(m.Seed) {
		if err := core.ValidateToken(d.Token); err != nil {
			return fmt.Errorf("rendered device token: %w", err)
		}
		if err := core.ValidateToken(d.CredentialToken); err != nil {
			return fmt.Errorf("rendered credential token: %w", err)
		}
		if err := validateAssignments(d, areaTokens, assetTokens, customerTokens); err != nil {
			return err
		}
	}
	return nil
}

// ruleTopLevelMetric reads the metric a rules.Rule reads, or "" when the
// definition names none at a depth this package is willing to look.
//
// TWO fields, because a rules.Rule has two. A threshold/duration/repeating rule
// names it in its leaf comparison (`when.metric`); an aggregate or deltaRate rule
// names its value selector at the top level (`metric`). Reading only the first
// left every aggregate rule unchecked — it would provision cleanly against a
// profile that never reports the metric, and never fire, which is the exact seam
// the caller says it closes. They are the same trivial depth; missing one was an
// oversight, not a judgement about opacity.
//
// A rule may legitimately name neither (connectivity is leaf-less and config-less
// by design). Returning "" means "no opinion", never "no metric": the caller must
// not treat it as a failure, or it would make those rules unauthorable.
func ruleTopLevelMetric(definition string) string {
	var rule struct {
		Metric string `json:"metric"`
		When   struct {
			Metric string `json:"metric"`
		} `json:"when"`
	}
	if err := json.Unmarshal([]byte(definition), &rule); err != nil {
		return ""
	}
	if rule.When.Metric != "" {
		return rule.When.Metric
	}
	return rule.Metric
}

// ruleSendCommandKeys reads the command keys a rules.Rule's sendCommand actions name, or
// nil when the definition names none at a depth this package is willing to look.
//
// Same posture as ruleTopLevelMetric above, and the same justification: this package treats
// Definition as opaque, and reaches inside it only where the alternative is a fact NOTHING
// anywhere checks. This is such a place. Both sides genuinely decline to check it —
// event-processing's compiler is state-free by design and never reads a profile, and the
// manifest holds the command vocabulary — so a command key belongs to the same
// two-facts-on-opposite-sides-of-a-boundary shape as the dashboard control-widget gate.
//
// A definition that does not parse returns nil rather than an error: json.Valid in Validate
// already owns that failure, and re-reporting it here would give one mistake two messages.
// A definition with no actions at all likewise returns nil — "no opinion", never "no
// command", exactly as ruleTopLevelMetric's "" is.
func ruleSendCommandKeys(definition string) []string {
	var rule struct {
		Actions []struct {
			Type        string `json:"type"`
			SendCommand struct {
				Command string `json:"command"`
			} `json:"sendCommand"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(definition), &rule); err != nil {
		return nil
	}
	var keys []string
	for _, a := range rule.Actions {
		// Gated on the DISCRIMINATOR rather than on a non-empty payload. An action whose
		// type disagrees with its populated variant is rejected at publish, so reading the
		// variant of an action that does not claim to be one would be forming an opinion
		// about a document that cannot exist. An EMPTY command is returned rather than
		// skipped for the same reason: skipping it would make a sendCommand naming nothing
		// the one action shape this check has no opinion on, which is the quietest
		// possible place for it to hide.
		if a.Type == actionTypeSendCommand {
			keys = append(keys, a.SendCommand.Command)
		}
	}
	return keys
}

// validateAssignments checks one device's rendered Assignment set: token
// grammar, a supported TargetType, and that the target actually exists in the
// corresponding lookup. Split out from Validate (which builds the lookups from
// m and always calls this against m.Expand(m.Seed)'s own, inherently self-
// consistent output) so a dangling-target rejection is directly testable
// against a hand-built Assignment, without needing Expand to somehow produce
// one on its own — which it never does by construction.
func validateAssignments(d DeviceInstance, areaTokens, assetTokens, customerTokens map[string]bool) error {
	for _, a := range d.Assignments {
		if err := core.ValidateToken(a.RelationshipToken); err != nil {
			return fmt.Errorf("rendered assignment token: %w", err)
		}
		if !entityAssignmentTargetTypes[a.TargetType] {
			return fmt.Errorf("device %q assignment references unsupported target type %q", d.Token, a.TargetType)
		}
		var exists bool
		switch a.TargetType {
		case "area":
			exists = areaTokens[a.TargetToken]
		case "asset":
			exists = assetTokens[a.TargetToken]
		case "customer":
			exists = customerTokens[a.TargetToken]
		}
		if !exists {
			return fmt.Errorf("device %q assignment references unknown %s %q", d.Token, a.TargetType, a.TargetToken)
		}
	}
	return nil
}
