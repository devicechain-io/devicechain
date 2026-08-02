// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/rs/zerolog/log"
)

// Provision runs the manifest's whole provisioning chain against
// device-management's (and, for dashboards, dashboard-management's) tenant
// GraphQL API, in the order a real scenario's references demand:
//
//  1. customer/area/asset classifier types, then their instances
//  2. device profile(s) (+ metrics + commands + detection rules) -> publish
//  3. device type(s)
//  4. devices (+ credentials)
//  5. assignments (bulk createEntityRelationships, "assigned" type)
//  6. dashboards (create -> publish)
//
// Every step checks-then-creates by token (a *ByToken query first, a create
// only if it comes back empty), so re-running Provision against an already-
// provisioned tenant is a no-op except for whatever is genuinely missing —
// this is what makes `reset` an idempotent re-Bootstrap rather than a
// drop-and-recreate. Everything a profile version snapshots — metrics, commands,
// detection rules — is created before publish (phase 2's own ordering, inside
// ensureProfile) since ADR-045's draft is inert until publish, and ensureProfile
// republishes when a re-run added any of them.
//
// Alarms are raised by a DetectionRule action, not by an AlarmDefinition: ADR-057
// made the DETECT rule the one authoring path and createAlarmDefinition no longer
// exists. This comment claimed "+ alarm defs" long after that was true, which is
// how buildingpulse came to look like it seeds alarms while raising none.
//
// On success, rt.Devices holds the manifest's Expand()'d devices (with their
// credential material and Assignments), ready for Tick to emit against.
func Provision(ctx context.Context, rt *Runtime, manifest SimManifest) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}

	// Fail fast (before any network call) if this scenario provisions dashboards
	// but no dashboard-management endpoint was resolved into the handshake. This
	// is where the dashboardMgmtGraphQL endpoint is "required" — lazily, by the
	// scenarios that actually need it — rather than unconditionally in
	// Handshake.Validate, so a dashboard-less devicepulse record still loads.
	if len(manifest.Dashboards) > 0 && rt.Endpoints.DashboardMgmtGraphQL == "" {
		return fmt.Errorf("manifest %q declares %d dashboard(s) but the handshake has no endpoints.dashboardMgmtGraphQL",
			manifest.Name, len(manifest.Dashboards))
	}

	// Same shape, for detection rules. verifyDetectionRulesAreLive makes this check
	// too, but only after the profile has published — by which point a customer, its
	// areas, its assets and a profile version already exist. There is nothing to
	// discover on the network here: a handshake with no event-processing endpoint
	// cannot confirm a rule no matter what the cluster is running, so it is decided
	// up front where it costs nothing.
	//
	// 🔴 It does NOT establish that event-processing is DEPLOYED. dcctl mints every
	// endpoint URL from the server name unconditionally, so this string is non-empty
	// even on a chart profile that omits the service; only the post-publish liveness
	// poll can tell those apart. This catches a handshake written by an older dcctl,
	// which is a different failure with a different fix.
	if rules := manifest.EnabledDetectionRuleCount(); rules > 0 && strings.TrimSpace(rt.Endpoints.EventProcessingGraphQL) == "" {
		return fmt.Errorf("manifest %q declares %d enabled detection rule(s) but the handshake "+
			"has no endpoints.eventProcessingGraphQL, so nothing can confirm they are running: "+
			"a rule dropped at load leaves the alarm widgets permanently empty with every step "+
			"reporting success. Re-create the sim with a current dcctl", manifest.Name, rules)
	}

	for _, ct := range manifest.CustomerTypes {
		if err := ensureCustomerType(ctx, rt, ct); err != nil {
			return fmt.Errorf("provision customer type %q: %w", ct.Token, err)
		}
	}
	for _, c := range manifest.Customers {
		if err := ensureCustomer(ctx, rt, c); err != nil {
			return fmt.Errorf("provision customer %q: %w", c.Token, err)
		}
	}
	for _, at := range manifest.AreaTypes {
		if err := ensureAreaType(ctx, rt, at); err != nil {
			return fmt.Errorf("provision area type %q: %w", at.Token, err)
		}
	}
	for _, a := range manifest.Areas {
		if err := ensureArea(ctx, rt, a); err != nil {
			return fmt.Errorf("provision area %q: %w", a.Token, err)
		}
	}
	for _, at := range manifest.AssetTypes {
		if err := ensureAssetType(ctx, rt, at); err != nil {
			return fmt.Errorf("provision asset type %q: %w", at.Token, err)
		}
	}
	for _, a := range manifest.Assets {
		if err := ensureAsset(ctx, rt, a); err != nil {
			return fmt.Errorf("provision asset %q: %w", a.Token, err)
		}
	}

	for _, p := range manifest.Profiles {
		if err := ensureProfile(ctx, rt, p); err != nil {
			return fmt.Errorf("provision profile %q: %w", p.Token, err)
		}
	}
	for _, dt := range manifest.DeviceTypes {
		if err := ensureDeviceType(ctx, rt, dt); err != nil {
			return fmt.Errorf("provision device type %q: %w", dt.Token, err)
		}
	}

	devices := manifest.Expand(manifest.Seed)
	for _, d := range devices {
		if err := ensureDevice(ctx, rt, d); err != nil {
			return fmt.Errorf("provision device %q: %w", d.Token, err)
		}
	}
	rt.Devices = devices

	if err := ensureAssignments(ctx, rt, devices); err != nil {
		return fmt.Errorf("provision assignments: %w", err)
	}

	for _, ds := range manifest.Dashboards {
		if err := ensureDashboard(ctx, rt, ds); err != nil {
			return fmt.Errorf("provision dashboard %q: %w", ds.Token, err)
		}
	}
	return nil
}

// --- device profile + metrics -------------------------------------------------

// deviceProfileInfo carries the profile plus its DRAFT definitions, fetched in one
// query so ensureProfile can compare what a scenario declares against what the
// tenant already holds — not merely whether a token exists.
type deviceProfileInfo struct {
	Token             string `json:"token"`
	ActiveVersion     *int   `json:"activeVersion"`
	MetricDefinitions []struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		DataType string `json:"dataType"`
		Unit     string `json:"unit"`
	} `json:"metricDefinitions"`
	CommandDefinitions []struct {
		Token           string `json:"token"`
		CommandKey      string `json:"commandKey"`
		Name            string `json:"name"`
		ParameterSchema string `json:"parameterSchema"`
	} `json:"commandDefinitions"`
	DetectionRules []struct {
		Token      string `json:"token"`
		Name       string `json:"name"`
		Definition string `json:"definition"`
		Enabled    bool   `json:"enabled"`
	} `json:"detectionRules"`
}

const queryDeviceProfilesByToken = `query($tokens:[String!]!){` +
	`deviceProfilesByToken(tokens:$tokens){token activeVersion ` +
	`metricDefinitions{token name dataType unit} ` +
	`commandDefinitions{token commandKey name parameterSchema} ` +
	`detectionRules{token name definition enabled}}}`

const mutationCreateDeviceProfile = `mutation($request:DeviceProfileCreateRequest){` +
	`createDeviceProfile(request:$request){token activeVersion}}`

const mutationPublishDeviceProfile = `mutation($token:String!,$label:String,$description:String){` +
	`publishDeviceProfile(token:$token,label:$label,description:$description){version}}`

func ensureProfile(ctx context.Context, rt *Runtime, p ProfileSpec) error {
	profile, err := deviceProfileByToken(ctx, rt, p.Token)
	if err != nil {
		return err
	}
	if profile == nil {
		var created struct {
			CreateDeviceProfile deviceProfileInfo `json:"createDeviceProfile"`
		}
		req := map[string]any{
			"token":    p.Token,
			"name":     p.Name,
			"category": p.Category,
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDeviceProfile,
			map[string]any{"request": req}, &created); err != nil {
			return fmt.Errorf("createDeviceProfile: %w", err)
		}
		profile = &created.CreateDeviceProfile
		log.Info().Str("token", p.Token).Msg("created device profile")
	}

	// Everything a profile version SNAPSHOTS is reconciled before the publish below,
	// because ADR-045's draft is inert until published: a metric, command or rule
	// that is not in the active version is one the running platform behaves as
	// though nobody declared.
	//
	// Reconciled, not merely created. Matching on token alone never converges
	// CONTENT: a scenario that ships a corrected rule definition would find its own
	// token already present and leave the broken one in place, so a fix could not be
	// delivered to any tenant that had already run the old build. That is not
	// hypothetical — this scenario shipped a rule the compiler rejects, and a
	// create-or-get provisioner could not have healed it.
	for _, m := range p.Metrics {
		if err := ensureMetricDefinition(ctx, rt, p.Token, m, profile); err != nil {
			return err
		}
	}
	for _, c := range p.Commands {
		if err := ensureCommandDefinition(ctx, rt, p.Token, c, profile); err != nil {
			return err
		}
	}
	for _, r := range p.DetectionRules {
		if err := ensureDetectionRule(ctx, rt, p.Token, r, profile); err != nil {
			return err
		}
	}

	// Publish when the ACTIVE version does not already carry THIS content.
	//
	// The obvious formulation — publish when this run created something — is wrong
	// in a way that fails silently and permanently. If a run adds a rule and the
	// publish then fails (the ADR-044 validation gate fails CLOSED, so a transiently
	// unavailable event-processing is enough), the next run finds the rule already
	// present, concludes it added nothing, sees a non-nil activeVersion, and skips
	// the publish forever. The addition is stranded as a draft: the tenant looks
	// fully provisioned and the widget bound to it renders empty, which is exactly
	// the failure this whole reconciliation exists to prevent.
	//
	// So the decision is made against durable state instead of against what this
	// process happened to do. The publish stamps a version label derived from the
	// declared content; if the content has never been published — because it
	// changed, because a publish failed, or because the profile is new — the label
	// is not there and the publish is retried automatically, while an unchanged
	// re-bootstrap publishes nothing and repeated resets accumulate no junk versions.
	//
	// 🔴 The question is asked of the ACTIVE version, not of every version that has
	// ever existed. Only the active version is live (ADR-045: a draft is inert), and
	// matching against the whole history means a marker in a SUPERSEDED version
	// answers "yes, published" forever. Concretely: the sim publishes its content,
	// a console user edits the draft and republishes, and the new active version does
	// not carry the scenario's rule — but the old label is still in history, so every
	// later bootstrap skips the publish and reports success over an active version
	// missing the very thing it was asked to provision. Reading the active label
	// instead makes that self-healing: it does not match, so the reconciled content
	// is republished.
	marker := profileContentMarker(p)
	active, hasActive, err := activeVersionOf(ctx, rt, p.Token)
	if err != nil {
		return err
	}
	if !hasActive || active.Label != marker {
		var out struct {
			PublishDeviceProfile struct {
				Version int `json:"version"`
			} `json:"publishDeviceProfile"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationPublishDeviceProfile,
			map[string]any{
				"token":       p.Token,
				"label":       marker,
				"description": "Published by the sim scenario that declares this profile.",
			}, &out); err != nil {
			return fmt.Errorf("publishDeviceProfile: %w", err)
		}
		log.Info().Str("token", p.Token).Int("version", out.PublishDeviceProfile.Version).
			Str("label", marker).Msg("published device profile")

		// Post-assert the ACTIVE version now carries this content. publishDeviceProfile
		// returning a version number says a row was written, not that the row is the
		// one the platform will serve — and everything downstream reads the active
		// version, not the one this call happened to create.
		republished, hasActive, verifyErr := activeVersionOf(ctx, rt, p.Token)
		if verifyErr != nil {
			return verifyErr
		}
		if !hasActive || republished.Label != marker {
			return fmt.Errorf("published profile %q but its active version carries label %q, not %q "+
				"(hasActive=%t): something republished it concurrently, and the running platform is "+
				"serving content this scenario did not declare", p.Token, republished.Label, marker, hasActive)
		}
		active = republished
	}

	// Whether or not this run published, prove the rules are LIVE before returning —
	// and specifically live for THIS version, which is why the active version number
	// is threaded through rather than re-derived.
	return verifyDetectionRulesAreLive(ctx, rt, p, active.Version)
}

// profileContentMarker is a stable, short digest of everything a ProfileSpec
// contributes to a published version. It is used as the version LABEL, which makes
// "has this exact content ever been published?" a question the tenant itself can
// answer — see ensureProfile.
//
// It hashes the declared content in declaration order. Order is part of the
// identity on purpose: reordering is a change a reader would expect to see
// published, and pretending otherwise would need a canonicalisation step whose only
// job is to hide it.
func profileContentMarker(p ProfileSpec) string {
	h := sha256.New()
	for _, m := range p.Metrics {
		fmt.Fprintf(h, "metric\x00%s\x00%s\x00%s\x00%s\n", m.Key, m.Name, m.DataType, m.Unit)
	}
	for _, c := range p.Commands {
		fmt.Fprintf(h, "command\x00%s\x00%s\x00%s\x00%s\n", c.Token, c.CommandKey, c.Name, c.ParameterSchema)
	}
	for _, r := range p.DetectionRules {
		fmt.Fprintf(h, "rule\x00%s\x00%s\x00%s\x00%t\n", r.Token, r.Name, r.Definition, r.Enabled)
	}
	return "sim-" + hex.EncodeToString(h.Sum(nil))[:16]
}

const queryDeviceProfileVersions = `query($token:String!){deviceProfileVersions(token:$token){version label}}`

// activeProfileVersion is the profile's ACTIVE published version: its number and its
// label. Superseded versions are ignored — only the active version is live (ADR-045:
// a draft, and every version behind the active one, is inert), so it is the only one
// whose label answers "is this content published?".
//
// The NUMBER is returned as well as the label because it is what identifies the
// version to event-processing: a rule id embeds "{profileToken}@{version}", so it is
// the only way to tell the engine serving what was just published from the engine
// still serving what came before it.
type activeProfileVersion struct {
	Version int
	Label   string
}

func activeVersionOf(ctx context.Context, rt *Runtime, token string) (activeProfileVersion, bool, error) {
	profile, err := deviceProfileByToken(ctx, rt, token)
	if err != nil {
		return activeProfileVersion{}, false, err
	}
	if profile == nil || profile.ActiveVersion == nil {
		return activeProfileVersion{}, false, nil
	}

	var out struct {
		DeviceProfileVersions []struct {
			Version int    `json:"version"`
			Label   string `json:"label"`
		} `json:"deviceProfileVersions"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDeviceProfileVersions,
		map[string]any{"token": token}, &out); err != nil {
		return activeProfileVersion{}, false, fmt.Errorf("deviceProfileVersions: %w", err)
	}
	for _, v := range out.DeviceProfileVersions {
		if v.Version == *profile.ActiveVersion {
			return activeProfileVersion{Version: v.Version, Label: v.Label}, true, nil
		}
	}
	// An active version number with no matching row is not "unpublished" — it is the
	// two reads disagreeing. Treating it as unpublished would republish on every
	// bootstrap; saying so is better than either guess.
	return activeProfileVersion{}, false, fmt.Errorf("profile %q reports active version %d, which "+
		"deviceProfileVersions does not list", token, *profile.ActiveVersion)
}

func deviceProfileByToken(ctx context.Context, rt *Runtime, token string) (*deviceProfileInfo, error) {
	var out struct {
		DeviceProfilesByToken []deviceProfileInfo `json:"deviceProfilesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDeviceProfilesByToken,
		map[string]any{"tokens": []string{token}}, &out); err != nil {
		return nil, fmt.Errorf("deviceProfilesByToken: %w", err)
	}
	if len(out.DeviceProfilesByToken) == 0 {
		return nil, nil
	}
	return &out.DeviceProfilesByToken[0], nil
}

const queryMetricDefinitionsByToken = `query($tokens:[String!]!){` +
	`metricDefinitionsByToken(tokens:$tokens){token}}`

const mutationCreateMetricDefinition = `mutation($request:MetricDefinitionCreateRequest){` +
	`createMetricDefinition(request:$request){token}}`

const mutationUpdateMetricDefinition = `mutation($token:String!,$request:MetricDefinitionCreateRequest){` +
	`updateMetricDefinition(token:$token,request:$request){token}}`

// ensureMetricDefinition creates the metric when the profile's draft lacks it, and
// updates it when the draft holds a different shape. Both paths leave the draft
// matching what the scenario declares; the publish decision is ensureProfile's.
func ensureMetricDefinition(ctx context.Context, rt *Runtime, profileToken string, m MetricSpec, profile *deviceProfileInfo) error {
	metricToken := profileToken + "-" + m.Key
	req := map[string]any{
		"token":              metricToken,
		"deviceProfileToken": profileToken,
		"metricKey":          m.Key,
		"name":               m.Name,
		"dataType":           m.DataType,
		"unit":               m.Unit,
	}

	for _, existing := range profile.MetricDefinitions {
		if existing.Token != metricToken {
			continue
		}
		if existing.Name == m.Name && existing.DataType == m.DataType && existing.Unit == m.Unit {
			return nil
		}
		var updated struct {
			UpdateMetricDefinition struct {
				Token string `json:"token"`
			} `json:"updateMetricDefinition"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationUpdateMetricDefinition,
			map[string]any{"token": metricToken, "request": req}, &updated); err != nil {
			return fmt.Errorf("updateMetricDefinition: %w", err)
		}
		log.Info().Str("token", metricToken).Str("profile", profileToken).Msg("updated metric definition")
		return nil
	}

	var created struct {
		CreateMetricDefinition struct {
			Token string `json:"token"`
		} `json:"createMetricDefinition"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateMetricDefinition,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createMetricDefinition: %w", err)
	}
	log.Info().Str("token", metricToken).Str("profile", profileToken).Msg("created metric definition")
	return nil
}

// --- command definitions + detection rules (ADR-043 / ADR-051, ADR-057) ---------
//
// Both are profile-version content, so both are created BEFORE ensureProfile's
// publish and both report whether they created anything, so a re-bootstrap that
// introduces one republishes rather than leaving it a draft.
//
// Both are create-or-get, unlike the load-test harnesses this is ported from
// (loadtest/detection.go, loadtest/command.go) which REFUSE a pre-existing rule.
// That refusal is right for a measurement tool — residual state skews a verdict —
// and wrong here: `reset` is a re-Bootstrap, so a scenario must converge on the
// topology it declares rather than demand a fresh tenant.

const mutationCreateCommandDefinition = `mutation($request:CommandDefinitionCreateRequest){` +
	`createCommandDefinition(request:$request){token}}`

const mutationUpdateCommandDefinition = `mutation($token:String!,$request:CommandDefinitionCreateRequest){` +
	`updateCommandDefinition(token:$token,request:$request){token}}`

func ensureCommandDefinition(ctx context.Context, rt *Runtime, profileToken string, c CommandSpec, profile *deviceProfileInfo) error {
	req := map[string]any{
		"token":              c.Token,
		"deviceProfileToken": profileToken,
		"commandKey":         c.CommandKey,
		"name":               c.Name,
	}
	if c.ParameterSchema != "" {
		req["parameterSchema"] = c.ParameterSchema
	}

	for _, existing := range profile.CommandDefinitions {
		if existing.Token != c.Token {
			continue
		}
		if existing.CommandKey == c.CommandKey && existing.Name == c.Name && existing.ParameterSchema == c.ParameterSchema {
			return nil
		}
		var updated struct {
			UpdateCommandDefinition struct {
				Token string `json:"token"`
			} `json:"updateCommandDefinition"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationUpdateCommandDefinition,
			map[string]any{"token": c.Token, "request": req}, &updated); err != nil {
			return fmt.Errorf("updateCommandDefinition: %w", err)
		}
		log.Info().Str("token", c.Token).Str("profile", profileToken).Msg("updated command definition")
		return nil
	}

	var created struct {
		CreateCommandDefinition struct {
			Token string `json:"token"`
		} `json:"createCommandDefinition"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateCommandDefinition,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createCommandDefinition: %w", err)
	}
	log.Info().Str("token", c.Token).Str("profile", profileToken).Str("commandKey", c.CommandKey).
		Msg("created command definition")
	return nil
}

const mutationCreateDetectionRule = `mutation($request:DetectionRuleCreateRequest!){` +
	`createDetectionRule(request:$request){token}}`

const mutationUpdateDetectionRule = `mutation($token:String!,$request:DetectionRuleCreateRequest!){` +
	`updateDetectionRule(token:$token,request:$request){token}}`

func ensureDetectionRule(ctx context.Context, rt *Runtime, profileToken string, r DetectionRuleSpec, profile *deviceProfileInfo) error {
	req := map[string]any{
		"token":              r.Token,
		"deviceProfileToken": profileToken,
		"name":               r.Name,
		"definition":         r.Definition,
		"enabled":            r.Enabled,
	}

	for _, existing := range profile.DetectionRules {
		if existing.Token != r.Token {
			continue
		}
		// Compared as JSON VALUES, not as strings: the platform round-trips a
		// definition through its own marshaller, so key order and whitespace differ
		// from what was sent. A byte comparison would report every unchanged rule as
		// changed and rewrite it on every bootstrap.
		if existing.Name == r.Name && existing.Enabled == r.Enabled && sameJSON(existing.Definition, r.Definition) {
			return nil
		}
		var updated struct {
			UpdateDetectionRule struct {
				Token string `json:"token"`
			} `json:"updateDetectionRule"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationUpdateDetectionRule,
			map[string]any{"token": r.Token, "request": req}, &updated); err != nil {
			return fmt.Errorf("updateDetectionRule: %w", err)
		}
		log.Info().Str("token", r.Token).Str("profile", profileToken).Msg("updated detection rule")
		return nil
	}

	var created struct {
		CreateDetectionRule struct {
			Token string `json:"token"`
		} `json:"createDetectionRule"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDetectionRule,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createDetectionRule: %w", err)
	}
	log.Info().Str("token", r.Token).Str("profile", profileToken).Bool("enabled", r.Enabled).
		Msg("created detection rule")
	return nil
}

// sameJSON reports whether two JSON documents carry the same value, ignoring key
// order and formatting. Either side failing to parse means "not the same" — an
// unparseable stored definition is precisely one that should be rewritten.
func sameJSON(a, b string) bool {
	var av, bv any
	if json.Unmarshal([]byte(a), &av) != nil || json.Unmarshal([]byte(b), &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// --- device type ---------------------------------------------------------------

const queryDeviceTypesByToken = `query($tokens:[String!]!){deviceTypesByToken(tokens:$tokens){token}}`

const mutationCreateDeviceType = `mutation($request:DeviceTypeCreateRequest){` +
	`createDeviceType(request:$request){token}}`

func ensureDeviceType(ctx context.Context, rt *Runtime, dt DeviceTypeSpec) error {
	var existing struct {
		DeviceTypesByToken []struct {
			Token string `json:"token"`
		} `json:"deviceTypesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDeviceTypesByToken,
		map[string]any{"tokens": []string{dt.Token}}, &existing); err != nil {
		return fmt.Errorf("deviceTypesByToken: %w", err)
	}
	if len(existing.DeviceTypesByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":        dt.Token,
		"name":         dt.Name,
		"profileToken": dt.ProfileToken,
	}
	var created struct {
		CreateDeviceType struct {
			Token string `json:"token"`
		} `json:"createDeviceType"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDeviceType,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createDeviceType: %w", err)
	}
	log.Info().Str("token", dt.Token).Msg("created device type")
	return nil
}

// --- device + credential ---------------------------------------------------------

const queryDevicesByToken = `query($tokens:[String!]!){devicesByToken(tokens:$tokens){token}}`

const mutationCreateDevice = `mutation($request:DeviceCreateRequest){createDevice(request:$request){token}}`

const queryDeviceCredentialsByToken = `query($tokens:[String!]!){deviceCredentialsByToken(tokens:$tokens){token}}`

const mutationCreateDeviceCredential = `mutation($request:DeviceCredentialCreateRequest){` +
	`createDeviceCredential(request:$request){token}}`

// credentialTypeAccessToken is device-management's model.CredentialAccessToken
// value (ADR-014); mirrored here as a literal since dc-simulator only ever
// speaks the wire, not the Go model package.
const credentialTypeAccessToken = "ACCESS_TOKEN"

func ensureDevice(ctx context.Context, rt *Runtime, d DeviceInstance) error {
	var existingDevice struct {
		DevicesByToken []struct {
			Token string `json:"token"`
		} `json:"devicesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDevicesByToken,
		map[string]any{"tokens": []string{d.Token}}, &existingDevice); err != nil {
		return fmt.Errorf("devicesByToken: %w", err)
	}
	if len(existingDevice.DevicesByToken) == 0 {
		req := map[string]any{
			"token":           d.Token,
			"externalId":      d.ExternalId,
			"deviceTypeToken": d.DeviceTypeToken,
		}
		var created struct {
			CreateDevice struct {
				Token string `json:"token"`
			} `json:"createDevice"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDevice,
			map[string]any{"request": req}, &created); err != nil {
			return fmt.Errorf("createDevice: %w", err)
		}
		log.Info().Str("token", d.Token).Str("externalId", d.ExternalId).Msg("created device")
	}

	var existingCred struct {
		DeviceCredentialsByToken []struct {
			Token string `json:"token"`
		} `json:"deviceCredentialsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDeviceCredentialsByToken,
		map[string]any{"tokens": []string{d.CredentialToken}}, &existingCred); err != nil {
		return fmt.Errorf("deviceCredentialsByToken: %w", err)
	}
	if len(existingCred.DeviceCredentialsByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":          d.CredentialToken,
		"deviceToken":    d.Token,
		"credentialType": credentialTypeAccessToken,
		"credentialId":   d.CredentialId,
		"enabled":        true,
	}
	var created struct {
		CreateDeviceCredential struct {
			Token string `json:"token"`
		} `json:"createDeviceCredential"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDeviceCredential,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createDeviceCredential: %w", err)
	}
	log.Info().Str("token", d.CredentialToken).Str("device", d.Token).Msg("created device credential")
	return nil
}

// --- customer/area/asset hierarchy ----------------------------------------------
//
// Same create-or-get-by-token shape as everything above, applied to the three
// entity-type hierarchies device assignments anchor against (ADR-013/044).
// Each instance needs its classifier type created first, exactly like a device
// needs its device type.

const queryCustomerTypesByToken = `query($tokens:[String!]!){customerTypesByToken(tokens:$tokens){token}}`

const mutationCreateCustomerType = `mutation($request:CustomerTypeCreateRequest){` +
	`createCustomerType(request:$request){token}}`

func ensureCustomerType(ctx context.Context, rt *Runtime, ct CustomerTypeSpec) error {
	var existing struct {
		CustomerTypesByToken []struct {
			Token string `json:"token"`
		} `json:"customerTypesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryCustomerTypesByToken,
		map[string]any{"tokens": []string{ct.Token}}, &existing); err != nil {
		return fmt.Errorf("customerTypesByToken: %w", err)
	}
	if len(existing.CustomerTypesByToken) > 0 {
		return nil
	}

	req := map[string]any{"token": ct.Token, "name": ct.Name}
	var created struct {
		CreateCustomerType struct {
			Token string `json:"token"`
		} `json:"createCustomerType"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateCustomerType,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createCustomerType: %w", err)
	}
	log.Info().Str("token", ct.Token).Msg("created customer type")
	return nil
}

const queryCustomersByToken = `query($tokens:[String!]!){customersByToken(tokens:$tokens){token}}`

const mutationCreateCustomer = `mutation($request:CustomerCreateRequest){createCustomer(request:$request){token}}`

func ensureCustomer(ctx context.Context, rt *Runtime, c CustomerSpec) error {
	var existing struct {
		CustomersByToken []struct {
			Token string `json:"token"`
		} `json:"customersByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryCustomersByToken,
		map[string]any{"tokens": []string{c.Token}}, &existing); err != nil {
		return fmt.Errorf("customersByToken: %w", err)
	}
	if len(existing.CustomersByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":             c.Token,
		"name":              c.Name,
		"customerTypeToken": c.CustomerTypeToken,
	}
	var created struct {
		CreateCustomer struct {
			Token string `json:"token"`
		} `json:"createCustomer"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateCustomer,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createCustomer: %w", err)
	}
	log.Info().Str("token", c.Token).Msg("created customer")
	return nil
}

const queryAreaTypesByToken = `query($tokens:[String!]!){areaTypesByToken(tokens:$tokens){token}}`

const mutationCreateAreaType = `mutation($request:AreaTypeCreateRequest){` +
	`createAreaType(request:$request){token}}`

func ensureAreaType(ctx context.Context, rt *Runtime, at AreaTypeSpec) error {
	var existing struct {
		AreaTypesByToken []struct {
			Token string `json:"token"`
		} `json:"areaTypesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryAreaTypesByToken,
		map[string]any{"tokens": []string{at.Token}}, &existing); err != nil {
		return fmt.Errorf("areaTypesByToken: %w", err)
	}
	if len(existing.AreaTypesByToken) > 0 {
		return nil
	}

	req := map[string]any{"token": at.Token, "name": at.Name}
	var created struct {
		CreateAreaType struct {
			Token string `json:"token"`
		} `json:"createAreaType"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateAreaType,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createAreaType: %w", err)
	}
	log.Info().Str("token", at.Token).Msg("created area type")
	return nil
}

const queryAreasByToken = `query($tokens:[String!]!){areasByToken(tokens:$tokens){token}}`

const mutationCreateArea = `mutation($request:AreaCreateRequest){createArea(request:$request){token}}`

func ensureArea(ctx context.Context, rt *Runtime, a AreaSpec) error {
	var existing struct {
		AreasByToken []struct {
			Token string `json:"token"`
		} `json:"areasByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryAreasByToken,
		map[string]any{"tokens": []string{a.Token}}, &existing); err != nil {
		return fmt.Errorf("areasByToken: %w", err)
	}
	if len(existing.AreasByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":         a.Token,
		"name":          a.Name,
		"areaTypeToken": a.AreaTypeToken,
	}
	var created struct {
		CreateArea struct {
			Token string `json:"token"`
		} `json:"createArea"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateArea,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createArea: %w", err)
	}
	log.Info().Str("token", a.Token).Msg("created area")
	return nil
}

const queryAssetTypesByToken = `query($tokens:[String!]!){assetTypesByToken(tokens:$tokens){token}}`

const mutationCreateAssetType = `mutation($request:AssetTypeCreateRequest){` +
	`createAssetType(request:$request){token}}`

func ensureAssetType(ctx context.Context, rt *Runtime, at AssetTypeSpec) error {
	var existing struct {
		AssetTypesByToken []struct {
			Token string `json:"token"`
		} `json:"assetTypesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryAssetTypesByToken,
		map[string]any{"tokens": []string{at.Token}}, &existing); err != nil {
		return fmt.Errorf("assetTypesByToken: %w", err)
	}
	if len(existing.AssetTypesByToken) > 0 {
		return nil
	}

	req := map[string]any{"token": at.Token, "name": at.Name}
	var created struct {
		CreateAssetType struct {
			Token string `json:"token"`
		} `json:"createAssetType"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateAssetType,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createAssetType: %w", err)
	}
	log.Info().Str("token", at.Token).Msg("created asset type")
	return nil
}

const queryAssetsByToken = `query($tokens:[String!]!){assetsByToken(tokens:$tokens){token}}`

const mutationCreateAsset = `mutation($request:AssetCreateRequest){createAsset(request:$request){token}}`

func ensureAsset(ctx context.Context, rt *Runtime, a AssetSpec) error {
	var existing struct {
		AssetsByToken []struct {
			Token string `json:"token"`
		} `json:"assetsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryAssetsByToken,
		map[string]any{"tokens": []string{a.Token}}, &existing); err != nil {
		return fmt.Errorf("assetsByToken: %w", err)
	}
	if len(existing.AssetsByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":          a.Token,
		"name":           a.Name,
		"assetTypeToken": a.AssetTypeToken,
	}
	var created struct {
		CreateAsset struct {
			Token string `json:"token"`
		} `json:"createAsset"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateAsset,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createAsset: %w", err)
	}
	log.Info().Str("token", a.Token).Msg("created asset")
	return nil
}

// --- assignments (tracked EntityRelationships) ----------------------------------

const queryEntityRelationshipsByToken = `query($tokens:[String!]!){` +
	`entityRelationshipsByToken(tokens:$tokens){token}}`

const mutationCreateEntityRelationships = `mutation($requests:[EntityRelationshipCreateRequest!]!){` +
	`createEntityRelationships(requests:$requests){token}}`

// assignmentRelationshipType is device-management's reserved "assigned"
// EntityRelationshipType token (model/api_membership.go's
// AssignmentRelationshipType) — mirrored here as a literal since dc-simulator
// only ever speaks the wire. Using it auto-provisions the type as tracked=true
// on first use in a tenant, which is what makes a device's assignment targets
// show up as anchors on its events (event_resolver.go's deviceAnchors).
const assignmentRelationshipType = "assigned"

// ensureAssignments create-or-gets every device's rendered Assignment set in
// one batched existence check plus one bulk createEntityRelationships call —
// preferred over per-relationship round-trips per the wire facts (bulk create
// is transactional). Devices with no Assignments (e.g. devicepulse, which
// declares no customers/areas) make this a no-op.
func ensureAssignments(ctx context.Context, rt *Runtime, devices []DeviceInstance) error {
	type pendingAssignment struct {
		deviceToken string
		assignment  Assignment
	}
	var all []pendingAssignment
	for _, d := range devices {
		for _, a := range d.Assignments {
			all = append(all, pendingAssignment{deviceToken: d.Token, assignment: a})
		}
	}
	if len(all) == 0 {
		return nil
	}

	tokens := make([]string, len(all))
	for i, p := range all {
		tokens[i] = p.assignment.RelationshipToken
	}
	var existing struct {
		EntityRelationshipsByToken []struct {
			Token string `json:"token"`
		} `json:"entityRelationshipsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryEntityRelationshipsByToken,
		map[string]any{"tokens": tokens}, &existing); err != nil {
		return fmt.Errorf("entityRelationshipsByToken: %w", err)
	}
	existingTokens := make(map[string]bool, len(existing.EntityRelationshipsByToken))
	for _, r := range existing.EntityRelationshipsByToken {
		existingTokens[r.Token] = true
	}

	var missing []pendingAssignment
	for _, p := range all {
		if !existingTokens[p.assignment.RelationshipToken] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	requests := make([]map[string]any, len(missing))
	for i, p := range missing {
		requests[i] = map[string]any{
			"token":            p.assignment.RelationshipToken,
			"sourceType":       "device",
			"source":           p.deviceToken,
			"targetType":       p.assignment.TargetType,
			"target":           p.assignment.TargetToken,
			"relationshipType": assignmentRelationshipType,
		}
	}
	var created struct {
		CreateEntityRelationships []struct {
			Token string `json:"token"`
		} `json:"createEntityRelationships"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateEntityRelationships,
		map[string]any{"requests": requests}, &created); err != nil {
		return fmt.Errorf("createEntityRelationships: %w", err)
	}
	log.Info().Int("count", len(missing)).Msg("created assignment relationships")
	return nil
}

// --- dashboards ------------------------------------------------------------------

const queryDashboardByToken = `query($token:String!){dashboard(token:$token){token}}`

const queryDashboardVersions = `query($token:String!){dashboardVersions(token:$token){version}}`

const mutationCreateDashboard = `mutation($request:DashboardCreateRequest!){` +
	`createDashboard(request:$request){token}}`

// expectedUpdatedAt is omitted, so the update is UNCONDITIONAL (no optimistic-
// concurrency precondition) — the sim owns its dashboard and always writes the
// current spec, it isn't reconciling against a concurrent human editor.
const mutationUpdateDashboard = `mutation($token:String!,$request:DashboardCreateRequest!){` +
	`updateDashboard(token:$token,request:$request){token}}`

const mutationPublishDashboard = `mutation($token:String!){publishDashboard(token:$token){version}}`

// ensureDashboard makes a dashboard match the spec (create-or-UPDATE the draft),
// then publishes it if it has no published version yet. Updating rather than
// create-or-getting is deliberate: createDashboard is create-or-get, so a
// pre-existing dashboard (e.g. from an earlier sim run, or one left behind because
// deleting the tenant does not yet cascade dashboard-management rows) would keep its
// stale definition forever and a changed sim spec — a new layout, the fill-area
// grid-schema cutover — would silently never reach the viewer. The console renders
// the mutable DRAFT, so syncing the draft is what makes the current spec visible;
// the publish gate only ensures at least one published version exists (publishing
// on every run would pile up redundant versions since publish does not de-dup).
func ensureDashboard(ctx context.Context, rt *Runtime, ds DashboardSpec) error {
	var existing struct {
		Dashboard *struct {
			Token string `json:"token"`
		} `json:"dashboard"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DashboardMgmtGraphQL, queryDashboardByToken,
		map[string]any{"token": ds.Token}, &existing); err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}
	req := map[string]any{
		"token":       ds.Token,
		"name":        ds.Name,
		"description": ds.Description,
		"definition":  ds.Definition,
	}
	if existing.Dashboard == nil {
		var created struct {
			CreateDashboard struct {
				Token string `json:"token"`
			} `json:"createDashboard"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DashboardMgmtGraphQL, mutationCreateDashboard,
			map[string]any{"request": req}, &created); err != nil {
			return fmt.Errorf("createDashboard: %w", err)
		}
		log.Info().Str("token", ds.Token).Msg("created dashboard")
	} else {
		var updated struct {
			UpdateDashboard struct {
				Token string `json:"token"`
			} `json:"updateDashboard"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DashboardMgmtGraphQL, mutationUpdateDashboard,
			map[string]any{"token": ds.Token, "request": req}, &updated); err != nil {
			return fmt.Errorf("updateDashboard: %w", err)
		}
		log.Info().Str("token", ds.Token).Msg("updated dashboard")
	}

	var versions struct {
		DashboardVersions []struct {
			Version int `json:"version"`
		} `json:"dashboardVersions"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DashboardMgmtGraphQL, queryDashboardVersions,
		map[string]any{"token": ds.Token}, &versions); err != nil {
		return fmt.Errorf("dashboardVersions: %w", err)
	}
	if len(versions.DashboardVersions) > 0 {
		return nil
	}

	var published struct {
		PublishDashboard struct {
			Version int `json:"version"`
		} `json:"publishDashboard"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DashboardMgmtGraphQL, mutationPublishDashboard,
		map[string]any{"token": ds.Token}, &published); err != nil {
		return fmt.Errorf("publishDashboard: %w", err)
	}
	log.Info().Str("token", ds.Token).Int("version", published.PublishDashboard.Version).
		Msg("published dashboard")
	return nil
}
