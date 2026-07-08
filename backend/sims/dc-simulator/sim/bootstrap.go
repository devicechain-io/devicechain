// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

// Provision runs the manifest's whole provisioning chain against
// device-management's (and, for dashboards, dashboard-management's) tenant
// GraphQL API, in the order a real scenario's references demand:
//
//  1. customer/area/asset classifier types, then their instances
//  2. device profile(s) (+ metrics + alarm defs) -> publish
//  3. device type(s)
//  4. devices (+ credentials)
//  5. assignments (bulk createEntityRelationships, "assigned" type)
//  6. dashboards (create -> publish)
//
// Every step checks-then-creates by token (a *ByToken query first, a create
// only if it comes back empty), so re-running Provision against an already-
// provisioned tenant is a no-op except for whatever is genuinely missing —
// this is what makes `reset` an idempotent re-Bootstrap rather than a
// drop-and-recreate. Alarm defs are created before publish (phase 2's own
// ordering, inside ensureProfile) since ADR-045's draft is inert until publish
// — the active version's snapshot must already include them.
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

type deviceProfileInfo struct {
	Token         string `json:"token"`
	ActiveVersion *int   `json:"activeVersion"`
}

const queryDeviceProfilesByToken = `query($tokens:[String!]!){` +
	`deviceProfilesByToken(tokens:$tokens){token activeVersion}}`

const mutationCreateDeviceProfile = `mutation($request:DeviceProfileCreateRequest){` +
	`createDeviceProfile(request:$request){token activeVersion}}`

const mutationPublishDeviceProfile = `mutation($token:String!){` +
	`publishDeviceProfile(token:$token){version}}`

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

	for _, m := range p.Metrics {
		if err := ensureMetricDefinition(ctx, rt, p.Token, m); err != nil {
			return err
		}
	}
	for _, a := range p.Alarms {
		// Alarm defs must exist before publishDeviceProfile below — ADR-045's
		// draft is inert until publish, so the active version's snapshot has to
		// already include them, exactly like the metrics loop just above.
		if err := ensureAlarmDefinition(ctx, rt, p.Token, a); err != nil {
			return err
		}
	}

	if profile.ActiveVersion == nil {
		var published struct {
			PublishDeviceProfile struct {
				Version int `json:"version"`
			} `json:"publishDeviceProfile"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationPublishDeviceProfile,
			map[string]any{"token": p.Token}, &published); err != nil {
			return fmt.Errorf("publishDeviceProfile: %w", err)
		}
		log.Info().Str("token", p.Token).Int("version", published.PublishDeviceProfile.Version).
			Msg("published device profile")
	}
	return nil
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

func ensureMetricDefinition(ctx context.Context, rt *Runtime, profileToken string, m MetricSpec) error {
	metricToken := profileToken + "-" + m.Key
	var existing struct {
		MetricDefinitionsByToken []struct {
			Token string `json:"token"`
		} `json:"metricDefinitionsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryMetricDefinitionsByToken,
		map[string]any{"tokens": []string{metricToken}}, &existing); err != nil {
		return fmt.Errorf("metricDefinitionsByToken: %w", err)
	}
	if len(existing.MetricDefinitionsByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":              metricToken,
		"deviceProfileToken": profileToken,
		"metricKey":          m.Key,
		"name":               m.Name,
		"dataType":           m.DataType,
		"unit":               m.Unit,
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

const queryAlarmDefinitionsByToken = `query($tokens:[String!]!){` +
	`alarmDefinitionsByToken(tokens:$tokens){token}}`

const mutationCreateAlarmDefinition = `mutation($request:AlarmDefinitionCreateRequest!){` +
	`createAlarmDefinition(request:$request){token}}`

// ensureAlarmDefinition create-or-gets one alarm definition on profileToken.
// Its token is derived the same way ensureMetricDefinition derives a metric's
// (profileToken + "-" + a stable key), here keyed by AlarmKey — Validate checks
// this exact derivation so a grammar-unsafe alarm key fails fast.
func ensureAlarmDefinition(ctx context.Context, rt *Runtime, profileToken string, a AlarmDefSpec) error {
	alarmToken := profileToken + "-" + a.AlarmKey
	var existing struct {
		AlarmDefinitionsByToken []struct {
			Token string `json:"token"`
		} `json:"alarmDefinitionsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryAlarmDefinitionsByToken,
		map[string]any{"tokens": []string{alarmToken}}, &existing); err != nil {
		return fmt.Errorf("alarmDefinitionsByToken: %w", err)
	}
	if len(existing.AlarmDefinitionsByToken) > 0 {
		return nil
	}

	req := map[string]any{
		"token":               alarmToken,
		"deviceProfileToken":  profileToken,
		"alarmKey":            a.AlarmKey,
		"metricKey":           a.MetricKey,
		"name":                a.Name,
		"description":         a.Description,
		"conditionType":       a.ConditionType,
		"operator":            a.Operator,
		"severity":            a.Severity,
		"threshold":           a.Threshold,
		"thresholdAttr":       a.ThresholdAttr,
		"durationSeconds":     a.DurationSeconds,
		"repeatCount":         a.RepeatCount,
		"repeatWindowSeconds": a.RepeatWindowSeconds,
		"enabled":             a.Enabled,
	}
	var created struct {
		CreateAlarmDefinition struct {
			Token string `json:"token"`
		} `json:"createAlarmDefinition"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateAlarmDefinition,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createAlarmDefinition: %w", err)
	}
	log.Info().Str("token", alarmToken).Str("profile", profileToken).Msg("created alarm definition")
	return nil
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
