// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
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

// AlarmDefSpec is one alarm definition a ProfileSpec declares, evaluated live
// against measurements against devices of that profile once published. Only
// ConditionType "SIMPLE" is evaluated by the live evaluator today — DURATION
// and REPEATING are accepted by the schema but not yet acted on, so scenarios
// should stick to SIMPLE. The definition's own token is derived the same way
// ensureMetricDefinition derives a metric's (profileToken + "-" + key), here
// keyed by AlarmKey rather than metric Key — see ensureAlarmDefinition.
type AlarmDefSpec struct {
	AlarmKey    string
	MetricKey   string
	Name        string
	Description string
	// ConditionType is one of SIMPLE/DURATION/REPEATING; Operator one of
	// GT/GTE/LT/LTE/EQ/NEQ; Severity one of CRITICAL/MAJOR/MINOR/WARNING/
	// INDETERMINATE (device-management's AlarmDefinitionCreateRequest vocabulary,
	// mirrored here verbatim since the sim only speaks the wire).
	ConditionType string
	Operator      string
	Severity      string
	// Exactly one of Threshold/ThresholdAttr is set, mirroring the schema's own
	// "exactly one of threshold / thresholdAttr" invariant (server-validated on
	// create). Pointers so an unset Threshold marshals as GraphQL null rather
	// than a misleading 0.
	Threshold           *float64
	ThresholdAttr       string
	DurationSeconds     *int
	RepeatCount         *int
	RepeatWindowSeconds *int
	Enabled             bool
}

// ProfileSpec is a static-singleton device profile (+ its metric and alarm
// definitions) a manifest provisions once and publishes. Profiles are shared
// capability contracts (ADR-045) — a manifest declares few of them even when it
// fans out many devices. Alarms must exist before publish (ADR-045: draft is
// inert until publish, so the active version's snapshot must already include
// them) — Provision enforces that ordering, not this struct.
type ProfileSpec struct {
	Token    string
	Name     string
	Category string
	Metrics  []MetricSpec
	Alarms   []AlarmDefSpec
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

// SimManifest is the whole declarative shape of one sim scenario: the static
// singletons (profiles + device types + the customer/area/asset hierarchies +
// dashboards) provisioned once, and the populations that fan out into concrete
// devices. Seed is the ONLY source of non-pattern randomness (credential
// material derivation) — Expand never consults the system clock or crypto/rand,
// so (manifest, seed) deterministically reproduces the same tokens/externalIds/
// credentials/assignments on every run (ADR-050), which is what makes
// bootstrap's create-or-ignore idempotent across resets.
type SimManifest struct {
	Name          string
	Seed          int64
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

// placeholderPattern matches "{n}" or "{n:0Wd}" (W one or more digits).
var placeholderPattern = regexp.MustCompile(`\{n(?::0(\d+)d)?\}`)

// renderPattern fills a TokenPattern/ExternalIdPattern's "{n}"/"{n:0Wd}"
// placeholder with a 1-based index. Pure string formatting — no randomness.
func renderPattern(pattern string, n int) string {
	return placeholderPattern.ReplaceAllStringFunc(pattern, func(match string) string {
		sub := placeholderPattern.FindStringSubmatch(match)
		if sub[1] == "" {
			return strconv.Itoa(n)
		}
		width, err := strconv.Atoi(sub[1])
		if err != nil {
			return strconv.Itoa(n)
		}
		return fmt.Sprintf("%0*d", width, n)
	})
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
	for _, p := range m.Profiles {
		if err := core.ValidateToken(p.Token); err != nil {
			return fmt.Errorf("profile token: %w", err)
		}
		for _, mx := range p.Metrics {
			// Mirrors bootstrap.go's ensureMetricDefinition token derivation
			// (profileToken + "-" + metricKey) so a grammar-unsafe metric key
			// fails here rather than deep inside a GraphQL round-trip.
			if err := core.ValidateToken(p.Token + "-" + mx.Key); err != nil {
				return fmt.Errorf("metric definition token: %w", err)
			}
		}
		for _, ax := range p.Alarms {
			// Mirrors bootstrap.go's ensureAlarmDefinition token derivation
			// (profileToken + "-" + alarmKey).
			if err := core.ValidateToken(p.Token + "-" + ax.AlarmKey); err != nil {
				return fmt.Errorf("alarm definition token: %w", err)
			}
			if ax.Threshold == nil && strings.TrimSpace(ax.ThresholdAttr) == "" {
				return fmt.Errorf("alarm %q on profile %q sets neither threshold nor thresholdAttr", ax.AlarmKey, p.Token)
			}
			if ax.Threshold != nil && strings.TrimSpace(ax.ThresholdAttr) != "" {
				return fmt.Errorf("alarm %q on profile %q sets both threshold and thresholdAttr", ax.AlarmKey, p.Token)
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
