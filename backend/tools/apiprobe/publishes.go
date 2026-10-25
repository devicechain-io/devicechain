// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
)

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
	// Wrap and WrapMarker are the entity's: set when the publish returns an envelope
	// whose `Wrap` member is the version. See entity.Wrap and entity.WrapMarker.
	Wrap       string
	WrapMarker string
}

// entity renders the op in the table's own vocabulary.
//
// The receipt token is the PARENT's, supplied here rather than read off the response,
// because a version carries no token and seed would otherwise refuse it. That is also
// exactly what the read-back needs: xVersions is keyed by the parent.
func (p publishOp) entity() entity {
	return entity{
		Name:       p.Name,
		Area:       p.Area,
		Mutation:   p.Mutation,
		Read:       p.Read,
		Fields:     p.Fields,
		Requires:   p.Requires,
		Publish:    true,
		Wrap:       p.Wrap,
		WrapMarker: p.WrapMarker,
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
		// publishDashboard returns {version, dashboard}; earlier releases returned the
		// version itself, which the baseline adaptation falls back to.
		Wrap:       "version",
		WrapMarker: "dashboard",
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
	{
		// Depends on: device, device-profile, device-profile-configuration,
		// device-profile-version.
		//
		// 🔑 THIS WRITE IS WHAT MINTS device_configuration_revisions. A shared attribute
		// write mints a revision only when the key is declared by the device's profile's
		// ACTIVE PUBLISHED version, so it can run neither before the declaration is set nor
		// before the profile is published — the same ordering reason as the rows above.
		// Verify reads the attribute back; the table sweep then proves the revision row it
		// minted survived the upgrade.
		//
		// device_configuration_states is NOT reached from here: it is written only by the
		// consumer of a device's own sync and report messages, and no tenant API writes it.
		// See tablesweep.go.
		Name:      "entity-attribute-configuration",
		Area:      "device-management",
		Mutation:  "setEntityAttribute",
		Input:     "EntityAttributeSetRequest!",
		Read:      "entityAttributes",
		ReadInput: "EntityAttributeSearchCriteria!",
		ReadVars: map[string]any{
			"pageNumber": 1, "pageSize": 10,
			"entityType": "device", "entity": "apiprobe-device",
			"scope": probeAttrScope, "attrKeys": []any{probeConfigKey},
		},
		Fields: "entityType scope attrKey valueType value",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"entityType": "device",
				"entity":     s.tokens["device"],
				"scope":      probeAttrScope,
				"attrKey":    probeConfigKey,
				"valueType":  probeValueType,
				"value":      probeConfigValue,
			}}
		},
		TokenFrom: func(s *state) string {
			return "device/" + s.tokens["device"] + "/" + probeAttrScope + "/" + probeConfigKey
		},
	},
	{
		// Depends on: device-profile.
		//
		// An artifact whose bytes WERE uploaded, so it is AVAILABLE: the only state an
		// artifact can be assigned from, and the one that holds a blob reference. The
		// `artifact` row in the main table stays UPLOADING on purpose (it proves the
		// declaration survives); this one proves the completed state does, and gives the
		// assignment below something it may legitimately name.
		//
		// The content is a fixed literal so its digest is a constant the declaration and
		// the upload agree on. The upload is the one step GraphQL cannot express, which is
		// what After is for.
		Name:     "artifact-available",
		Area:     "update-management",
		Mutation: "createArtifact",
		Input:    "ArtifactCreateRequest!",
		Read:     "artifact",
		Single:   true,
		Fields:   "token kind name version component sizeBytes digest mediaType compatibleProfiles trustStatus state",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":              s.tok("artifact-available"),
				"kind":               "FIRMWARE",
				"name":               "apiprobe artifact (uploaded)",
				"version":            "2.0.0",
				"component":          "firmware",
				"sizeBytes":          fmt.Sprint(len(probeArtifactContent)),
				"digest":             probeArtifactDigest(),
				"mediaType":          "application/octet-stream",
				"compatibleProfiles": []string{s.tokens["profile"]},
			}}
		},
		After: uploadProbeArtifact,
		// The upload is what makes it AVAILABLE, so the create response (UPLOADING) is
		// not what it holds afterwards; the seed records the settled read instead.
		Settled: func(f map[string]any) error {
			if s := str(f["state"]); s != "AVAILABLE" {
				return fmt.Errorf("state is %q, want AVAILABLE", s)
			}
			return nil
		},
	},
	{
		// Depends on: device, artifact-available.
		//
		// 🔑 THIS WRITE IS WHAT CREATES update_assignments AND update_attempts: assigning an
		// artifact stores the append-only assignment row and the attempt the update state
		// machine will drive. The token is minted by the platform, so the receipt records
		// the one the response returned and the read-back is keyed by it.
		//
		// The attempt is read back QUEUED and stays QUEUED: nothing here is a device, so
		// nothing reports, and the deadline that would move it is a day away. Only stored
		// values are selected; the timestamps that move are not.
		//
		// ⚠️ IT NEEDS THE INSTANCE TO ALLOW UNVERIFIED ARTIFACTS
		// (updates.allowUnverifiedArtifacts), because every artifact is UNVERIFIED today. An
		// instance that does not refuses this seed, by name, rather than skipping it: a
		// silent skip would be coverage that quietly stops existing. The upgrade rig sets the
		// flag on its own instance before each seed that writes this row
		// (allow_unverified_artifacts in hack/upgrade-rig.sh), and installs a baseline that
		// ships the area with the object store the artifact rows need.
		Name:     "update-assignment",
		Area:     "update-management",
		Mutation: "assignArtifact",
		Params: []param{
			{Name: "deviceToken", Type: "String!"},
			{Name: "artifactToken", Type: "String!"},
		},
		Read:   "updateAssignment",
		Single: true,
		Fields: "token deviceToken component current actor artifact{token state digest} " +
			"attempt{token state targetVersion targetDigest requiresReboot cancelRequested reconciled}",
		Vars: func(s *state) map[string]any {
			return map[string]any{
				"deviceToken":   s.tokens["device"],
				"artifactToken": s.tok("artifact-available"),
			}
		},
	},
	{
		// Depends on: device-profile.
		//
		// A SIGNER, registered through the real API: update-management's trust registry, and the
		// table artifact_signers the coverage sweep would otherwise find empty. The key is derived
		// from a fixed public seed so the receipt, the read-back and the signature below are all
		// deterministic literals; it is a test key with no authority anywhere. The signer's token
		// is minted by the platform from the key, so the receipt records the one the response
		// returned. Only stored fields are selected: soleSignedArtifactCount is derived.
		Name:     "artifact-signer",
		Area:     "update-management",
		Mutation: "registerArtifactSigner",
		Params: []param{
			{Name: "name", Type: "String!"},
			{Name: "publicKeyPem", Type: "String!"},
		},
		Read:   "artifactSigner",
		Single: true,
		Fields: "token name algorithm publicKeyPem keyId state createdBy",
		Vars: func(s *state) map[string]any {
			return map[string]any{"name": "apiprobe signer", "publicKeyPem": probeSignerPEM()}
		},
		Requires: probeSignerGroup,
	},
	{
		// Depends on: device-profile.
		//
		// The artifact the signature is attached to. Declared only (UPLOADING, no bytes): a
		// signature is over the declared digest, so nothing is gained by uploading, and keeping it
		// apart from artifact-available leaves that row's trust UNVERIFIED for its assignment.
		Name:     "artifact-signed",
		Area:     "update-management",
		Requires: probeSignerGroup,
		Mutation: "createArtifact",
		Input:    "ArtifactCreateRequest!",
		Read:     "artifact",
		Single:   true,
		Fields:   "token kind name version component sizeBytes digest mediaType compatibleProfiles state",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":              s.tok("artifact-signed"),
				"kind":               "FIRMWARE",
				"name":               "apiprobe artifact (signed)",
				"version":            "3.0.0",
				"component":          "firmware",
				"sizeBytes":          fmt.Sprint(len(probeArtifactContent)),
				"digest":             probeArtifactDigest(),
				"mediaType":          "application/octet-stream",
				"compatibleProfiles": []string{s.tokens["profile"]},
			}}
		},
	},
	{
		// Depends on: artifact-signer, artifact-signed.
		//
		// 🔑 THIS WRITE CREATES artifact_signatures, AND ITS READ-BACK IS THE DERIVED TRUST. The
		// platform verifies the signature against the artifact's stored digest, so a seed that
		// signed the wrong bytes is refused by name. trustStatus is computed at read time from the
		// signature and the signer's state; selecting it means verify fails if an upgrade loses the
		// signature, the signer, or the rule that joins them (the artifact reads UNVERIFIED).
		Name:     "artifact-signature",
		Area:     "update-management",
		Requires: probeSignerGroup,
		Mutation: "addArtifactSignature",
		Params: []param{
			{Name: "artifactToken", Type: "String!"},
			{Name: "signerToken", Type: "String!"},
			{Name: "signature", Type: "String!"},
		},
		Read:   "artifact",
		Single: true,
		Fields: "token digest state trustStatus",
		Vars: func(s *state) map[string]any {
			return map[string]any{
				"artifactToken": s.tok("artifact-signed"),
				"signerToken":   probeSignerToken(),
				"signature":     probeArtifactSignature(),
			}
		},
	},
	{
		// Depends on: dashboard, customer.
		//
		// 🔑 THIS WRITE CREATES dashboard_shares: sharing the probe dashboard with the probe
		// customer stores the anchor row a customer-scoped member's dashboard list is
		// filtered by. The read-back selects sharedWith, so verify fails if an upgrade loses
		// the share — which would not look like loss at all, only like a customer member
		// whose dashboards quietly disappeared.
		//
		// A release that predates sharing cannot express shareDashboard, so a baseline seed
		// skips this row by the mutation's name; nothing reads a token out of it, so the
		// skip strands no dependent. It is in this phase only because it writes onto a
		// dashboard that already exists, and sharing freezes nothing a publish would need.
		Name:     "dashboard-share",
		Area:     "dashboard-management",
		Mutation: "shareDashboard",
		Params: []param{
			{Name: "token", Type: "String!"},
			{Name: "anchors", Type: "[ScopeAnchorInput!]!"},
		},
		Read:   "dashboard",
		Single: true,
		Fields: "token sharedWith{kind token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{
				"token":   s.tok("dashboard"),
				"anchors": []any{map[string]any{"kind": "CUSTOMER", "token": s.tok("customer")}},
			}
		},
	},
	{
		// Depends on: artifact-available, entity-group-dynamic, entity-group-version,
		// update-assignment.
		//
		// 🔑 THIS ROW CREATES update_rollouts, AND ITS START CREATES update_rollout_targets. A group
		// rollout is declared DRAFT over GraphQL; starting it (the After step) freezes the dynamic
		// group's published membership — the seeded device — as its target set. The device already
		// holds the update-assignment row's assignment of the same artifact, so the start ADOPTS it
		// rather than assigning again, and the rollout's one target is in flight from then on.
		//
		// Only stored values are selected: targetCount is written once by the start; state and
		// progress move as the device's attempt does (its deadline is a day away) and are left out.
		// It needs updates.allowUnverifiedArtifacts for the same reason update-assignment does, and
		// a release without rollouts skips it by the field it was written for; nothing reads its token.
		Name:     "update-rollout",
		Area:     "update-management",
		Mutation: "createUpdateRollout",
		Input:    "UpdateRolloutCreateRequest!",
		Read:     "updateRollout",
		Single:   true,
		Requires: []string{"UpdateRollout.maxInFlight"},
		Fields:   "token name groupToken maxInFlight failureThreshold targetCount artifact{token}",
		Vars: func(s *state) map[string]any {
			return map[string]any{"req": map[string]any{
				"token":            s.tok("update-rollout"),
				"name":             "apiprobe rollout",
				"artifactToken":    s.tok("artifact-available"),
				"groupToken":       s.tok("entity-group-dynamic"),
				"maxInFlight":      2,
				"failureThreshold": 5,
			}}
		},
		After: startProbeRollout,
		Settled: func(f map[string]any) error {
			if n, _ := f["targetCount"].(float64); n < 1 {
				return fmt.Errorf("targetCount is %v, want the started rollout's frozen target set", f["targetCount"])
			}
			return nil
		},
	},
}

// startProbeRollout starts the probe rollout, which is what writes its target rows.
func startProbeRollout(ctx context.Context, c *connection, sess *userclient.TenantSession, st *state) error {
	var out struct {
		StartUpdateRollout struct {
			State       string `json:"state"`
			TargetCount int    `json:"targetCount"`
		} `json:"startUpdateRollout"`
	}
	if err := sess.Query(ctx, c.areaURL("update-management"), `mutation($t:String!){startUpdateRollout(token:$t){state targetCount}}`,
		map[string]any{"t": st.tok("update-rollout")}, &out); err != nil {
		return fmt.Errorf("start the probe rollout: %w", err)
	}
	if out.StartUpdateRollout.TargetCount < 1 {
		return fmt.Errorf("the probe rollout started with %d targets; the dynamic group should resolve to the seeded device",
			out.StartUpdateRollout.TargetCount)
	}
	return nil
}

// probeArtifactContent is the bytes the probe uploads for its AVAILABLE artifact. A fixed literal,
// so the digest declared at create and the digest of what is uploaded are the same constant.
const probeArtifactContent = "apiprobe artifact content: not a firmware image, only bytes with a known digest\n"

func probeArtifactDigest() string {
	sum := sha256.Sum256([]byte(probeArtifactContent))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// uploadProbeArtifact sends the probe artifact bytes to the content endpoint, authenticated as the
// probe tenant administrator like every other write. The platform verifies size and digest and
// flips the artifact to AVAILABLE; anything else is a refusal worth stopping for.
func uploadProbeArtifact(ctx context.Context, c *connection, sess *userclient.TenantSession, st *state) error {
	token, err := sess.AccessToken(ctx)
	if err != nil {
		return fmt.Errorf("obtain an access token for the upload: %w", err)
	}
	target := fmt.Sprintf("%s/api/update-management/updates/artifacts/%s/content", c.base(), st.tok("artifact-available"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, strings.NewReader(probeArtifactContent))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("upload to %s: %w", target, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload to %s: HTTP %d: %s", target, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// The device-visible configuration the seed declares on the probe profile and then sets
// on the probe device. A STRING shared attribute, like the facet attribute beside it.
const (
	probeConfigKey   = "apiprobe-config-mode"
	probeConfigValue = "eco"
)

// prePublish is the phase between the creates and the publishes: writes that must land
// on a parent AFTER it exists and BEFORE it is published, because publishing freezes them.
var prePublish = []entity{
	{
		// Depends on: device-profile.
		//
		// The profile's DRAFT device-visible configuration declaration. It is frozen into
		// the profile version at publish, and it is the declaration the attribute write in
		// afterPublish is checked against — so it has to be set before the publish.
		Name:     "device-profile-configuration",
		Area:     "device-management",
		Mutation: "setDeviceProfileConfigurationDeclaration",
		Params: []param{
			{Name: "token", Type: "String!"},
			{Name: "keys", Type: "[ConfigurationKeyInput!]!"},
		},
		Read:   "deviceProfilesByToken",
		Fields: "token configurationDeclaration{key valueType description}",
		Vars: func(s *state) map[string]any {
			return map[string]any{
				"token": s.tok("device-profile"),
				"keys": []any{map[string]any{
					"key":         probeConfigKey,
					"valueType":   probeValueType,
					"description": "Written by apiprobe; declares a device-visible configuration key.",
				}},
			}
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
	out := make([]entity, 0, len(entities)+len(prePublish)+len(publishes)+len(afterPublish))
	out = append(out, entities...)
	out = append(out, prePublish...)
	for _, p := range publishes {
		out = append(out, p.entity())
	}
	return append(out, afterPublish...)
}

// probeSignerGroup gates the three signer rows (artifact-signer, artifact-signed,
// artifact-signature) as ONE group on a field only a release with the signer registry declares.
//
// 🔑 THE TOKEN IS COMPUTED, NOT RECORDED. A row that feeds the shared state cannot be skipped by a
// baseline (plan refuses it, because its dependents would send an empty token), and a release
// older than the registry cannot express registerArtifactSigner. The platform derives a signer's
// token from the key, so the dependents compute it too (probeSignerToken); the signer is then a
// leaf, and every member carries the SAME Requires so they skip together.
var probeSignerGroup = []string{"ArtifactSigner.keyId"}

// probeSignerToken is the token the platform gives the probe signer: "signer-" and the first 16 hex
// digits of the SHA-256 of the key's SPKI DER (its keyId).
func probeSignerToken() string {
	der, err := x509.MarshalPKIXPublicKey(probeSignerKey().Public())
	if err != nil {
		panic(err) // an Ed25519 public key always marshals
	}
	sum := sha256.Sum256(der)
	return "signer-" + hex.EncodeToString(sum[:])[:16]
}

// probeSignerKey is the signer's key pair, derived from a fixed public seed. It is a test key:
// it signs one constant digest in a drill and has no authority anywhere.
func probeSignerKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte("apiprobe-signer-seed-0123456789!"))
}

// probeSignerPEM is the SPKI PEM of the signer's public key.
func probeSignerPEM() string {
	der, err := x509.MarshalPKIXPublicKey(probeSignerKey().Public())
	if err != nil {
		panic(err) // an Ed25519 public key always marshals
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// probeArtifactSignature is the base64 Ed25519 signature over the raw 32-byte digest of the probe
// artifact content, which is what the platform verifies.
func probeArtifactSignature() string {
	sum := sha256.Sum256([]byte(probeArtifactContent))
	return base64.StdEncoding.EncodeToString(ed25519.Sign(probeSignerKey(), sum[:]))
}
