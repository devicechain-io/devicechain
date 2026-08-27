// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
)

// entity is one creatable thing: how to make it, and how to read it back.
//
// 🔑 THE TABLE IS THE COVERAGE CLAIM. Everything this tool asserts is scoped to
// what is listed here, so it is written as DATA and printed by `apiprobe
// coverage` — an entity nobody added is then a row somebody can count, rather
// than absent code nobody can see. A tool that silently covers a fraction of the
// API and reports PASS is the vacuous check this whole rig exists to argue
// against.
//
// ⚠️ IT IS AN ORDERED SEQUENCE, NOT A SET. A device type needs a profile, a
// device needs a type, a policy needs a channel. Later entries read earlier
// tokens out of the state, so reordering the slice breaks the run — which is why
// dependencies are named in each entry's comment rather than inferred.
type entity struct {
	// Name is the stable identifier on the receipt. Never reuse or rename one:
	// a receipt written before an upgrade is read after it, so a renamed entity
	// reads as one entity missing and one unexpected.
	Name string

	// Area is the functional area whose GraphQL endpoint serves it, i.e. the
	// `<area>` in /api/<area>/graphql.
	Area string

	// Mutation is the create field, Input the FULL GraphQL type of its variable
	// (written out with its own `!` and brackets rather than decorated here, so
	// the entry says exactly what the schema says), and Arg its argument name —
	// "request" everywhere except the bulk-relationship create, which takes
	// `requests`.
	Mutation string
	Input    string
	Arg      string

	// Read is the read-back query field.
	Read string

	// Single marks a read-back that returns a NULLABLE OBJECT rather than the
	// usual non-null list.
	//
	// 🔴 THE LIST FORM IS NOT UNIVERSAL, WHICH AN EARLIER DRAFT OF THIS TOOL
	// ASSUMED. dashboard(token:) and connector(token:) are single nullable
	// lookups; everything else is a *sByToken(tokens:[…]) list. Both give
	// "missing" an exact definition — an empty list, or a null — but they are
	// different JSON, and inferring which one arrived would turn a schema that
	// changed KIND into a silent pass instead of a SHAPE finding.
	Single bool

	// Publish marks a version-producing publish op rather than a create. See
	// publishes.go: the mutation takes scalar arguments instead of an input object,
	// and the read-back is a list keyed by the PARENT's token.
	Publish bool

	// Bulk marks a create that returns a LIST of created objects rather than one.
	// Every element is recorded separately, so a bulk create of N devices is N
	// receipt rows and N read-backs — not one row standing in for N.
	Bulk bool

	// Wrap is the sub-key the created object sits under, for a create that
	// returns a result envelope instead of the object ({command, rejection}).
	// Reject is the sibling selection to ask for at the same time, so a refusal
	// arrives with its reason attached rather than as a bare absent object.
	Wrap   string
	Reject string

	// Fields is the selection set, used VERBATIM in BOTH documents.
	//
	// 🔑 IT IS ONE STRING BECAUSE THE COMPARISON IS BETWEEN THE TWO RESPONSES.
	// A read that selected fewer fields than the create would narrow what this
	// tool can detect, and narrow it INVISIBLY — the missing field simply never
	// differs. Sharing the string makes that structurally impossible instead of
	// leaving it to a test to notice.
	//
	// ⚠️ Select only fields that are STORED. A derived one (a DeviceProfile's
	// deviceTypeCount rises when a type adopts it; a Command's status advances as
	// it is delivered) changes for reasons that are not data loss, and would
	// report a healthy upgrade as MISMATCH.
	Fields string

	// Vars builds the create input. It receives the run state so an entry can
	// reference a token an earlier entry produced.
	Vars func(*state) map[string]any

	// Record, when set, stores tokens from the created object into the state so
	// later entities can reference them. Most entries need none.
	Record func(*state, map[string]any)
}

func (e entity) arg() string {
	if e.Arg == "" {
		return "request"
	}
	return e.Arg
}

// createDoc renders the create mutation. Both documents are GENERATED from
// Fields rather than written out, which is what keeps the two selections
// identical; see the note on Fields.
func (e entity) createDoc() string {
	if e.Publish {
		// Uniform across all four publishes, so it is rendered rather than spelled
		// out per op — see publishes.go. `description` is deliberately not sent: it
		// defaults to the parent's, and sending one would make the comparison assert
		// this tool's own literal rather than what publishing preserved.
		return "mutation($token:String!,$label:String){" + e.Mutation +
			"(token:$token,label:$label){" + e.Fields + "}}"
	}
	selection := e.Fields
	if e.Wrap != "" {
		selection = e.Wrap + "{" + e.Fields + "} " + e.Reject
	}
	return "mutation($req:" + e.Input + "){" + e.Mutation + "(" + e.arg() + ":$req){" + selection + "}}"
}

// readDoc renders the read-back query, in whichever of the two shapes the schema
// actually serves.
func (e entity) readDoc() string {
	if e.Publish {
		// Keyed by the PARENT token like a Single read, but returns a LIST like the
		// default one — the third shape the schemas actually serve.
		return "query($token:String!){" + e.Read + "(token:$token){" + e.Fields + "}}"
	}
	if e.Single {
		return "query($token:String!){" + e.Read + "(token:$token){" + e.Fields + "}}"
	}
	return "query($token:String!){" + e.Read + "(tokens:[$token]){" + e.Fields + "}}"
}

// state carries what earlier entities produced into the ones that depend on them.
type state struct {
	tenant string
	tokens map[string]string
}

func newState(tenant string) *state {
	return &state{tenant: tenant, tokens: map[string]string{}}
}

// tok returns a deterministic token for an entity. Deterministic rather than
// random deliberately: a receipt is read by a DIFFERENT process after an
// upgrade, and a value that has to survive that is easier to debug when it can
// be predicted from the entity name alone.
func (s *state) tok(name string) string { return "apiprobe-" + name }

// record stores the created object's token under key, for later entries to read.
func record(key string) func(*state, map[string]any) {
	return func(s *state, o map[string]any) { s.tokens[key] = str(o["token"]) }
}

// meta is the metadata document every entity carries: one key naming the entity
// it belongs to, so a row found by hand in the database says what wrote it.
func meta(name string) string { return `{"probe":"` + name + `"}` }

// probeMetricKey is the metric the probe declares and the probe's detection rule
// then references. They are the same string on purpose: a rule pointing at a
// metric nobody declared would still write successfully, and the pair is more
// representative of a real profile than two unrelated entries would be.
const probeMetricKey = "temp"

// probeCommandKey is the command the probe declares on the profile and then
// issues, for the same reason.
const probeCommandKey = "reboot"

// The coverage table — every tenant-plane create mutation the platform serves.
var entities = []entity{
	// ---- device-management: the profile spine ----------------------------
	{
		// The spine's root: everything a device does is declared on its profile.
		Name:     "device-profile",
		Area:     "device-management",
		Mutation: "createDeviceProfile",
		Input:    "DeviceProfileCreateRequest!",
		Read:     "deviceProfilesByToken",
		// deviceTypeCount is deliberately NOT selected: it counts the types that
		// adopt this profile, so the very next entry would change it.
		Fields: "token name description category metadata activeVersion",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("device-profile"),
				"name":        "apiprobe profile",
				"description": "Written by apiprobe; read back to prove the upgrade preserved it.",
				"category":    "probe",
				"metadata":    meta("device-profile"),
			}}
		},
		Record: record("profile"),
	},
	{
		// Depends on: device-profile.
		Name:     "metric-definition",
		Area:     "device-management",
		Mutation: "createMetricDefinition",
		Input:    "MetricDefinitionCreateRequest!",
		Read:     "metricDefinitionsByToken",
		Fields:   "token name description metricKey dataType unit minValue maxValue enum descriptor metadata deviceProfile{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":              s.tok("metric-definition"),
				"deviceProfileToken": s.tokens["profile"],
				"metricKey":          probeMetricKey,
				"name":               "Temperature",
				"description":        "The metric the probe's detection rule reads.",
				"dataType":           "DOUBLE",
				"unit":               "C",
				"minValue":           -40,
				"maxValue":           125,
				"metadata":           meta("metric-definition"),
			}}
		},
	},
	{
		// Depends on: device-profile. Its key is what the probe's command names.
		Name:     "command-definition",
		Area:     "device-management",
		Mutation: "createCommandDefinition",
		Input:    "CommandDefinitionCreateRequest!",
		Read:     "commandDefinitionsByToken",
		Fields:   "token name description commandKey parameterSchema metadata deviceProfile{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":              s.tok("command-definition"),
				"deviceProfileToken": s.tokens["profile"],
				"commandKey":         probeCommandKey,
				"name":               "Reboot",
				"description":        "The command the probe later enqueues.",
				// 🔴 NOT a JSON Schema. parameterSchema is an ORDERED
				// []CommandParameter (ADR-043), and the field's GraphQL type is
				// String — so a JSON-Schema document is accepted by the schema,
				// by every document check here, and by nothing on a cluster. The
				// bounds are carried deliberately: they make the stored jsonb a
				// nested document rather than a flat one, which is what the
				// upgrade has to bring across intact.
				"parameterSchema": `[{"name":"delaySeconds","description":"Delay before the reboot.",` +
					`"kind":"SCALAR","dataType":"INT","required":true,"minValue":0,"maxValue":3600}]`,
				"metadata": meta("command-definition"),
			}}
		},
	},
	{
		// Depends on: device-profile. Authored DISABLED: the probe is checking
		// that the rule document survives, not running the engine, and an enabled
		// rule left behind by a drill is a live alarm source in the drill tenant.
		Name:     "detection-rule",
		Area:     "device-management",
		Mutation: "createDetectionRule",
		Input:    "DetectionRuleCreateRequest!",
		Read:     "detectionRulesByToken",
		Fields:   "token name description definition authoringGraph enabled metadata entityGroupToken entityGroupVersion deviceProfile{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":              s.tok("detection-rule"),
				"deviceProfileToken": s.tokens["profile"],
				"name":               "Overheating",
				"description":        "Inert by design; the probe asserts the document, not the engine.",
				"definition":         `{"type":"threshold","metric":"` + probeMetricKey + `","op":">","value":30}`,
				"enabled":            false,
				"metadata":           meta("detection-rule"),
			}}
		},
	},
	{
		Name:     "geo-fence",
		Area:     "device-management",
		Mutation: "createGeoFence",
		Input:    "GeoFenceCreateRequest!",
		Read:     "geoFencesByToken",
		// kind is derived from the geometry document rather than stored beside
		// it, so selecting it also asserts the derivation still agrees.
		Fields: "token name description geometry kind metadata",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("geo-fence"),
				"name":        "apiprobe yard",
				"description": "A closed WGS84 ring; the first and last coordinate are the same point.",
				"geometry": `{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":` +
					`[[[-90.0,35.0],[-90.0,35.01],[-89.99,35.01],[-89.99,35.0],[-90.0,35.0]]]}}`,
				"metadata": meta("geo-fence"),
			}}
		},
	},

	// ---- device-management: devices --------------------------------------
	{
		// Depends on: device-profile (adopts it, un-fused).
		Name:     "device-type",
		Area:     "device-management",
		Mutation: "createDeviceType",
		Input:    "DeviceTypeCreateRequest!",
		Read:     "deviceTypesByToken",
		Fields:   "token name description imageUrl icon backgroundColor foregroundColor borderColor manufacturer model metadata profile{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":           s.tok("device-type"),
				"name":            "apiprobe type",
				"description":     "Adopts the apiprobe profile.",
				"profileToken":    s.tokens["profile"],
				"imageUrl":        "https://example.invalid/apiprobe.png",
				"icon":            "cpu",
				"backgroundColor": "#101828",
				"foregroundColor": "#ffffff",
				"borderColor":     "#475467",
				"manufacturer":    "apiprobe",
				"model":           "P-1",
				"metadata":        meta("device-type"),
			}}
		},
		Record: record("deviceType"),
	},
	{
		// Depends on: device-type.
		Name:     "device",
		Area:     "device-management",
		Mutation: "createDevice",
		Input:    "DeviceCreateRequest!",
		Read:     "devicesByToken",
		Fields:   "token externalId name description metadata deviceType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":           s.tok("device"),
				"externalId":      "apiprobe-ext-1",
				"name":            "apiprobe device",
				"description":     "One device, to prove the spine survived.",
				"deviceTypeToken": s.tokens["deviceType"],
				"metadata":        meta("device"),
			}}
		},
		Record: record("device"),
	},
	{
		// Depends on: device-type. The bulk path renders tokens from a template
		// rather than taking them, so this is the one entry whose tokens the
		// probe does not choose — which is exactly why every rendered device is
		// recorded separately instead of one standing in for the batch.
		Name:     "devices-bulk",
		Area:     "device-management",
		Mutation: "createDevices",
		Input:    "DeviceBulkCreateRequest!",
		Read:     "devicesByToken",
		Bulk:     true,
		Fields:   "token externalId name description metadata deviceType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"deviceTypeToken": s.tokens["deviceType"],
				"count":           3,
				"startIndex":      1,
				// {random} is refused in a token template precisely so a token
				// stays reproducible; the probe needs that property twice over,
				// since verify re-derives nothing and reads the receipt instead.
				"tokenTemplate":      "apiprobe-bulk-{n:03d}",
				"nameTemplate":       "apiprobe bulk {n}",
				"externalIdTemplate": "apiprobe-bulk-ext-{n}",
				"metadata":           meta("devices-bulk"),
			}}
		},
	},
	{
		// Depends on: device. credentialValue is NOT selected: it is write-only
		// and read back as null by design, so comparing it would assert the
		// platform's secret-hiding rather than the upgrade's data fidelity.
		Name:     "device-credential",
		Area:     "device-management",
		Mutation: "createDeviceCredential",
		Input:    "DeviceCredentialCreateRequest!",
		Read:     "deviceCredentialsByToken",
		Fields:   "token credentialType credentialId enabled expiresAt metadata device{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":           s.tok("device-credential"),
				"deviceToken":     s.tokens["device"],
				"credentialType":  "MQTT_BASIC",
				"credentialId":    "apiprobe-device",
				"credentialValue": "apiprobe-secret",
				"enabled":         true,
				"metadata":        meta("device-credential"),
			}}
		},
	},
	{
		// Depends on: device-type. provisionSecret is write-only and absent from
		// the output type, so there is nothing to exclude by hand.
		Name:     "provisioning-profile",
		Area:     "device-management",
		Mutation: "createProvisioningProfile",
		Input:    "ProvisioningProfileCreateRequest!",
		Read:     "provisioningProfilesByToken",
		Fields:   "token name description provisionKey strategy credentialType enabled expiresAt metadata deviceType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("provisioning-profile"),
				"name":        "apiprobe provisioning",
				"description": "Self-registration profile; never exercised by the probe.",
				// 🔴 THE ONE VALUE IN THIS TABLE THAT IS NOT TENANT-SCOPED.
				// provision_key carries a bare `unique` CONSTRAINT rather than a
				// per-tenant index — a device presents it before it has a tenant,
				// and the schema's own snapshot records the global scope as a known
				// inconsistency. So it is derived from the tenant here: two probe
				// tenants in one instance would otherwise collide on a duplicate-key
				// error that names an index rather than the tenancy behind it.
				"provisionKey":    "apiprobe-key-" + s.tenant,
				"provisionSecret": "apiprobe-provision-secret",
				"strategy":        "ALLOW_NEW",
				"deviceTypeToken": s.tokens["deviceType"],
				// 🔑 The MINTABLE credential types are a strict subset of the
				// valid ones: provisioning can only mint ACCESS_TOKEN, because
				// its id IS the bearer token and there is nothing else to hand
				// back. MQTT_BASIC is a perfectly good credential type — it is
				// what the device-credential entity above declares — and it is
				// still refused here.
				"credentialType": "ACCESS_TOKEN",
				"enabled":        true,
				"metadata":       meta("provisioning-profile"),
			}}
		},
	},

	// ---- device-management: the entity families --------------------------
	{
		// A STATIC group: activeVersion and versions belong to a dynamic group's
		// publish history, and a static one is never versioned. `versions` is a
		// relation and is not selected; activeVersion is, so it must stay null.
		Name:     "entity-group",
		Area:     "device-management",
		Mutation: "createEntityGroup",
		Input:    "EntityGroupCreateRequest!",
		Read:     "entityGroupsByToken",
		Fields: "token name description imageUrl icon backgroundColor foregroundColor borderColor " +
			"metadata memberType membershipMode selector activeVersion",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":           s.tok("entity-group"),
				"memberType":      "device",
				"membershipMode":  "static",
				"name":            "apiprobe group",
				"description":     "A static device group; membership is explicit, so nothing evaluates.",
				"imageUrl":        "https://example.invalid/group.png",
				"icon":            "layers",
				"backgroundColor": "#101828",
				"foregroundColor": "#ffffff",
				"borderColor":     "#475467",
				"metadata":        meta("entity-group"),
			}}
		},
	},
	{
		Name:     "asset-type",
		Area:     "device-management",
		Mutation: "createAssetType",
		Input:    "AssetTypeCreateRequest!",
		Read:     "assetTypesByToken",
		Fields:   "token name description imageUrl icon backgroundColor foregroundColor borderColor metadata",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": brandedType(s.tok("asset-type"), "apiprobe asset type",
				"Parent of the probe asset.", meta("asset-type"))}
		},
		Record: record("assetType"),
	},
	{
		// Depends on: asset-type.
		Name:     "asset",
		Area:     "device-management",
		Mutation: "createAsset",
		Input:    "AssetCreateRequest!",
		Read:     "assetsByToken",
		Fields:   "token name description metadata assetType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":          s.tok("asset"),
				"name":           "apiprobe asset",
				"description":    "Child of the probe asset type.",
				"assetTypeToken": s.tokens["assetType"],
				"metadata":       meta("asset"),
			}}
		},
	},
	{
		Name:     "customer-type",
		Area:     "device-management",
		Mutation: "createCustomerType",
		Input:    "CustomerTypeCreateRequest!",
		Read:     "customerTypesByToken",
		Fields:   "token name description imageUrl icon backgroundColor foregroundColor borderColor metadata",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": brandedType(s.tok("customer-type"), "apiprobe customer type",
				"Parent of the probe customer.", meta("customer-type"))}
		},
		Record: record("customerType"),
	},
	{
		// Depends on: customer-type.
		Name:     "customer",
		Area:     "device-management",
		Mutation: "createCustomer",
		Input:    "CustomerCreateRequest!",
		Read:     "customersByToken",
		Fields:   "token name description metadata customerType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":             s.tok("customer"),
				"name":              "apiprobe customer",
				"description":       "Child of the probe customer type.",
				"customerTypeToken": s.tokens["customerType"],
				"metadata":          meta("customer"),
			}}
		},
	},
	{
		Name:     "area-type",
		Area:     "device-management",
		Mutation: "createAreaType",
		Input:    "AreaTypeCreateRequest!",
		Read:     "areaTypesByToken",
		Fields:   "token name description imageUrl icon backgroundColor foregroundColor borderColor metadata",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": brandedType(s.tok("area-type"), "apiprobe area type",
				"Parent of the probe area.", meta("area-type"))}
		},
		Record: record("areaType"),
	},
	{
		// Depends on: area-type.
		Name:     "area",
		Area:     "device-management",
		Mutation: "createArea",
		Input:    "AreaCreateRequest!",
		Read:     "areasByToken",
		Fields:   "token name description metadata areaType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":         s.tok("area"),
				"name":          "apiprobe area",
				"description":   "Child of the probe area type.",
				"areaTypeToken": s.tokens["areaType"],
				"metadata":      meta("area"),
			}}
		},
		Record: record("area"),
	},

	// ---- device-management: the typed relationship graph -----------------
	{
		Name:     "relationship-type",
		Area:     "device-management",
		Mutation: "createEntityRelationshipType",
		Input:    "EntityRelationshipTypeCreateRequest!",
		Read:     "entityRelationshipTypesByToken",
		Fields:   "token name description metadata tracked",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("relationship-type"),
				"name":        "apiprobe located-in",
				"description": "The edge kind the probe's two relationships use.",
				"tracked":     false,
				"metadata":    meta("relationship-type"),
			}}
		},
		Record: record("relationshipType"),
	},
	{
		// Depends on: relationship-type, device, area. Selecting source, target
		// and relationshipType by token is the point of this entry: an upgrade
		// that re-keyed the graph would leave the ROW intact and the EDGE wrong,
		// and only the endpoints show that.
		Name:     "entity-relationship",
		Area:     "device-management",
		Mutation: "createEntityRelationship",
		Input:    "EntityRelationshipCreateRequest!",
		Read:     "entityRelationshipsByToken",
		Fields:   "token sourceType targetType metadata source{token} target{token} relationshipType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": probeEdge(s, "entity-relationship")}
		},
	},
	{
		// Depends on: the same three. The bulk form takes `requests`, a LIST, and
		// is the only entry whose argument is not named `request`.
		Name:     "entity-relationships-bulk",
		Area:     "device-management",
		Mutation: "createEntityRelationships",
		Input:    "[EntityRelationshipCreateRequest!]!",
		Arg:      "requests",
		Read:     "entityRelationshipsByToken",
		Bulk:     true,
		Fields:   "token sourceType targetType metadata source{token} target{token} relationshipType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": []any{probeEdge(s, "entity-relationships-bulk")}}
		},
	},

	// ---- command-delivery -------------------------------------------------
	{
		// Depends on: device (and, for a published profile, command-definition —
		// though an unpublished profile leaves the device's vocabulary OPEN, so
		// the enqueue is accepted either way).
		//
		// 🔑 status, queuedTime, sentTime and respondedTime are deliberately NOT
		// selected. A command is an OPERATIONAL record whose state advances by
		// design — it may be held for an absent device, then delivered — so
		// comparing it verbatim across an upgrade would report ordinary delivery
		// as data corruption. What must not change is what the caller ASKED FOR.
		Name:     "command",
		Area:     "command-delivery",
		Mutation: "createCommand",
		Input:    "CommandCreateRequest!",
		Read:     "commandsByToken",
		Wrap:     "command",
		Reject:   "rejection{code reason}",
		Fields:   "token deviceToken name payload metadata",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("command"),
				"deviceToken": s.tokens["device"],
				"name":        probeCommandKey,
				"payload":     `{"delaySeconds":5}`,
				"metadata":    meta("command"),
			}}
		},
	},
	{
		// Depends on: device. resolved and accepted are STORED facts about the
		// moment the batch fired rather than live counts, which is what makes
		// them safe to compare; cancelledAt and cancelledCount stay null because
		// nothing cancels a drill batch.
		Name:     "command-batch",
		Area:     "command-delivery",
		Mutation: "createCommandBatch",
		Input:    "CommandBatchCreateRequest!",
		Read:     "commandBatchesByToken",
		Wrap:     "batch",
		Reject:   "rejection{code reason}",
		Fields: "token name payload targetKind groupToken groupVersion allowPartial " +
			"resolved accepted cancelledAt cancelledCount metadata",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":        s.tok("command-batch"),
				"name":         probeCommandKey,
				"payload":      `{"delaySeconds":10}`,
				"deviceTokens": []any{s.tokens["device"]},
				"allowPartial": true,
				"metadata":     meta("command-batch"),
			}}
		},
	},

	// ---- dashboard-management ---------------------------------------------
	{
		// 🔴 Read back through dashboard(token:) — a NULLABLE SINGLE object, not
		// the *sByToken list every device-management entity uses.
		Name:     "dashboard",
		Area:     "dashboard-management",
		Mutation: "createDashboard",
		Input:    "DashboardCreateRequest!",
		Read:     "dashboard",
		Single:   true,
		Fields:   "token name description definition",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("dashboard"),
				"name":        "apiprobe dashboard",
				"description": "The definition is stored opaquely, so this asserts the blob, not its meaning.",
				"definition":  `{"version":1,"layout":{"columns":12},"widgets":[]}`,
			}}
		},
	},

	// ---- outbound-connectors ----------------------------------------------
	{
		// 🔴 Also a nullable single read. The config is validated per type at
		// write for a shipped generator, so an mqtt config must carry a non-empty
		// urls list and a topic — an invalid one is refused, not stored.
		Name:     "connector",
		Area:     "outbound-connectors",
		Mutation: "createConnector",
		Input:    "ConnectorCreateRequest!",
		Read:     "connector",
		Single:   true,
		// hasSecret rather than the secret: the credential is sealed in the
		// secret store and never returned, and whether one EXISTS is the part an
		// upgrade could lose.
		Fields: "token name description type config hasSecret",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("connector"),
				"name":        "apiprobe connector",
				"description": "Never dispatched; the probe asserts the record, not a delivery.",
				"type":        "mqtt",
				"config":      `{"urls":["tcp://localhost:1883"],"topic":"apiprobe"}`,
				"secret":      "apiprobe-connector-secret",
			}}
		},
	},

	// ---- notification-management -------------------------------------------
	{
		Name:     "notification-channel",
		Area:     "notification-management",
		Mutation: "createNotificationChannel",
		Input:    "NotificationChannelCreateRequest!",
		Read:     "notificationChannelsByToken",
		Fields:   "token name description channelType config hasSecret enabled metadata",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("notification-channel"),
				"name":        "apiprobe webhook",
				"description": "Points at an unroutable host; the probe never delivers through it.",
				"channelType": "webhook",
				"config":      `{"url":"https://example.invalid/hook","method":"POST"}`,
				"secret":      "apiprobe-channel-secret",
				"enabled":     true,
				"metadata":    meta("notification-channel"),
			}}
		},
		Record: record("channel"),
	},
	{
		// Depends on: notification-channel.
		//
		// ⚠️ deviceTypeToken is left UNSET on purpose. A non-empty value is
		// refused at write today, because the dispatcher skips a device-type
		// scoped policy rather than applying it tenant-wide — so setting it would
		// turn this row into a REFUSED seed, not extra coverage.
		//
		// The severity is uppercase because a rule matches the ALARM's tier by
		// exact string. A lowercase one writes, reads back byte-for-byte, and
		// never delivers — precisely the kind of defect a read-back comparison
		// cannot see, so it is worth not planting one here.
		Name:     "notification-policy",
		Area:     "notification-management",
		Mutation: "createNotificationPolicy",
		Input:    "NotificationPolicyCreateRequest!",
		Read:     "notificationPoliciesByToken",
		Fields: "token name description deviceTypeToken throttleSeconds escalateAfterSeconds " +
			"maxEscalations enabled metadata rules{severity recipients channel{token}}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":                s.tok("notification-policy"),
				"name":                 "apiprobe policy",
				"description":          "One rule, one channel; asserts the rule set survives with it.",
				"throttleSeconds":      300,
				"escalateAfterSeconds": 900,
				"maxEscalations":       2,
				"enabled":              true,
				"rules": []any{map[string]any{
					"severity":     "CRITICAL",
					"channelToken": s.tokens["channel"],
					"recipients":   `["ops@example.invalid"]`,
				}},
				"metadata": meta("notification-policy"),
			}}
		},
	},
}

// brandedType builds the create input the three branded parent types share
// (asset/customer/area type). They take an identical field set, and writing it
// out three times invites the drift where one of them quietly stops exercising a
// colour column.
func brandedType(token, name, description, metadata string) map[string]any {
	return map[string]any{
		"token":           token,
		"name":            name,
		"description":     description,
		"imageUrl":        "https://example.invalid/" + token + ".png",
		"icon":            "box",
		"backgroundColor": "#101828",
		"foregroundColor": "#ffffff",
		"borderColor":     "#475467",
		"metadata":        metadata,
	}
}

// probeEdge builds a device→area relationship of the probe's own type. Both
// relationship entries create the SAME edge under different tokens, which the
// graph permits and which keeps the singular and bulk paths comparable.
func probeEdge(s *state, name string) map[string]any {
	return map[string]any{
		"token":            s.tok(name),
		"sourceType":       "device",
		"source":           s.tokens["device"],
		"targetType":       "area",
		"target":           s.tokens["area"],
		"relationshipType": s.tokens["relationshipType"],
		"metadata":         meta(name),
	}
}

// tenantCreateMutations is the platform's total, measured from the served
// schemas. It is written down so printCoverage can state the DENOMINATOR: "26
// entities covered" invites the reader to assume that is all there is, and
// "26 of 26" does not.
//
// ⚠️ Kept honest by TestCoverageDenominatorMatchesTheSchemas, which counts the
// create mutations in the served schemas rather than trusting this number. When
// a new create mutation lands, that test fails HERE — which is the notice that
// the table needs a row, not just that the constant needs a bump.
const tenantCreateMutations = 26

// tenantPublishMutations is the same claim for publishes, measured the same way by
// TestThePublishDenominatorMatchesTheSchemas. Separate from the count above because
// they are separate claims: a table can cover every create and no publish, which is
// exactly the state this constant was added to end.
const tenantPublishMutations = 4

// printCoverage prints the tool's coverage CLAIM. Given a baseline it also
// prints what a seed against that release would SKIP — so the drill's real
// coverage can be read before an hour of cluster time is spent discovering it.
func printCoverage(base *baseline) {
	byArea := map[string][]string{}
	for _, e := range allEntities() {
		byArea[e.Area] = append(byArea[e.Area], e.Name)
	}
	areas := make([]string, 0, len(byArea))
	for a := range byArea {
		areas = append(areas, a)
	}
	sort.Strings(areas)

	fmt.Printf("apiprobe covers %d of the platform's %d tenant-plane create mutations,\n"+
		"and %d of its %d publish mutations.\n\n",
		len(entities), tenantCreateMutations, len(publishes), tenantPublishMutations)
	for _, a := range areas {
		fmt.Printf("  %s\n", a)
		for _, n := range byArea[a] {
			fmt.Printf("      %s\n", n)
		}
	}
	if len(entities) < tenantCreateMutations {
		fmt.Printf("\n⚠️  %d create mutations are NOT covered. A green verify says the entities\n"+
			"    listed above survived — it says nothing about the rest of the API.\n",
			tenantCreateMutations-len(entities))
	} else {
		// Covering every create mutation is still not covering every FIELD: the
		// comparison only sees what each row SELECTS. Say so, rather than letting
		// "26 of 26" read as "nothing can slip through".
		fmt.Print("\nEvery tenant-plane create mutation has a row. The comparison still covers\n" +
			"only the fields each row SELECTS — derived and operational fields are left\n" +
			"out deliberately, and the table's comments say which and why.\n")
	}

	if base != nil {
		var skipped []string
		for _, e := range entities {
			if ok, why := base.supports(e); !ok {
				skipped = append(skipped, fmt.Sprintf("  %-26s %s", e.Name, why))
			}
		}
		fmt.Printf("\nAgainst the baseline at %s:\n", base.dir)
		if len(skipped) == 0 {
			fmt.Print("  nothing is skipped — that release can express the whole table.\n")
		} else {
			fmt.Printf("  %d of %d entities cannot be seeded there, so a drill from that release\n"+
				"  says nothing about them:\n", len(skipped), len(entities))
			for _, s := range skipped {
				fmt.Println(s)
			}
		}
	}

	// The controls belong in the coverage claim, because they are what makes a
	// green verify mean anything at all. `apiprobe tamper` breaks these, in this
	// order, and each names the exit code it proves verify can still produce.
	fmt.Print("\nNegative controls (apiprobe tamper --mode …), in the order a rig runs them:\n")
	for _, t := range tampers {
		what, expect := t.Mutation+"("+t.Arg+":)", "MISSING"
		if t.Mode == tamperModify {
			what, expect = t.Mutation+" sets "+t.Field, "MISMATCH"
		}
		fmt.Printf("  %-8s %-12s %-32s ⇒ verify must exit %s\n", t.Mode, t.Entity, what, expect)
	}
}

// printAreas lists the functional areas the table writes to, one per line, so a
// rig can DEPLOY them rather than assume them.
//
// 🔑 This is not cosmetic. `outbound-connectors` is held out of the `default`
// profile, so a stock install serves no route for it and the connector row is
// refused — an entity missing from the drill because of a deployment decision,
// reported as if the API had rejected it. A rig that reads this list enables
// every area the table needs, and keeps doing so when the table gains another.
func printAreas() {
	for _, a := range probeAreas() {
		fmt.Println(a)
	}
}

// probeAreas is the deduplicated, sorted set of areas the table writes to.
func probeAreas() []string {
	seen := map[string]bool{}
	areas := make([]string, 0, len(entities))
	// allEntities, not entities: a publish in an area no create touches would
	// otherwise leave the rig deploying an area short, and the seed would fail
	// against an endpoint that was never brought up.
	for _, e := range allEntities() {
		if !seen[e.Area] {
			seen[e.Area] = true
			areas = append(areas, e.Area)
		}
	}
	sort.Strings(areas)
	return areas
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
