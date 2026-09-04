// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

// PUBLISHING, WHICH THE COVERAGE TABLE STRUCTURALLY CANNOT HOLD.
//
// `entities` is one row per tenant CREATE mutation, and its own tests enforce that:
// TestTheTableCoversEveryCreateMutation pins len(entities) to the create-mutation count
// read out of the schemas, and TestEveryEntityIsComplete requires an input type and a
// `token` in the selection. A publish has none of those — it takes scalar arguments,
// and it returns a version object addressed by (parent token, version number) with no
// token of its own. Adding four rows to that table would have broken both invariants,
// which is the tests saying this is a different kind of thing rather than an
// inconvenience to work around.
//
// 🔴 AND IT IS NOT A COSMETIC GAP. Nothing in the drill published anything, so:
//
//   - `device_profile_versions`, `entity_group_versions`, `dashboard_versions` and
//     `connector_versions` were all EMPTY. Those rows are the frozen snapshots the
//     stored-shape guard exists to protect — the drill was guarding a document it
//     never wrote.
//   - `event-processing.detect_rules` was empty too, and that one is not a gap in a
//     table, it is a gap in what the drill was testing. detect_rules is projected from
//     a DetectionRulesPublishedEvent, and factRuleRows DROPS any rule whose
//     ProfileVersionToken is empty. With nothing published, the seeded detection rule
//     never reached the DETECT engine at all: the row existed, the rule was INERT, and
//     every check the drill ran was satisfied by the row.
//
// That is the same shape as the geofence-archive defect — something DERIVED from a
// write, invisible to a round trip of that write — reached this time through a table
// that held no rows rather than a snapshot that held stale ones.
//
// WHY A PARALLEL TABLE AND NOT A SECOND CODE PATH. Each op renders itself AS an
// entity, so seed, verify, tamper and the receipt need no second anything: the receipt
// format is unchanged, the read-back comparison is the same field-by-field compare, and
// the only new behaviour is three lines in createDoc/readDoc/readObject. A publish that
// needed its own verifier would be a second, weaker verifier.

// publishOp is one publish mutation and the query that reads its version back.
//
// Uniform by construction — all four are publishX(token:,label:,description:) against a
// parent token, read back through xVersions(token:) newest-first. There is nothing to
// vary, so nothing is made variable: the documents are rendered from this shape rather
// than spelled out per op, which is what stops one of the four drifting.
type publishOp struct {
	// Name is the receipt key. Same rule as entity.Name: never reuse or rename one.
	Name string
	// Area is the functional area serving the mutation.
	Area string
	// Mutation is the publish field, Read the versions query.
	Mutation string
	Read     string
	// Requires is passed straight through to the rendered entity: a publish whose
	// TARGET row is field-gated has to be gated on the same field, or it publishes a
	// token nothing created. See entity.Requires.
	Requires []string
	// Of is the NAME of the entity being published. Its token is derived with the same
	// state.tok helper the parent used, rather than read out of the recorded state:
	// only some entries Record themselves, and a publish that depended on that would
	// work for the profile and silently publish an empty token for the dashboard.
	// TestEveryPublishNamesAnEntityInTheTable pins the name to a real row.
	Of string
	// Fields is the selection, used verbatim in both documents. Same rule as
	// entity.Fields: select only STORED values.
	Fields string
}

// entity renders the op in the table's own vocabulary.
//
// The receipt token is the PARENT's, supplied here rather than read off the response,
// because a version carries no token and seed would otherwise refuse it. That is also
// exactly what the read-back needs: xVersions is keyed by the parent.
func (p publishOp) entity() entity {
	return entity{
		Name:     p.Name,
		Area:     p.Area,
		Mutation: p.Mutation,
		Read:     p.Read,
		Fields:   p.Fields,
		Requires: p.Requires,
		Publish:  true,
		Vars: func(s *state) map[string]any {
			return map[string]any{
				"token": s.tok(p.Of),
				"label": "apiprobe-" + p.Name,
			}
		},
	}
}

// publishVersionFields are the fields every version type has and that are safe to
// compare across an upgrade.
//
// 🔴 publishedAt IS DELIBERATELY NOT AMONG THEM, and the reason is worth writing down
// because it is not "the field is derived". It is stored, and it does not change. What
// differs is PRECISION: the publish response carries the in-memory time.Time, and
// PostgreSQL stores timestamptz to microseconds, so the value handed back by the
// mutation was never persisted at the precision it was printed with. Measured, on the
// drill that caught it:
//
//	written: 2026-08-27T17:06:22.615501294Z   (nanoseconds, from the create response)
//	read:    2026-08-27T17:06:22.615501Z      (microseconds, from the row)
//
// Comparing those reports a healthy instance as MISMATCH — the failure code this drill
// exists to raise — on every single run. Same test as activeVersion on the profile:
// not "is it derived?" but "does anything OTHER than data loss change it between seed
// and verify?", and here the answer is persistence itself.
//
// (It is also a small, real observation about the API: a client that caches a publish
// response holds a publishedAt that will never equal a subsequent read of the same row.)
//
// publishedBy stays. It is a string, it round-trips exactly, and a migration that
// rebuilt a version table would be most likely to lose it.
const publishVersionFields = "version label description publishedBy"

// publishes is the publish coverage claim, the same way entities is the create one.
// TestThePublishTableCoversEveryPublishMutation counts it against the schemas.
var publishes = []publishOp{
	{
		// 🔴 THE ONE THAT IS NOT ABOUT ITS OWN TABLE. Publishing the profile is what
		// emits the DetectionRulesPublishedEvent that projects the seeded detection
		// rule into event-processing. Without it the rule is a row nobody runs.
		Name:     "device-profile-version",
		Area:     "device-management",
		Mutation: "publishDeviceProfile",
		Read:     "deviceProfileVersions",
		Of:       "device-profile",
		Fields:   publishVersionFields,
	},
	{
		// selector and memberType are FROZEN at publish — the whole point of the version
		// — so they are compared, not just the envelope around them. This publishes the
		// DYNAMIC group; the static one beside it is never versioned, which is why the
		// table needs both and why covering only one of them covered neither path.
		Name:     "entity-group-version",
		Area:     "device-management",
		Mutation: "publishEntityGroup",
		Read:     "entityGroupVersions",
		Of:       "entity-group-dynamic",
		Fields:   publishVersionFields + " selector memberType",
	},
	{
		Name:     "dashboard-version",
		Area:     "dashboard-management",
		Mutation: "publishDashboard",
		Read:     "dashboardVersions",
		Of:       "dashboard",
		Fields:   publishVersionFields,
	},
	{
		// propertySchema is the contract FROZEN at publish — the whole point of the
		// version, and the thing an asset is validated against — so it is compared, not
		// just the envelope around it. It is also the one version type that exposes its
		// frozen document at all: a device profile's snapshot is deliberately hidden, so
		// there is nothing there for this table to compare.
		Name:     "asset-type-version",
		Area:     "device-management",
		Mutation: "publishAssetType",
		Read:     "assetTypeVersions",
		// The SCHEMA-BEARING type, not the plain one: publishAssetType refuses a type
		// with no draft contract, so publishing `asset-type` would fail on every run.
		Of:       "asset-type-schema",
		Requires: []string{"AssetTypeCreateRequest.propertySchema"},
		Fields:   publishVersionFields + " propertySchema",
	},
	{
		// `type` is the connector type frozen at publish, which is a different claim
		// from the live connector's type and is what a rollback would restore.
		Name:     "connector-version",
		Area:     "outbound-connectors",
		Mutation: "publishConnector",
		Read:     "connectorVersions",
		Of:       "connector",
		Fields:   publishVersionFields + " type",
	},
}

// publishesNotCovered names publish mutations this tool deliberately does NOT exercise,
// with the reason — so that a short table is a number with an explanation attached
// rather than a gap somebody has to notice.
//
// 🔴 THE SUM IS WHAT IS CHECKED, not either half: len(publishes) + len(this) must equal
// the count read out of the schemas. A publish added to the platform then breaks the
// arithmetic instead of quietly enlarging the denominator, and an entry here that stops
// being true is caught by the two counterweights in publishes_test.go. Same shape as the
// govulncheck allowlist, and for the same reason: an exception nobody is watching is a
// gap with paperwork.
//
// It is EMPTY, and it was not. publishEntityGroup lived here for exactly as long as the
// table seeded only a STATIC group — the platform refuses to publish one, in its own
// words: `only a dynamic entity group can be published`. The table now seeds both, so
// the exclusion was deleted rather than left to look maintained.
var publishesNotCovered = map[string]string{}

// afterPublish is the third phase: entities that cannot be created until something has
// been published, because they reference a published VERSION NUMBER.
var afterPublish = []entity{
	{
		// Depends on: asset-type-schema, asset-type-version.
		//
		// An asset under the SCHEMA-BEARING type, carrying properties — which is only
		// possible after that type is published, since an asset of a type with no
		// published contract may carry none. That is the whole reason this entry is in
		// this phase rather than beside `asset` in the main table.
		//
		// The `asset` row next door stays property-less on purpose: the two together are
		// the pair of states an asset can be in, and covering only the filled one would
		// leave the empty one — the state every asset that predates a publish is in —
		// unexercised.
		//
		// 🔴 IT REQUIRES BOTH FIELDS, not just its own. `properties` is what this row
		// sends; `propertySchema` is what the type it hangs off needs in order to exist
		// at all. Gating on one of them would let this row outlive its dependency the
		// day a release ships one without the other — and the failure would be a
		// REFUSED create naming THIS row, several steps after the decision that caused
		// it. The two shipped together; the gate says so rather than assuming it.
		Name:     "asset-with-properties",
		Area:     "device-management",
		Mutation: "createAsset",
		Input:    "AssetCreateRequest!",
		Read:     "assetsByToken",
		Requires: []string{"AssetCreateRequest.properties", "AssetTypeCreateRequest.propertySchema"},
		Fields:   "token name description metadata properties assetType{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":       s.tok("asset-with-properties"),
				"name":        "apiprobe asset with properties",
				"description": "Fills the published property contract of the probe asset type.",
				// state.tok, not the recorded state: asset-type-schema is a skippable leaf
				// and deliberately records nothing, so its token is derived the same way the
				// publish op derives it.
				"assetTypeToken": s.tok("asset-type-schema"),
				"metadata":       meta("asset-with-properties"),
				"properties":     `{"vendor":"apiprobe","psi":42}`,
			}}
		},
	},
	{
		// Depends on: device-profile, entity-group-dynamic, entity-group-version.
		//
		// A rule's scope is (groupToken, groupVersion), and that version does not exist
		// until the group is published — which is the whole reason this phase exists.
		//
		// 🔴 IT DOES NOT WRITE detection_rule_scope_refs, and an earlier version of this
		// comment claimed it did. Those rows come from scopedRulesInSnapshot(), which
		// SKIPS disabled rules, at profile publish — and this rule is authored DISABLED
		// for the same reason the unscoped one is: the probe asserts that the rule
		// document and its SCOPE survive an upgrade, not that the engine runs. What this
		// entry covers is the round trip of entityGroupToken/entityGroupVersion, which is
		// a real claim and the only one it makes. The scope-ref table is exempt, with the
		// whole chain written out in tablesweep.go.
		//
		// Version 1 is not a guess — it is the first publish of a group this tool created.
		Name:     "detection-rule-scoped",
		Area:     "device-management",
		Mutation: "createDetectionRule",
		Input:    "DetectionRuleCreateRequest!",
		Read:     "detectionRulesByToken",
		Fields: "token name description definition enabled metadata " +
			"entityGroupToken entityGroupVersion deviceProfile{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":              s.tok("detection-rule-scoped"),
				"deviceProfileToken": s.tok("device-profile"),
				"name":               "Overheating, in one group",
				"description":        "Scoped to a published group version; asserts the scope, not the engine.",
				"definition":         `{"type":"threshold","metric":"` + probeMetricKey + `","op":">","value":40}`,
				"enabled":            false,
				"entityGroupToken":   s.tok("entity-group-dynamic"),
				"entityGroupVersion": 1,
				"metadata":           meta("detection-rule-scoped"),
			}}
		},
	},
}

// allEntities is every seedable thing: creates first, then publishes.
//
// THREE PHASES, and the third exists because two were not enough. A detection rule
// SCOPED to an entity group carries that group's published VERSION NUMBER, which does
// not exist until the group is published — so it can be neither a create (phase one runs
// before every publish) nor a publish. Rather than reorder the whole table around one
// entry, the sequence gained an explicit tail, named for what it is.
//
// 🔴 ORDER IS LOAD-BEARING, and more so here than in the table alone. A publish freezes
// whatever its parent holds AT THAT MOMENT, so it has to run after every entity that
// contributes to the parent — a profile's metric, command and detection-rule
// definitions are all created after the profile itself, and a profile published before
// them would freeze an empty snapshot and still look like a successful publish.
func allEntities() []entity {
	out := make([]entity, 0, len(entities)+len(publishes)+len(afterPublish))
	out = append(out, entities...)
	for _, p := range publishes {
		out = append(out, p.entity())
	}
	return append(out, afterPublish...)
}
