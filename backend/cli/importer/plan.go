// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

// Class says how faithfully something maps.
type Class string

const (
	// Direct carries over unchanged.
	Direct Class = "direct"
	// Transformed carries over after a stated change (renamed, reversed, re-typed).
	Transformed Class = "transformed"
	// Partial carries over with something lost, which the line says.
	Partial Class = "partial"
	// Unsupported is not converted; the line says what the operator redoes.
	Unsupported Class = "unsupported"
)

// Line is one report line.
type Line struct {
	Class   Class  `json:"class"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref,omitempty"`
	Message string `json:"message"`
}

// Limits of the target; longer values are cut and the cut is reported.
const (
	maxName        = 128
	maxDescription = 1024
	maxExternalID  = 256
)

// Plan is everything Apply will send, already in the target's vocabulary.
type Plan struct {
	Profiles   []ProfilePlan   `json:"profiles"`
	AssetTypes []AssetTypePlan `json:"assetTypes"`
	Assets     []AssetPlan     `json:"assets"`
	Devices    []DeviceRow     `json:"devices"`
	RelTypes   []RelTypePlan   `json:"relationshipTypes"`
	Parents    []ParentPlan    `json:"assetParents"`
	Relations  []RelPlan       `json:"relationships"`
	Attributes []AttrPlan      `json:"attributes"`
	Report     []Line          `json:"report"`
}

// ProfilePlan is one source device profile: a DeviceChain profile and the type that adopts it,
// sharing a token.
type ProfilePlan struct {
	Token, Name, Description, Metadata string
}

// AssetTypePlan is one source asset profile.
type AssetTypePlan struct {
	Token, Name, Description, Metadata string
}

// AssetPlan is one asset.
type AssetPlan struct {
	Token, Name, Description, TypeToken, Metadata string
}

// DeviceRow is one importDevices row. A row with a CredentialID goes in FACTORY_CREDENTIALS mode.
type DeviceRow struct {
	Token           string `json:"token"`
	DeviceTypeToken string `json:"deviceTypeToken"`
	ExternalID      string `json:"externalId,omitempty"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	Metadata        string `json:"metadata,omitempty"`
	CredentialType  string `json:"credentialType,omitempty"`
	CredentialID    string `json:"credentialId,omitempty"`
}

// RelTypePlan is a custom (untracked) relationship type.
type RelTypePlan struct{ Token, Name string }

// ParentPlan places a child asset under a parent asset.
type ParentPlan struct{ Child, Parent string }

// RelPlan is one relationship edge.
type RelPlan struct {
	Token, SourceType, Source, TargetType, Target, Type string
}

// AttrPlan is one attribute write.
type AttrPlan struct {
	EntityType, Entity, Scope, Key, ValueType, Value string
}

// Options tunes Build.
type Options struct {
	// CarryAccessTokens imports a device's access-token credential with the device. Off by
	// default: the usual migration re-provisions credentials, because the device's wire format
	// changes anyway and a credential that travelled in a file has been exposed to that file.
	CarryAccessTokens bool
}

// systemAttrKeys are maintained by the source platform itself (connectivity state and update
// delivery); DeviceChain owns the equivalents, so copying them would write stale values.
var systemAttrKeys = map[string]bool{
	"active": true, "lastActivityTime": true, "lastConnectTime": true,
	"lastDisconnectTime": true, "inactivityAlarmTime": true,
	"fw_title": true, "fw_version": true, "fw_checksum": true, "fw_checksum_algorithm": true,
	"fw_size": true, "fw_url": true, "fw_state": true, "fw_ts": true,
	"sw_title": true, "sw_version": true, "sw_checksum": true, "sw_checksum_algorithm": true,
	"sw_size": true, "sw_url": true, "sw_state": true, "sw_ts": true,
}

var scopes = map[string]string{"SERVER_SCOPE": "SERVER", "SHARED_SCOPE": "SHARED", "CLIENT_SCOPE": "CLIENT"}

// uncovered are entity kinds with no converter in this spike; each kind is counted once.
var uncovered = map[string]string{
	"CUSTOMER":       "customers are not migrated; re-create them and their members from this list",
	"USER":           "users are re-invited; no password is exported",
	"DASHBOARD":      "dashboards do not convert; rebuild on the dashboard runtime from the widget/alias catalogue",
	"RULE_CHAIN":     "rule chains do not convert; each is rebuilt as detection rules, actions or notification policy",
	"ENTITY_VIEW":    "entity views have no equivalent; the nearest is a customer-scoped dynamic group (all keys, all time)",
	"OTA_PACKAGE":    "update packages are re-declared in the artifact catalogue from the original files",
	"WIDGET_TYPE":    "custom widgets are rewritten against the widget SDK",
	"WIDGETS_BUNDLE": "custom widgets are rewritten against the widget SDK",
	"TB_RESOURCE":    "resources are not migrated",
}

type builder struct {
	opts    Options
	plan    *Plan
	devices map[string]Entity // by id
	assets  map[string]Entity
	profs   map[string]bool
	atypes  map[string]bool
	extIDs  map[string]string
	counts  map[string]int
}

func (b *builder) add(c Class, kind, ref, msg string, args ...any) {
	b.plan.Report = append(b.plan.Report, Line{Class: c, Kind: kind, Ref: ref, Message: fmt.Sprintf(msg, args...)})
}

func cut(b *builder, kind, ref, field, v string, max int) string {
	if len(v) <= max {
		return v
	}
	b.add(Partial, kind, ref, "%s is %d characters; cut to %d (the full value is kept in metadata)", field, len(v), max)
	return v[:max]
}

func sourceMeta(e Entity, extra map[string]any) string {
	m := map[string]any{"id": e.ID.ID, "createdTime": e.CreatedTime}
	if e.Name != "" {
		m["name"] = e.Name
	}
	if e.Label != "" {
		m["label"] = e.Label
	}
	if len(e.AdditionalInfo) > 0 && string(e.AdditionalInfo) != "null" {
		m["additionalInfo"] = json.RawMessage(e.AdditionalInfo)
	}
	if e.Customer != nil {
		m["customerId"] = e.Customer.ID
	}
	for k, v := range extra {
		m[k] = v
	}
	out, _ := json.Marshal(map[string]any{"source": m})
	return string(out)
}

// Build maps an export onto the target. It touches nothing and needs no connection.
func Build(ex *Export, opts Options) *Plan {
	b := &builder{opts: opts, plan: &Plan{}, devices: map[string]Entity{}, assets: map[string]Entity{},
		profs: map[string]bool{}, atypes: map[string]bool{}, extIDs: map[string]string{}, counts: map[string]int{}}

	type decoded struct {
		rec Record
		ent Entity
	}
	var recs []decoded
	for _, r := range ex.Records {
		e, err := r.entity()
		if err != nil {
			b.add(Unsupported, r.EntityType, "", "entity could not be read: %v", err)
			continue
		}
		recs = append(recs, decoded{r, e})
	}
	// Index first so references resolve whatever order the files arrive in.
	for _, d := range recs {
		switch d.rec.EntityType {
		case "DEVICE_PROFILE":
			b.profs[d.ent.ID.ID] = true
		case "ASSET_PROFILE":
			b.atypes[d.ent.ID.ID] = true
		case "DEVICE":
			b.devices[d.ent.ID.ID] = d.ent
		case "ASSET":
			b.assets[d.ent.ID.ID] = d.ent
		}
	}

	var rels []Relation
	customerRefs := 0
	for _, d := range recs {
		e := d.ent
		ref := e.Name
		if ref == "" {
			ref = e.ID.ID
		}
		switch d.rec.EntityType {
		case "DEVICE_PROFILE":
			b.deviceProfile(e, ref)
		case "ASSET_PROFILE":
			b.assetProfile(e, ref)
		case "ASSET":
			b.asset(e, ref)
			if e.Customer != nil {
				customerRefs++
			}
		case "DEVICE":
			b.device(e, d.rec.Credentials, ref)
			if e.Customer != nil {
				customerRefs++
			}
		default:
			b.counts[d.rec.EntityType]++
		}
		if d.rec.EntityType == "DEVICE" || d.rec.EntityType == "ASSET" {
			b.attributes(d.rec.EntityType, e.ID.ID, ref, d.rec.Attributes)
		} else if len(d.rec.Attributes) > 0 {
			b.add(Unsupported, d.rec.EntityType, ref, "attributes of this entity kind are not migrated")
		}
		rels = append(rels, d.rec.Relations...)
	}
	kinds := make([]string, 0, len(b.counts))
	for k := range b.counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		msg := uncovered[k]
		if msg == "" {
			msg = "no counterpart; not migrated"
		}
		b.add(Unsupported, k, fmt.Sprintf("%d", b.counts[k]), "%s", msg)
	}
	if customerRefs > 0 {
		b.add(Partial, "CUSTOMER", fmt.Sprintf("%d", customerRefs),
			"devices and assets that name a customer keep the customer id in metadata only; ownership is not recreated")
	}
	b.relations(rels)
	if len(b.plan.Devices) > 0 {
		b.add(Transformed, "DEVICE", fmt.Sprintf("%d", len(b.plan.Devices)),
			"the registry moves by tool; the devices speak a different wire (topics, payload envelope, attribute/command/update channels) and need new firmware or a translating hop")
	}
	return b.plan
}

func (b *builder) deviceProfile(e Entity, ref string) {
	if err := core.ValidateToken(e.ID.ID); err != nil {
		b.add(Unsupported, "DEVICE_PROFILE", ref, "id is not a valid token: %v", err)
		delete(b.profs, e.ID.ID)
		return
	}
	extra := map[string]any{}
	if e.Type != "" {
		extra["profileType"] = e.Type
	}
	if e.TransportType != "" {
		extra["transportType"] = e.TransportType
	}
	b.plan.Profiles = append(b.plan.Profiles, ProfilePlan{
		Token: e.ID.ID, Name: cut(b, "DEVICE_PROFILE", ref, "name", e.Name, maxName),
		Description: cut(b, "DEVICE_PROFILE", ref, "description", e.Description, maxDescription),
		Metadata:    sourceMeta(e, extra)})
	b.add(Transformed, "DEVICE_PROFILE", ref, "becomes one device type and one published profile (token %s); the source declares no telemetry keys, so the profile starts with no metrics", e.ID.ID)
	if e.TransportType != "" && e.TransportType != "DEFAULT" {
		b.add(Unsupported, "DEVICE_PROFILE", ref, "transport %q (custom topics, binary payloads or another protocol) has no counterpart; devices need a translating hop or new firmware", e.TransportType)
	}
	if e.ProfileData != nil {
		for i, raw := range e.ProfileData.Alarms {
			c, msg := ClassifyAlarm(raw)
			b.add(c, "ALARM_RULE", fmt.Sprintf("%s#%d", ref, i+1), "%s", msg)
		}
	}
}

func (b *builder) assetProfile(e Entity, ref string) {
	if err := core.ValidateToken(e.ID.ID); err != nil {
		b.add(Unsupported, "ASSET_PROFILE", ref, "id is not a valid token: %v", err)
		delete(b.atypes, e.ID.ID)
		return
	}
	b.plan.AssetTypes = append(b.plan.AssetTypes, AssetTypePlan{
		Token: e.ID.ID, Name: cut(b, "ASSET_PROFILE", ref, "name", e.Name, maxName),
		Description: cut(b, "ASSET_PROFILE", ref, "description", e.Description, maxDescription),
		Metadata:    sourceMeta(e, nil)})
	b.add(Transformed, "ASSET_PROFILE", ref, "becomes an asset type with no property contract")
}

func (b *builder) asset(e Entity, ref string) {
	if err := core.ValidateToken(e.ID.ID); err != nil {
		b.add(Unsupported, "ASSET", ref, "id is not a valid token: %v", err)
		delete(b.assets, e.ID.ID)
		return
	}
	if e.AssetProfile == nil || !b.atypes[e.AssetProfile.ID] {
		b.add(Unsupported, "ASSET", ref, "its asset profile is not in the export; not imported")
		delete(b.assets, e.ID.ID)
		return
	}
	b.plan.Assets = append(b.plan.Assets, AssetPlan{
		Token: e.ID.ID, Name: cut(b, "ASSET", ref, "name", e.Name, maxName),
		Description: cut(b, "ASSET", ref, "description", e.Label, maxDescription),
		TypeToken:   e.AssetProfile.ID, Metadata: sourceMeta(e, nil)})
}

func (b *builder) device(e Entity, cr *Credentials, ref string) {
	if err := core.ValidateToken(e.ID.ID); err != nil {
		b.add(Unsupported, "DEVICE", ref, "id is not a valid token: %v", err)
		delete(b.devices, e.ID.ID)
		return
	}
	if e.DeviceProfile == nil || !b.profs[e.DeviceProfile.ID] {
		b.add(Unsupported, "DEVICE", ref, "its device profile is not in the export; not imported")
		delete(b.devices, e.ID.ID)
		return
	}
	row := DeviceRow{Token: e.ID.ID, DeviceTypeToken: e.DeviceProfile.ID,
		Name:        cut(b, "DEVICE", ref, "name", e.Name, maxName),
		Description: cut(b, "DEVICE", ref, "description", e.Label, maxDescription),
		Metadata:    sourceMeta(e, nil)}
	if e.Name != "" {
		// The source's device names are unique per tenant, so the name is safe as the business
		// identifier and keeps the name-to-device link a gateway relied on recoverable.
		if len(e.Name) > maxExternalID {
			b.add(Partial, "DEVICE", ref, "name is longer than %d characters; no external id set", maxExternalID)
		} else if other, dup := b.extIDs[e.Name]; dup {
			b.add(Partial, "DEVICE", ref, "name repeats device %s; no external id set", other)
		} else {
			b.extIDs[e.Name] = e.ID.ID
			row.ExternalID = e.Name
		}
	}
	switch {
	case cr == nil:
		b.add(Transformed, "CREDENTIAL", ref, "no credential in the export; issue one after import")
	case cr.Type == "ACCESS_TOKEN" && b.opts.CarryAccessTokens && cr.ID != "":
		row.CredentialType, row.CredentialID = "ACCESS_TOKEN", cr.ID
		b.add(Direct, "CREDENTIAL", ref, "access token carried over (stored as a keyed digest)")
	case cr.Type == "ACCESS_TOKEN":
		b.add(Transformed, "CREDENTIAL", ref, "access token not carried (--carry-access-tokens is off); re-provision")
	case cr.Type == "MQTT_BASIC":
		b.add(Partial, "CREDENTIAL", ref, "basic MQTT credential not carried: passwords under 16 bytes and client-id-only logins are refused by the target; re-provision")
	case cr.Type == "X509_CERTIFICATE":
		b.add(Unsupported, "CREDENTIAL", ref, "certificate credential not carried by this spike; pin the certificate through the certificate-credential flow after import")
	default:
		b.add(Unsupported, "CREDENTIAL", ref, "credential type %q has no counterpart", cr.Type)
	}
	b.plan.Devices = append(b.plan.Devices, row)
}

func (b *builder) attributes(kind, id, ref string, attrs map[string][]Attribute) {
	if _, ok := b.devices[id]; kind == "DEVICE" && !ok {
		return
	}
	if _, ok := b.assets[id]; kind == "ASSET" && !ok {
		return
	}
	et := strings.ToLower(kind)
	scopeNames := make([]string, 0, len(attrs))
	for s := range attrs {
		scopeNames = append(scopeNames, s)
	}
	sort.Strings(scopeNames)
	skipped, shared := 0, 0
	for _, s := range scopeNames {
		scope, ok := scopes[s]
		if !ok {
			b.add(Unsupported, "ATTRIBUTE", ref, "scope %q is not a known scope; %d attribute(s) skipped", s, len(attrs[s]))
			continue
		}
		for _, a := range attrs[s] {
			if systemAttrKeys[a.Key] {
				skipped++
				continue
			}
			vt, v, ok := attrValue(a)
			if !ok {
				b.add(Unsupported, "ATTRIBUTE", ref, "%s has no value; skipped", a.Key)
				continue
			}
			if scope == "SHARED" && kind == "DEVICE" {
				shared++
			}
			b.plan.Attributes = append(b.plan.Attributes, AttrPlan{EntityType: et, Entity: id, Scope: scope, Key: a.Key, ValueType: vt, Value: v})
		}
	}
	if skipped > 0 {
		b.add(Transformed, "ATTRIBUTE", ref, "%d platform-maintained attribute(s) (connectivity state, update delivery) not copied; the target owns them", skipped)
	}
	if shared > 0 {
		b.add(Partial, "ATTRIBUTE", ref, "%d shared attribute(s) written; the device sees one only if its profile declares the key as configuration", shared)
	}
}

func attrValue(a Attribute) (vt, v string, ok bool) {
	switch {
	case a.BooleanValue != nil:
		return "BOOLEAN", fmt.Sprintf("%t", *a.BooleanValue), true
	case a.LongValue != nil:
		return "LONG", fmt.Sprintf("%d", *a.LongValue), true
	case a.DoubleValue != nil:
		return "DOUBLE", fmt.Sprintf("%v", *a.DoubleValue), true
	case a.JSONValue != nil:
		return "JSON", compactJSON(*a.JSONValue), true
	case a.StrValue != nil:
		return "STRING", *a.StrValue, true
	}
	return untypedValue(a.Value)
}

// slug turns a source relation type into a token.
func slug(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	out := sb.String()
	if out == "" || out[0] == '_' || out[0] == '-' {
		out = "rel" + out
	}
	if len(out) > maxName {
		out = out[:maxName]
	}
	return out
}

func relToken(from, to, typ string) string {
	h := sha256.Sum256([]byte(from + "\x00" + to + "\x00" + typ))
	return "rel-" + hex.EncodeToString(h[:])[:32]
}

func (b *builder) kindOf(id EntityID) (string, bool) {
	switch id.EntityType {
	case "DEVICE":
		_, ok := b.devices[id.ID]
		return "device", ok
	case "ASSET":
		_, ok := b.assets[id.ID]
		return "asset", ok
	}
	return "", false
}

func (b *builder) relations(rels []Relation) {
	type key struct{ f, t, ty, g string }
	seen := map[key]bool{}
	var uniq []Relation
	for _, r := range rels {
		k := key{r.From.ID, r.To.ID, r.Type, r.TypeGroup}
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, r)
	}
	// Deterministic: a child's kept parent is the lowest parent id, so every run picks alike.
	sort.SliceStable(uniq, func(i, j int) bool {
		if uniq[i].From.ID != uniq[j].From.ID {
			return uniq[i].From.ID < uniq[j].From.ID
		}
		return uniq[i].To.ID < uniq[j].To.ID
	})
	parentOf := map[string]string{}
	types := map[string]string{} // slug -> original
	var typeOrder []string
	addType := func(slugged, orig string) {
		if prev, ok := types[slugged]; ok {
			if prev != orig {
				b.add(Partial, "RELATION_TYPE", orig, "type collides with %q after renaming to %q; the two are merged", prev, slugged)
			}
			return
		}
		types[slugged] = orig
		typeOrder = append(typeOrder, slugged)
	}
	for _, r := range uniq {
		ref := fmt.Sprintf("%s %s %s", r.From.ID, r.Type, r.To.ID)
		if r.TypeGroup != "" && r.TypeGroup != "COMMON" {
			b.add(Unsupported, "RELATION", ref, "relation group %q is not migrated", r.TypeGroup)
			continue
		}
		fk, fok := b.kindOf(r.From)
		tk, tok := b.kindOf(r.To)
		if !fok || !tok {
			b.add(Unsupported, "RELATION", ref, "an end is not a migrated device or asset (%s -> %s)", r.From.EntityType, r.To.EntityType)
			continue
		}
		contains := strings.EqualFold(r.Type, "Contains")
		switch {
		case contains && fk == "asset" && tk == "asset":
			if p, has := parentOf[r.To.ID]; has {
				addType("contains-extra", "Contains")
				b.plan.Relations = append(b.plan.Relations, RelPlan{Token: relToken(r.From.ID, r.To.ID, "contains-extra"),
					SourceType: "asset", Source: r.From.ID, TargetType: "asset", Target: r.To.ID, Type: "contains-extra"})
				b.add(Partial, "RELATION", ref, "asset already sits under %s (the target allows one parent); kept as a plain 'contains-extra' relationship", p)
				continue
			}
			parentOf[r.To.ID] = r.From.ID
			b.plan.Parents = append(b.plan.Parents, ParentPlan{Child: r.To.ID, Parent: r.From.ID})
		case contains && fk == "asset" && tk == "device":
			b.plan.Relations = append(b.plan.Relations, RelPlan{Token: relToken(r.To.ID, r.From.ID, "assigned"),
				SourceType: "device", Source: r.To.ID, TargetType: "asset", Target: r.From.ID, Type: "assigned"})
			b.add(Transformed, "RELATION", ref, "becomes a tracked 'assigned' edge device -> asset (direction reversed)")
		default:
			s := slug(r.Type)
			addType(s, r.Type)
			b.plan.Relations = append(b.plan.Relations, RelPlan{Token: relToken(r.From.ID, r.To.ID, s),
				SourceType: fk, Source: r.From.ID, TargetType: tk, Target: r.To.ID, Type: s})
			b.add(Transformed, "RELATION", ref, "custom relationship type %q (untracked)", s)
		}
	}
	for _, s := range typeOrder {
		b.plan.RelTypes = append(b.plan.RelTypes, RelTypePlan{Token: s, Name: types[s]})
	}
	if n := len(b.plan.Parents); n > 0 {
		b.add(Transformed, "RELATION", fmt.Sprintf("%d", n), "asset 'Contains' asset becomes the asset hierarchy (one parent each)")
	}
}

// ClassifyAlarm says whether one source alarm rule could become a detection rule, and why not
// when it cannot. The spike reports; it does not convert.
func ClassifyAlarm(raw json.RawMessage) (Class, string) {
	var a struct {
		AlarmType   string                     `json:"alarmType"`
		CreateRules map[string]json.RawMessage `json:"createRules"`
		ClearRule   json.RawMessage            `json:"clearRule"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return Unsupported, "alarm rule could not be read"
	}
	name := a.AlarmType
	var notes []string
	class := Transformed
	if len(a.CreateRules) == 0 {
		return Unsupported, fmt.Sprintf("%q has no create rule", name)
	}
	sevs := make([]string, 0, len(a.CreateRules))
	for s := range a.CreateRules {
		sevs = append(sevs, s)
	}
	sort.Strings(sevs)
	for _, sev := range sevs {
		var rule struct {
			Condition struct {
				Condition []struct {
					Key struct {
						Type string `json:"type"`
						Key  string `json:"key"`
					} `json:"key"`
					Predicate struct {
						Type  string `json:"type"`
						Value struct {
							Dynamic json.RawMessage `json:"dynamicValue"`
						} `json:"value"`
					} `json:"predicate"`
				} `json:"condition"`
				Spec struct {
					Type string `json:"type"`
				} `json:"spec"`
			} `json:"condition"`
			Schedule json.RawMessage `json:"schedule"`
		}
		if err := json.Unmarshal(a.CreateRules[sev], &rule); err != nil {
			return Unsupported, fmt.Sprintf("%q: rule could not be read", name)
		}
		if len(rule.Schedule) > 0 && string(rule.Schedule) != "null" {
			class = worse(class, Unsupported)
			notes = append(notes, sev+": active schedule has no counterpart")
		}
		conds := rule.Condition.Condition
		switch {
		case len(conds) != 1:
			class = worse(class, Partial)
			notes = append(notes, fmt.Sprintf("%s: %d conditions need a CEL condition written by hand", sev, len(conds)))
		case conds[0].Key.Type != "TIME_SERIES" && conds[0].Key.Type != "ATTRIBUTE":
			class = worse(class, Partial)
			notes = append(notes, sev+": a "+conds[0].Key.Type+" key needs a CEL condition")
		case conds[0].Predicate.Type != "NUMERIC":
			class = worse(class, Partial)
			notes = append(notes, sev+": a "+strings.ToLower(conds[0].Predicate.Type)+" predicate needs a CEL condition (booleans are stored as 0/1)")
		default:
			spec := rule.Condition.Spec.Type
			if spec != "SIMPLE" && spec != "DURATION" && spec != "REPEATING" {
				class = worse(class, Partial)
				notes = append(notes, sev+": condition kind "+spec+" is not mapped")
			} else {
				notes = append(notes, sev+": numeric "+strings.ToLower(spec)+" on "+conds[0].Key.Key+" maps to a threshold/duration/repeating rule")
			}
			if d := conds[0].Predicate.Value.Dynamic; len(d) > 0 && string(d) != "null" {
				notes = append(notes, "threshold read from an attribute (maps only for shared or server attributes of the device itself)")
			}
		}
	}
	if len(a.ClearRule) > 0 && string(a.ClearRule) != "null" {
		class = worse(class, Partial)
		notes = append(notes, "separate clear condition (hysteresis) is lost; the target clears on the falling edge of the same condition")
	}
	return class, fmt.Sprintf("%q: %s [not converted in this spike]", name, strings.Join(notes, "; "))
}

func worse(a, b Class) Class {
	rank := map[Class]int{Direct: 0, Transformed: 1, Partial: 2, Unsupported: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Summary counts report lines by class.
func Summary(lines []Line) map[Class]int {
	m := map[Class]int{}
	for _, l := range lines {
		m[l.Class]++
	}
	return m
}

// untypedValue types a REST-style attribute value by its JSON shape: true/false is a boolean, a
// number with no fraction or exponent is a long, any other number is a double, a string is a
// string, and an object or array is JSON. null and absent are no value.
func untypedValue(raw json.RawMessage) (vt, v string, ok bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", "", false
	}
	switch s[0] {
	case '"':
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return "", "", false
		}
		return "STRING", str, true
	case '{', '[':
		return "JSON", compactJSON(s), true
	case 't', 'f':
		return "BOOLEAN", s, s == "true" || s == "false"
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return "LONG", s, true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return "DOUBLE", s, true
	}
	return "", "", false
}

// compactJSON drops the insignificant whitespace an export is pretty-printed with, so a stored
// JSON attribute does not carry the source file's indentation or line endings. Text that is not
// valid JSON is returned unchanged.
func compactJSON(s string) string {
	var b bytes.Buffer
	if json.Compact(&b, []byte(s)) != nil {
		return s
	}
	return b.String()
}
