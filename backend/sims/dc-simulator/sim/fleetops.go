// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// The fleet operations a scenario provisions beside its devices: dynamic device groups and the
// SHARED attributes their selectors read, installed positions, the asset hierarchy, typed
// relationships, maintenance windows and campaigns.
//
// Like everything in bootstrap.go each is a check-then-create-or-converge step keyed by token, so
// a re-run is a no-op except for what is missing or changed. Two of them are not editable on the
// platform and are converged by replacing them: a campaign (delete and create; the platform has no
// update), and a group's published version (a changed selector is a new version, never an edit
// of the old one, so a rule pinned to it moves only when the profile republishes).

// EntityGroupSpec is one DYNAMIC group of devices: a CEL selector over the devices' SHARED
// attributes, published so that a rule can be scoped to it and a campaign can target it.
type EntityGroupSpec struct {
	Token       string
	Name        string
	Description string
	// Selector is the membership predicate, for example attr["quay"] == "east".
	Selector string
}

// AttributeSpec is one SHARED attribute written on a device. ValueType is STRING, LONG, DOUBLE or
// BOOLEAN; Value is its text ("true" for a BOOLEAN).
type AttributeSpec struct {
	Key       string
	ValueType string
	Value     string
}

// PositionSpec is where a device is installed, WGS84 decimal degrees.
type PositionSpec struct {
	Latitude  float64
	Longitude float64
}

// RelationshipTypeSpec is one typed relationship (createEntityRelationshipType). It is
// provisioned untracked: a tracked type stamps its targets onto every event of the source as
// anchors, which these graph edges are not.
type RelationshipTypeSpec struct {
	Token       string
	Name        string
	Description string
}

// RelationshipSpec is one edge of a typed relationship.
type RelationshipSpec struct {
	Token      string
	Type       string
	SourceType string
	Source     string
	TargetType string
	Target     string
}

// MaintenanceWindowSpec is one maintenance window: weekly slots in an IANA zone during which a
// device type's disruptive commands may start.
type MaintenanceWindowSpec struct {
	Token       string
	Name        string
	Description string
	Timezone    string
	Slots       []WindowSlot
}

// WindowSlot is one weekly opening, as the platform's slots document holds it.
type WindowSlot struct {
	Day             string `json:"day"`
	Start           string `json:"start"`
	DurationMinutes int    `json:"durationMinutes"`
}

// CampaignSpec is one campaign: a trigger that plans command steps for every device of a group.
type CampaignSpec struct {
	Token      string
	Name       string
	GroupToken string
	Trigger    CampaignTrigger
	Steps      []CampaignStepSpec
	// MaxLatenessSeconds and ExpiresInSeconds are 0 for the platform's defaults.
	MaxLatenessSeconds int
	ExpiresInSeconds   int
}

// CampaignTrigger is a campaign trigger, in the shape the platform stores it (core/schedule's
// Spec). A hand mirror: this process is an untrusted client and cannot import the owner's type.
type CampaignTrigger struct {
	Kind          string   `json:"kind"`
	Zone          string   `json:"zone"`
	Cron          string   `json:"cron,omitempty"`
	Dates         []string `json:"dates,omitempty"`
	Times         []string `json:"times,omitempty"`
	Event         string   `json:"event,omitempty"`
	OffsetMinutes int      `json:"offsetMinutes,omitempty"`
	Holidays      []string `json:"holidays,omitempty"`
}

// CampaignStepSpec is one COMMAND step.
type CampaignStepSpec struct {
	Command      string
	Payload      map[string]any
	DelaySeconds int
}

// The wire vocabulary this file sends. Hand mirrors; each is SENT, so a drift fails at the write.
const (
	groupMemberDevice     = "device"
	groupMembershipDyn    = "dynamic"
	attributeScopeShared  = "SHARED"
	campaignKindCalendar  = "CALENDAR"
	campaignKindSolar     = "SOLAR"
	campaignStepCommand   = "COMMAND"
	solarEventSunrise     = "SUNRISE"
	solarEventSunset      = "SUNSET"
	impactNormal          = "NORMAL"
	impactDisruptive      = "DISRUPTIVE"
	impactDestructive     = "DESTRUCTIVE"
	entityTypeAsset       = "asset"
	entityTypeDevice      = "device"
	positionFamilyDevice  = "DEVICE"
	positionSourceSurvey  = "SURVEY"
	maxCampaignSteps      = 10
	maxWindowSlots        = 56
	maxWindowSlotDuration = 10080
)

var attributeValueTypes = map[string]bool{"STRING": true, "LONG": true, "DOUBLE": true, "BOOLEAN": true}

var windowDays = map[string]bool{
	"MONDAY": true, "TUESDAY": true, "WEDNESDAY": true, "THURSDAY": true, "FRIDAY": true, "SATURDAY": true, "SUNDAY": true,
}

// validateFleetOps checks the fleet operations the manifest declares and every reference to them.
func (m SimManifest) validateFleetOps(typeTokens, assetTokens map[string]bool) error {
	seen := map[string]bool{}
	groups := map[string]bool{}
	for _, g := range m.EntityGroups {
		if err := validateTokenOnce(seen, "entity group", g.Token); err != nil {
			return err
		}
		if strings.TrimSpace(g.Selector) == "" {
			return fmt.Errorf("entity group %q has no selector: a dynamic group with none matches nothing", g.Token)
		}
		groups[g.Token] = true
	}
	commandKeys := map[string]bool{}
	for _, p := range m.Profiles {
		for _, c := range p.Commands {
			commandKeys[c.CommandKey] = true
			switch c.Impact {
			case "", impactNormal, impactDisruptive, impactDestructive:
			default:
				return fmt.Errorf("command %q has impact %q, not NORMAL, DISRUPTIVE or DESTRUCTIVE", c.Token, c.Impact)
			}
		}
		for _, r := range p.DetectionRules {
			if r.Group != "" && !groups[r.Group] {
				return fmt.Errorf("detection rule %q is scoped to group %q, which the manifest does not declare", r.Token, r.Group)
			}
		}
	}
	windows := map[string]bool{}
	for _, w := range m.MaintenanceWindows {
		if err := validateTokenOnce(seen, "maintenance window", w.Token); err != nil {
			return err
		}
		if err := validateSlots(w.Slots); err != nil {
			return fmt.Errorf("maintenance window %q: %w", w.Token, err)
		}
		if strings.TrimSpace(w.Timezone) == "" || w.Timezone == "Local" {
			return fmt.Errorf("maintenance window %q needs an IANA time zone", w.Token)
		}
		windows[w.Token] = true
	}
	for _, dt := range m.DeviceTypes {
		if dt.MaintenanceWindowToken != "" && !windows[dt.MaintenanceWindowToken] {
			return fmt.Errorf("device type %q names maintenance window %q, which the manifest does not declare", dt.Token, dt.MaintenanceWindowToken)
		}
	}
	// The asset hierarchy: every parent declared, and no cycle (the platform refuses one, after
	// the assets exist).
	parents := map[string]string{}
	for _, a := range m.Assets {
		if a.Parent == "" {
			continue
		}
		if !assetTokens[a.Parent] {
			return fmt.Errorf("asset %q has unknown parent %q", a.Token, a.Parent)
		}
		parents[a.Token] = a.Parent
	}
	for start := range parents {
		at, steps := start, 0
		for parents[at] != "" {
			at = parents[at]
			if steps++; at == start || steps > len(parents) {
				return fmt.Errorf("asset %q is its own ancestor", start)
			}
		}
	}
	devices := map[string]bool{}
	for _, d := range m.Expand(m.Seed) {
		devices[d.Token] = true
		keys := map[string]bool{}
		for _, a := range d.Attributes {
			if a.Key == "" || keys[a.Key] {
				return fmt.Errorf("device %q has an empty or repeated attribute %q", d.Token, a.Key)
			}
			keys[a.Key] = true
			if !attributeValueTypes[a.ValueType] {
				return fmt.Errorf("device %q attribute %q has value type %q", d.Token, a.Key, a.ValueType)
			}
		}
		if p := d.Position; p != nil {
			if math.IsNaN(p.Latitude) || math.IsNaN(p.Longitude) || math.Abs(p.Latitude) > 90 || math.Abs(p.Longitude) > 180 {
				return fmt.Errorf("device %q is installed at %v, which is not on the globe", d.Token, *p)
			}
		}
	}
	relTypes := map[string]bool{}
	for _, rt := range m.RelationshipTypes {
		if err := validateTokenOnce(seen, "relationship type", rt.Token); err != nil {
			return err
		}
		if rt.Token == assignmentRelationshipType {
			return fmt.Errorf("relationship type %q is the platform's own assignment type", rt.Token)
		}
		relTypes[rt.Token] = true
	}
	exists := func(kind, token string) bool {
		switch kind {
		case entityTypeAsset:
			return assetTokens[token]
		case entityTypeDevice:
			return devices[token]
		}
		return false
	}
	for _, r := range m.Relationships {
		if err := validateTokenOnce(seen, "relationship", r.Token); err != nil {
			return err
		}
		if !relTypes[r.Type] {
			return fmt.Errorf("relationship %q has type %q, which the manifest does not declare", r.Token, r.Type)
		}
		if !exists(r.SourceType, r.Source) || !exists(r.TargetType, r.Target) {
			return fmt.Errorf("relationship %q joins %s %q to %s %q, and the manifest does not declare both",
				r.Token, r.SourceType, r.Source, r.TargetType, r.Target)
		}
	}
	for _, c := range m.Campaigns {
		if err := validateTokenOnce(seen, "campaign", c.Token); err != nil {
			return err
		}
		if err := c.validate(groups, commandKeys); err != nil {
			return err
		}
	}
	return nil
}

func validateSlots(slots []WindowSlot) error {
	if len(slots) == 0 || len(slots) > maxWindowSlots {
		return fmt.Errorf("%d slots, want 1..%d", len(slots), maxWindowSlots)
	}
	for _, s := range slots {
		if !windowDays[s.Day] {
			return fmt.Errorf("slot day %q is not MONDAY..SUNDAY", s.Day)
		}
		if len(s.Start) != 5 || s.Start[2] != ':' {
			return fmt.Errorf("slot start %q is not HH:MM", s.Start)
		}
		if s.DurationMinutes < 1 || s.DurationMinutes > maxWindowSlotDuration {
			return fmt.Errorf("slot duration %d minutes is outside 1..%d", s.DurationMinutes, maxWindowSlotDuration)
		}
	}
	return nil
}

// validate checks one campaign against the groups and command keys the manifest declares. The
// platform is the judge of the trigger itself; this refuses what would plan nothing or send a
// command no device of the manifest accepts.
func (c CampaignSpec) validate(groups, commandKeys map[string]bool) error {
	if !groups[c.GroupToken] {
		return fmt.Errorf("campaign %q targets group %q, which the manifest does not declare", c.Token, c.GroupToken)
	}
	if len(c.Steps) == 0 || len(c.Steps) > maxCampaignSteps {
		return fmt.Errorf("campaign %q has %d steps, want 1..%d", c.Token, len(c.Steps), maxCampaignSteps)
	}
	for _, s := range c.Steps {
		if !commandKeys[s.Command] {
			return fmt.Errorf("campaign %q sends %q, which no profile of the manifest declares", c.Token, s.Command)
		}
	}
	if c.Trigger.Zone == "" || c.Trigger.Zone == "Local" {
		return fmt.Errorf("campaign %q needs an IANA zone", c.Token)
	}
	switch c.Trigger.Kind {
	case campaignKindCalendar:
		if len(c.Trigger.Dates) == 0 || len(c.Trigger.Times) == 0 {
			return fmt.Errorf("calendar campaign %q needs dates and times", c.Token)
		}
	case campaignKindSolar:
		if c.Trigger.Event != solarEventSunrise && c.Trigger.Event != solarEventSunset {
			return fmt.Errorf("solar campaign %q has event %q", c.Token, c.Trigger.Event)
		}
	default:
		return fmt.Errorf("campaign %q has trigger kind %q; this package provisions CALENDAR and SOLAR", c.Token, c.Trigger.Kind)
	}
	return nil
}

// triggerJSON is the trigger document createCampaign takes.
func (c CampaignSpec) triggerJSON() string {
	raw, err := json.Marshal(c.Trigger)
	if err != nil {
		panic(fmt.Sprintf("sim: marshal campaign trigger: %v", err)) // strings and ints cannot fail
	}
	return string(raw)
}

// stepInputs renders the steps as createCampaign takes them.
func (c CampaignSpec) stepInputs() []map[string]any {
	out := make([]map[string]any, 0, len(c.Steps))
	for _, s := range c.Steps {
		step := map[string]any{"type": campaignStepCommand, "name": s.Command, "delaySeconds": s.DelaySeconds}
		if s.Payload != nil {
			raw, err := json.Marshal(s.Payload)
			if err != nil {
				panic(fmt.Sprintf("sim: marshal campaign payload: %v", err))
			}
			step["payload"] = string(raw)
		}
		out = append(out, step)
	}
	return out
}

func (c CampaignSpec) createRequest() map[string]any {
	req := map[string]any{
		"token": c.Token, "name": c.Name, "trigger": c.triggerJSON(), "steps": c.stepInputs(),
		"groupToken": c.GroupToken, "targetKind": "GROUP",
	}
	if c.MaxLatenessSeconds > 0 {
		req["maxLatenessSeconds"] = c.MaxLatenessSeconds
	}
	if c.ExpiresInSeconds > 0 {
		req["expiresInSeconds"] = c.ExpiresInSeconds
	}
	return req
}

// ---- provisioning ------------------------------------------------------------------------------

const (
	queryEntityGroupsByToken = `query($tokens:[String!]!){entityGroupsByToken(tokens:$tokens){` +
		`token memberType membershipMode selector activeVersion versions{version selector}}}`

	mutationCreateEntityGroup = `mutation($request:EntityGroupCreateRequest){createEntityGroup(request:$request){token}}`

	mutationUpdateEntityGroup = `mutation($token:String!,$request:EntityGroupUpdateRequest!){` +
		`updateEntityGroup(token:$token,request:$request){token}}`

	mutationPublishEntityGroup = `mutation($token:String!,$label:String){publishEntityGroup(token:$token,label:$label){version}}`

	queryDeviceAttributes = `query($c:EntityAttributeSearchCriteria!){entityAttributes(criteria:$c){` +
		`results{entity{token} attrKey valueType value} pagination{totalRecords}}}`

	mutationSetEntityAttribute = `mutation($request:EntityAttributeSetRequest!){setEntityAttribute(request:$request){attrKey}}`

	queryInstalledPositions = `query($tokens:[String!]!){devicesByToken(tokens:$tokens){token installedPosition{latitude longitude}}}`

	mutationSetInstalledPosition = `mutation($family:PositionFamily!,$token:String!,$position:InstalledPositionInput!){` +
		`setInstalledPosition(family:$family,token:$token,position:$position){latitude longitude}}`

	queryAssetParent = `query($token:String!){assetParent(token:$token){token}}`

	mutationSetAssetParent = `mutation($child:String!,$parent:String!){setAssetParent(childToken:$child,parentToken:$parent){token}}`

	queryRelationshipTypesByToken = `query($tokens:[String!]!){entityRelationshipTypesByToken(tokens:$tokens){token}}`

	mutationCreateRelationshipType = `mutation($request:EntityRelationshipTypeCreateRequest!){` +
		`createEntityRelationshipType(request:$request){token}}`

	queryMaintenanceWindowsByToken = `query($tokens:[String!]!){maintenanceWindowsByToken(tokens:$tokens){token timezone slots}}`

	mutationCreateMaintenanceWindow = `mutation($request:MaintenanceWindowCreateRequest!){createMaintenanceWindow(request:$request){token}}`

	mutationUpdateMaintenanceWindow = `mutation($token:String!,$request:MaintenanceWindowUpdateRequest!){` +
		`updateMaintenanceWindow(token:$token,request:$request){token}}`

	queryCampaign = `query($token:String!){campaign(token:$token){token groupToken trigger maxLatenessSeconds expiresInSeconds ` +
		`steps{type name payload delaySeconds}}}`

	queryCampaigns = `query($limit:Int){campaigns(limit:$limit){token}}`

	mutationCreateCampaign = `mutation($request:CampaignCreateRequest!){createCampaign(request:$request){` +
		`campaign{token} rejection{code reason}}}`

	mutationDeleteCampaign = `mutation($token:String!){deleteCampaign(token:$token)}`
)

// ensureEntityGroup creates the dynamic group, converges its selector, publishes it when its
// active version is not this selector, and records the active version for the rules scoped to it.
func ensureEntityGroup(ctx context.Context, rt *Runtime, g EntityGroupSpec) error {
	url := rt.Endpoints.DeviceMgmtGraphQL
	type groupInfo struct {
		Token          string  `json:"token"`
		MemberType     string  `json:"memberType"`
		MembershipMode string  `json:"membershipMode"`
		Selector       *string `json:"selector"`
		ActiveVersion  *int    `json:"activeVersion"`
		Versions       []struct {
			Version  int    `json:"version"`
			Selector string `json:"selector"`
		} `json:"versions"`
	}
	read := func() (*groupInfo, error) {
		var out struct {
			EntityGroupsByToken []groupInfo `json:"entityGroupsByToken"`
		}
		if err := rt.Session.Query(ctx, url, queryEntityGroupsByToken, map[string]any{"tokens": []string{g.Token}}, &out); err != nil {
			return nil, fmt.Errorf("entityGroupsByToken: %w", err)
		}
		if len(out.EntityGroupsByToken) == 0 {
			return nil, nil
		}
		return &out.EntityGroupsByToken[0], nil
	}
	existing, err := read()
	if err != nil {
		return err
	}
	switch {
	case existing == nil:
		var out struct {
			CreateEntityGroup struct {
				Token string `json:"token"`
			} `json:"createEntityGroup"`
		}
		req := map[string]any{"token": g.Token, "name": g.Name, "description": g.Description,
			"memberType": groupMemberDevice, "membershipMode": groupMembershipDyn, "selector": g.Selector}
		if err := rt.Session.Query(ctx, url, mutationCreateEntityGroup, map[string]any{"request": req}, &out); err != nil {
			return fmt.Errorf("createEntityGroup: %w", err)
		}
		log.Info().Str("token", g.Token).Msg("created entity group")
	case existing.MemberType != groupMemberDevice || existing.MembershipMode != groupMembershipDyn:
		// Member family and mode are identity: an update cannot change them.
		return fmt.Errorf("entity group %q exists as a %s group of %ss; the scenario declares a dynamic group of devices",
			g.Token, existing.MembershipMode, existing.MemberType)
	case existing.Selector == nil || *existing.Selector != g.Selector:
		var out struct {
			UpdateEntityGroup struct {
				Token string `json:"token"`
			} `json:"updateEntityGroup"`
		}
		req := map[string]any{"name": g.Name, "description": g.Description, "selector": g.Selector}
		if err := rt.Session.Query(ctx, url, mutationUpdateEntityGroup, map[string]any{"token": g.Token, "request": req}, &out); err != nil {
			return fmt.Errorf("updateEntityGroup: %w", err)
		}
		log.Info().Str("token", g.Token).Msg("updated entity group selector")
	}
	if existing, err = read(); err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("entity group %q is not readable after it was written", g.Token)
	}
	active := 0
	if existing.ActiveVersion != nil {
		active = *existing.ActiveVersion
	}
	published := false
	for _, v := range existing.Versions {
		published = published || (v.Version == active && v.Selector == g.Selector)
	}
	if !published {
		var out struct {
			PublishEntityGroup struct {
				Version int `json:"version"`
			} `json:"publishEntityGroup"`
		}
		if err := rt.Session.Query(ctx, url, mutationPublishEntityGroup, map[string]any{"token": g.Token, "label": "sim"}, &out); err != nil {
			return fmt.Errorf("publishEntityGroup: %w", err)
		}
		active = out.PublishEntityGroup.Version
		log.Info().Str("token", g.Token).Int("version", active).Msg("published entity group")
	}
	if rt.GroupVersions == nil {
		rt.GroupVersions = map[string]int{}
	}
	rt.GroupVersions[g.Token] = active
	return nil
}

// ensureDeviceAttributes writes every declared SHARED attribute whose stored value differs. One
// read of the tenant's SHARED device attributes answers the whole comparison.
func ensureDeviceAttributes(ctx context.Context, rt *Runtime, devices []DeviceInstance) error {
	want := 0
	for _, d := range devices {
		want += len(d.Attributes)
	}
	if want == 0 {
		return nil
	}
	url := rt.Endpoints.DeviceMgmtGraphQL
	have := map[string]AttributeSpec{}
	for page := 1; page <= 50; page++ {
		var out struct {
			EntityAttributes struct {
				Results []struct {
					Entity struct {
						Token string `json:"token"`
					} `json:"entity"`
					AttrKey   string  `json:"attrKey"`
					ValueType string  `json:"valueType"`
					Value     *string `json:"value"`
				} `json:"results"`
			} `json:"entityAttributes"`
		}
		c := map[string]any{"pageNumber": page, "pageSize": 1000, "entityType": entityTypeDevice, "scope": attributeScopeShared}
		if err := rt.Session.Query(ctx, url, queryDeviceAttributes, map[string]any{"c": c}, &out); err != nil {
			return fmt.Errorf("entityAttributes: %w", err)
		}
		for _, a := range out.EntityAttributes.Results {
			v := ""
			if a.Value != nil {
				v = *a.Value
			}
			have[a.Entity.Token+"\x00"+a.AttrKey] = AttributeSpec{Key: a.AttrKey, ValueType: a.ValueType, Value: v}
		}
		if len(out.EntityAttributes.Results) < 1000 {
			break
		}
	}
	written := 0
	for _, d := range devices {
		for _, a := range d.Attributes {
			if have[d.Token+"\x00"+a.Key] == a {
				continue
			}
			var out struct {
				SetEntityAttribute struct {
					AttrKey string `json:"attrKey"`
				} `json:"setEntityAttribute"`
			}
			req := map[string]any{"entityType": entityTypeDevice, "entity": d.Token, "scope": attributeScopeShared,
				"attrKey": a.Key, "valueType": a.ValueType, "value": a.Value}
			if err := rt.Session.Query(ctx, url, mutationSetEntityAttribute, map[string]any{"request": req}, &out); err != nil {
				return fmt.Errorf("setEntityAttribute %s.%s: %w", d.Token, a.Key, err)
			}
			written++
		}
	}
	if written > 0 {
		log.Info().Int("count", written).Msg("wrote device attributes")
	}
	return nil
}

// ensureInstalledPositions records each declared installed position that is missing or moved.
func ensureInstalledPositions(ctx context.Context, rt *Runtime, devices []DeviceInstance) error {
	var tokens []string
	for _, d := range devices {
		if d.Position != nil {
			tokens = append(tokens, d.Token)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	url := rt.Endpoints.DeviceMgmtGraphQL
	var out struct {
		DevicesByToken []struct {
			Token             string `json:"token"`
			InstalledPosition *struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"installedPosition"`
		} `json:"devicesByToken"`
	}
	if err := rt.Session.Query(ctx, url, queryInstalledPositions, map[string]any{"tokens": tokens}, &out); err != nil {
		return fmt.Errorf("devicesByToken (installed positions): %w", err)
	}
	have := map[string]PositionSpec{}
	for _, d := range out.DevicesByToken {
		if d.InstalledPosition != nil {
			have[d.Token] = PositionSpec{Latitude: d.InstalledPosition.Latitude, Longitude: d.InstalledPosition.Longitude}
		}
	}
	for _, d := range devices {
		if d.Position == nil {
			continue
		}
		if h, ok := have[d.Token]; ok && math.Abs(h.Latitude-d.Position.Latitude) < 1e-9 && math.Abs(h.Longitude-d.Position.Longitude) < 1e-9 {
			continue
		}
		var set struct {
			SetInstalledPosition struct {
				Latitude float64 `json:"latitude"`
			} `json:"setInstalledPosition"`
		}
		vars := map[string]any{"family": positionFamilyDevice, "token": d.Token, "position": map[string]any{
			"latitude": d.Position.Latitude, "longitude": d.Position.Longitude, "source": positionSourceSurvey}}
		if err := rt.Session.Query(ctx, url, mutationSetInstalledPosition, vars, &set); err != nil {
			return fmt.Errorf("setInstalledPosition %s: %w", d.Token, err)
		}
		log.Info().Str("device", d.Token).Msg("recorded installed position")
	}
	return nil
}

// ensureAssetParents places every asset under its declared parent, after every asset exists.
func ensureAssetParents(ctx context.Context, rt *Runtime, assets []AssetSpec) error {
	url := rt.Endpoints.DeviceMgmtGraphQL
	for _, a := range assets {
		if a.Parent == "" {
			continue
		}
		var out struct {
			AssetParent *struct {
				Token string `json:"token"`
			} `json:"assetParent"`
		}
		if err := rt.Session.Query(ctx, url, queryAssetParent, map[string]any{"token": a.Token}, &out); err != nil {
			return fmt.Errorf("assetParent %s: %w", a.Token, err)
		}
		if out.AssetParent != nil && out.AssetParent.Token == a.Parent {
			continue
		}
		var set struct {
			SetAssetParent struct {
				Token string `json:"token"`
			} `json:"setAssetParent"`
		}
		if err := rt.Session.Query(ctx, url, mutationSetAssetParent, map[string]any{"child": a.Token, "parent": a.Parent}, &set); err != nil {
			return fmt.Errorf("setAssetParent %s under %s: %w", a.Token, a.Parent, err)
		}
		log.Info().Str("asset", a.Token).Str("parent", a.Parent).Msg("placed asset")
	}
	return nil
}

// ensureRelationshipType creates an untracked relationship type that is missing.
func ensureRelationshipType(ctx context.Context, rt *Runtime, t RelationshipTypeSpec) error {
	url := rt.Endpoints.DeviceMgmtGraphQL
	var existing struct {
		EntityRelationshipTypesByToken []struct {
			Token string `json:"token"`
		} `json:"entityRelationshipTypesByToken"`
	}
	if err := rt.Session.Query(ctx, url, queryRelationshipTypesByToken, map[string]any{"tokens": []string{t.Token}}, &existing); err != nil {
		return fmt.Errorf("entityRelationshipTypesByToken: %w", err)
	}
	if len(existing.EntityRelationshipTypesByToken) > 0 {
		return nil
	}
	var out struct {
		CreateEntityRelationshipType struct {
			Token string `json:"token"`
		} `json:"createEntityRelationshipType"`
	}
	req := map[string]any{"token": t.Token, "name": t.Name, "description": t.Description, "tracked": false}
	if err := rt.Session.Query(ctx, url, mutationCreateRelationshipType, map[string]any{"request": req}, &out); err != nil {
		return fmt.Errorf("createEntityRelationshipType: %w", err)
	}
	log.Info().Str("token", t.Token).Msg("created relationship type")
	return nil
}

// ensureRelationships creates every declared relationship that is missing, in one batch. A
// relationship's token names both of its ends, so an edge that moved is a new token.
func ensureRelationships(ctx context.Context, rt *Runtime, rels []RelationshipSpec) error {
	if len(rels) == 0 {
		return nil
	}
	url := rt.Endpoints.DeviceMgmtGraphQL
	tokens := make([]string, len(rels))
	for i, r := range rels {
		tokens[i] = r.Token
	}
	var existing struct {
		EntityRelationshipsByToken []struct {
			Token string `json:"token"`
		} `json:"entityRelationshipsByToken"`
	}
	if err := rt.Session.Query(ctx, url, queryEntityRelationshipsByToken, map[string]any{"tokens": tokens}, &existing); err != nil {
		return fmt.Errorf("entityRelationshipsByToken: %w", err)
	}
	have := map[string]bool{}
	for _, r := range existing.EntityRelationshipsByToken {
		have[r.Token] = true
	}
	var requests []map[string]any
	for _, r := range rels {
		if have[r.Token] {
			continue
		}
		requests = append(requests, map[string]any{"token": r.Token, "sourceType": r.SourceType, "source": r.Source,
			"targetType": r.TargetType, "target": r.Target, "relationshipType": r.Type})
	}
	if len(requests) == 0 {
		return nil
	}
	var created struct {
		CreateEntityRelationships []struct {
			Token string `json:"token"`
		} `json:"createEntityRelationships"`
	}
	if err := rt.Session.Query(ctx, url, mutationCreateEntityRelationships, map[string]any{"requests": requests}, &created); err != nil {
		return fmt.Errorf("createEntityRelationships: %w", err)
	}
	log.Info().Int("count", len(requests)).Msg("created typed relationships")
	return nil
}

func slotsJSON(slots []WindowSlot) string {
	raw, err := json.Marshal(slots)
	if err != nil {
		panic(fmt.Sprintf("sim: marshal window slots: %v", err))
	}
	return string(raw)
}

// sameSlots compares a stored slots document with the declared slots as values.
func sameSlots(stored string, slots []WindowSlot) bool {
	var have []WindowSlot
	if err := json.Unmarshal([]byte(stored), &have); err != nil || len(have) != len(slots) {
		return false
	}
	for i := range have {
		if have[i] != slots[i] {
			return false
		}
	}
	return true
}

// ensureMaintenanceWindow creates the window, or converges its zone and slots.
func ensureMaintenanceWindow(ctx context.Context, rt *Runtime, w MaintenanceWindowSpec) error {
	url := rt.Endpoints.DeviceMgmtGraphQL
	var existing struct {
		MaintenanceWindowsByToken []struct {
			Token    string `json:"token"`
			Timezone string `json:"timezone"`
			Slots    string `json:"slots"`
		} `json:"maintenanceWindowsByToken"`
	}
	if err := rt.Session.Query(ctx, url, queryMaintenanceWindowsByToken, map[string]any{"tokens": []string{w.Token}}, &existing); err != nil {
		return fmt.Errorf("maintenanceWindowsByToken: %w", err)
	}
	if len(existing.MaintenanceWindowsByToken) == 0 {
		var out struct {
			CreateMaintenanceWindow struct {
				Token string `json:"token"`
			} `json:"createMaintenanceWindow"`
		}
		req := map[string]any{"token": w.Token, "name": w.Name, "description": w.Description,
			"timezone": w.Timezone, "slots": slotsJSON(w.Slots)}
		if err := rt.Session.Query(ctx, url, mutationCreateMaintenanceWindow, map[string]any{"request": req}, &out); err != nil {
			return fmt.Errorf("createMaintenanceWindow: %w", err)
		}
		log.Info().Str("token", w.Token).Msg("created maintenance window")
		return nil
	}
	have := existing.MaintenanceWindowsByToken[0]
	if have.Timezone == w.Timezone && sameSlots(have.Slots, w.Slots) {
		return nil
	}
	return setMaintenanceWindowSlots(ctx, rt, w.Token, w.Timezone, w.Slots)
}

// setMaintenanceWindowSlots rewrites a window's zone and openings. Commands already waiting on the
// window follow the new schedule.
func setMaintenanceWindowSlots(ctx context.Context, rt *Runtime, token, zone string, slots []WindowSlot) error {
	if err := validateSlots(slots); err != nil {
		return fmt.Errorf("maintenance window %q: %w", token, err)
	}
	var out struct {
		UpdateMaintenanceWindow struct {
			Token string `json:"token"`
		} `json:"updateMaintenanceWindow"`
	}
	req := map[string]any{"timezone": zone, "slots": slotsJSON(slots)}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationUpdateMaintenanceWindow,
		map[string]any{"token": token, "request": req}, &out); err != nil {
		return fmt.Errorf("updateMaintenanceWindow: %w", err)
	}
	return nil
}

// storedCampaign is a campaign as the platform holds it.
type storedCampaign struct {
	Token              string `json:"token"`
	GroupToken         string `json:"groupToken"`
	Trigger            string `json:"trigger"`
	MaxLatenessSeconds int    `json:"maxLatenessSeconds"`
	ExpiresInSeconds   int    `json:"expiresInSeconds"`
	Steps              []struct {
		Type         string  `json:"type"`
		Name         *string `json:"name"`
		Payload      *string `json:"payload"`
		DelaySeconds int     `json:"delaySeconds"`
	} `json:"steps"`
}

// matches reports whether the stored campaign is the declared one. Lateness and expiry are
// compared only when declared (0 leaves the platform's default, which this side does not mirror).
func (s storedCampaign) matches(c CampaignSpec) bool {
	if s.GroupToken != c.GroupToken || !sameJSON(s.Trigger, c.triggerJSON()) || len(s.Steps) != len(c.Steps) {
		return false
	}
	if (c.MaxLatenessSeconds > 0 && s.MaxLatenessSeconds != c.MaxLatenessSeconds) ||
		(c.ExpiresInSeconds > 0 && s.ExpiresInSeconds != c.ExpiresInSeconds) {
		return false
	}
	for i, step := range c.stepInputs() {
		have := s.Steps[i]
		if have.Name == nil || *have.Name != step["name"] || have.DelaySeconds != step["delaySeconds"] {
			return false
		}
		want, _ := step["payload"].(string)
		got := ""
		if have.Payload != nil {
			got = *have.Payload
		}
		if (want == "") != (got == "") || (want != "" && !sameJSON(want, got)) {
			return false
		}
	}
	return true
}

// ensureCampaign creates the campaign, or replaces one that differs: the platform has no update,
// so a changed campaign is deleted and created again (which sends nothing that fell due before).
func ensureCampaign(ctx context.Context, rt *Runtime, c CampaignSpec) error {
	url := rt.Endpoints.CommandMgmtGraphQL
	var existing struct {
		Campaign *storedCampaign `json:"campaign"`
	}
	if err := rt.Session.Query(ctx, url, queryCampaign, map[string]any{"token": c.Token}, &existing); err != nil {
		return fmt.Errorf("campaign: %w", err)
	}
	if existing.Campaign != nil {
		if existing.Campaign.matches(c) {
			return nil
		}
		if err := deleteCampaign(ctx, rt, c.Token); err != nil {
			return err
		}
		log.Info().Str("token", c.Token).Msg("replacing a campaign that changed")
	}
	return createCampaign(ctx, rt, c)
}

// createCampaign creates a campaign, failing on the platform's rejection.
func createCampaign(ctx context.Context, rt *Runtime, c CampaignSpec) error {
	var out struct {
		CreateCampaign struct {
			Campaign *struct {
				Token string `json:"token"`
			} `json:"campaign"`
			Rejection *struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"rejection"`
		} `json:"createCampaign"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.CommandMgmtGraphQL, mutationCreateCampaign,
		map[string]any{"request": c.createRequest()}, &out); err != nil {
		return fmt.Errorf("createCampaign %s: %w", c.Token, err)
	}
	if r := out.CreateCampaign.Rejection; r != nil {
		return fmt.Errorf("createCampaign %s refused: %s: %s", c.Token, r.Code, r.Reason)
	}
	if out.CreateCampaign.Campaign == nil {
		return fmt.Errorf("createCampaign %s returned neither a campaign nor a rejection", c.Token)
	}
	log.Info().Str("token", c.Token).Str("kind", c.Trigger.Kind).Str("group", c.GroupToken).Msg("created campaign")
	return nil
}

func deleteCampaign(ctx context.Context, rt *Runtime, token string) error {
	var out struct {
		DeleteCampaign bool `json:"deleteCampaign"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.CommandMgmtGraphQL, mutationDeleteCampaign, map[string]any{"token": token}, &out); err != nil {
		return fmt.Errorf("deleteCampaign %s: %w", token, err)
	}
	return nil
}

// deleteCampaignsWithPrefix deletes the tenant's campaigns whose token starts with one of the
// prefixes: the ones a scenario creates as it runs, left over from an earlier run.
func deleteCampaignsWithPrefix(ctx context.Context, rt *Runtime, prefixes ...string) error {
	var out struct {
		Campaigns []struct {
			Token string `json:"token"`
		} `json:"campaigns"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.CommandMgmtGraphQL, queryCampaigns, map[string]any{"limit": 200}, &out); err != nil {
		return fmt.Errorf("campaigns: %w", err)
	}
	var stale []string
	for _, c := range out.Campaigns {
		for _, p := range prefixes {
			if strings.HasPrefix(c.Token, p) {
				stale = append(stale, c.Token)
				break
			}
		}
	}
	sort.Strings(stale)
	for _, t := range stale {
		if err := deleteCampaign(ctx, rt, t); err != nil {
			return err
		}
	}
	if len(stale) > 0 {
		log.Info().Int("count", len(stale)).Msg("deleted campaigns an earlier run left")
	}
	return nil
}

// requireFleetOpsEndpoints fails before any network call when campaigns are declared and the
// handshake names no command-delivery endpoint.
func requireFleetOpsEndpoints(m SimManifest, e Endpoints) error {
	if len(m.Campaigns) > 0 && strings.TrimSpace(e.CommandMgmtGraphQL) == "" {
		return fmt.Errorf("manifest %q declares campaigns but the handshake has no endpoints.commandMgmtGraphQL: "+
			"re-create the sim with a current dcctl", m.Name)
	}
	return nil
}
