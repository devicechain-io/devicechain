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
		Publish:  true,
		Vars: func(s *state) map[string]any {
			return map[string]any{
				"token": s.tok(p.Of),
				"label": "apiprobe-" + p.Name,
			}
		},
	}
}

// publishVersionFields are the five fields every version type has, all stored.
//
// `publishedAt` and `publishedBy` are stored columns, not derived: the row's creation
// time and the identity that wrote it. They are worth comparing precisely because a
// migration that rebuilt a version table would be most likely to lose them.
const publishVersionFields = "version label description publishedAt publishedBy"

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
		// selector and memberType are FROZEN at publish — the whole point of the
		// version — so they are compared, not just the envelope around them.
		Name:     "entity-group-version",
		Area:     "device-management",
		Mutation: "publishEntityGroup",
		Read:     "entityGroupVersions",
		Of:       "entity-group",
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

// allEntities is every seedable thing: creates first, then publishes.
//
// 🔴 ORDER IS LOAD-BEARING, and more so here than in the table alone. A publish freezes
// whatever its parent holds AT THAT MOMENT, so it has to run after every entity that
// contributes to the parent — a profile's metric, command and detection-rule
// definitions are all created after the profile itself, and a profile published before
// them would freeze an empty snapshot and still look like a successful publish.
func allEntities() []entity {
	out := make([]entity, 0, len(entities)+len(publishes))
	out = append(out, entities...)
	for _, p := range publishes {
		out = append(out, p.entity())
	}
	return out
}
