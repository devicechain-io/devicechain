// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// validateDetectionRuleToken enforces that a detection-rule token is present and
// token-grammar-safe (ADR-042): it becomes part of the runtime rule id
// ("{tenant}/{profileVersionToken}/{token}", ADR-051 slice 4b) which is carried on a
// per-tenant subject, so a free-form token must not ossify before that enforcement.
func validateDetectionRuleToken(token string) error {
	if err := core.ValidateToken(token); err != nil {
		return fmt.Errorf("invalid detection rule token %q: %w", token, err)
	}
	return nil
}

// validateDetectionRuleDefinition checks only that the authored rule is well-formed JSON —
// non-empty and a JSON object. It deliberately does NOT parse the detection taxonomy: the
// authoritative type/cost/injection validation is the synchronous compile event-processing
// performs at publish (ADR-044). Checking JSON shape here gives the author an immediate,
// event-processing-independent error for the common mistake (malformed blob) while keeping
// cel-go single-homed. A blob that is valid JSON but not a real rule is caught at publish —
// when the rule is enabled (a disabled rule is inert and not gated).
func validateDetectionRuleDefinition(definition string) error {
	if definition == "" {
		return fmt.Errorf("a detection rule definition is required")
	}
	if !json.Valid([]byte(definition)) {
		return fmt.Errorf("detection rule definition is not valid JSON")
	}
	// Require a JSON object (the rules.Rule shape) rather than an array/scalar/null. Note
	// the literal `null` unmarshals into a map as a no-op with no error (and jsonb `null`
	// satisfies the not-null column), so it must be rejected explicitly — else an
	// obviously-wrong blob slips past this guard and only fails at publish-compile.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(definition), &probe); err != nil {
		return fmt.Errorf("detection rule definition must be a JSON object: %w", err)
	}
	if probe == nil {
		return fmt.Errorf("detection rule definition must be a JSON object, not null")
	}
	return nil
}

// validateAuthoringGraph checks the optional canvas sidecar is a well-formed JSON object when
// present (nil ⇒ a form-authored rule with no sidecar, always valid). Like the definition
// check it deliberately does NOT parse the graph taxonomy — the CanvasDefinition schema is
// single-homed in event-processing; this only guards the stored-blob shape.
func validateAuthoringGraph(graph *string) error {
	if graph == nil {
		return nil
	}
	if !json.Valid([]byte(*graph)) {
		return fmt.Errorf("detection rule authoring graph is not valid JSON")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(*graph), &probe); err != nil || probe == nil {
		return fmt.Errorf("detection rule authoring graph must be a JSON object")
	}
	return nil
}

// authoringGraphJSON maps the optional request string to a nullable datatypes.JSON column
// value — nil (SQL NULL) when absent, so clearing the sidecar is a first-class state.
func authoringGraphJSON(graph *string) datatypes.JSON {
	if graph == nil {
		return nil
	}
	return datatypes.JSON(*graph)
}

// authoringGraphStr is the READ half of authoringGraphJSON, which a partial update
// needs and a full replace did not: folding an absent field means handing ApplyTo the
// value already stored.
//
// 🔴 The test is on LENGTH rather than on nil, and the reason is narrower than it
// looks. Probed against this package's own fixtures, a NULL column reads back as a nil
// slice — so `graph == nil` would in fact be correct today, and an earlier version of
// this comment asserted the opposite (that NULL arrives non-nil and zero-length) to
// justify the length test. The test is right and that justification was invented.
//
// What the length test actually buys is that BOTH readings of "no sidecar" map to the
// same answer: nil, and the empty slice a driver or a future column default could hand
// back instead. `len(x) == 0` covers both without depending on which one the storage
// layer happens to produce, and the cost of being wrong here is a fold that writes an
// unparseable "" over a NULL column on every update that leaves the field alone.
func authoringGraphStr(graph datatypes.JSON) *string {
	if len(graph) == 0 {
		return nil
	}
	s := string(graph)
	return &s
}

// Create a new detection rule (ADR-051 slice 4b).
func (api *Api) CreateDetectionRule(ctx context.Context,
	request *DetectionRuleCreateRequest) (*DetectionRule, error) {
	matches, err := api.DeviceProfilesByToken(ctx, []string{request.DeviceProfileToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if err := validateDetectionRuleToken(request.Token); err != nil {
		return nil, err
	}
	if err := validateDetectionRuleDefinition(request.Definition); err != nil {
		return nil, err
	}
	if err := validateAuthoringGraph(request.AuthoringGraph); err != nil {
		return nil, err
	}
	if err := api.validateDetectionRuleScope(ctx, request.EntityGroupToken, request.EntityGroupVersion); err != nil {
		return nil, err
	}

	scopeToken, scopeVersion := normalizedRuleScope(request.EntityGroupToken, request.EntityGroupVersion)
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &DetectionRule{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity:     rdb.MetadataEntity{Metadata: metadataJSON},
		DeviceProfile:      matches[0],
		Definition:         datatypes.JSON(request.Definition),
		AuthoringGraph:     authoringGraphJSON(request.AuthoringGraph),
		Enabled:            request.Enabled,
		EntityGroupToken:   scopeToken,
		EntityGroupVersion: scopeVersion,
	}
	// A draft rule save stores the scope but does NOT enroll the read-model: enrollment
	// follows PUBLISHED state (the active-version scope-ref sync at publish/rollback), so a
	// draft edit can never tear the read-model out from under a still-live published rule.
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// UpdateDetectionRule applies a PARTIAL update: a field the caller did not name keeps
// its stored value, an explicit null clears a nullable one, and the required fields
// (deviceProfileToken, definition, enabled) refuse a null.
//
// The rule's OWN token is not in the input and cannot be moved, so it is not
// grammar-checked here; CreateDetectionRule is where it is set and therefore the only
// place that check can apply.
func (api *Api) UpdateDetectionRule(ctx context.Context, token string,
	request *DetectionRuleUpdateRequest) (*DetectionRule, error) {
	matches, err := api.DetectionRulesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]

	// Everything that can refuse resolves before anything is written.
	reparent, err := api.resolveProfileRef(ctx, request.DeviceProfileToken, updated.DeviceProfile)
	if err != nil {
		return nil, err
	}
	definition, err := request.Definition.ApplyToRequired("definition", string(updated.Definition))
	if err != nil {
		return nil, err
	}
	if request.Definition.Set {
		if err := validateDetectionRuleDefinition(definition); err != nil {
			return nil, err
		}
	}
	enabled, err := request.Enabled.ApplyToRequired("enabled", updated.Enabled)
	if err != nil {
		return nil, err
	}
	// The canvas sidecar is nullable — a form-authored rule has none — so an explicit
	// null drops the now-stale graph, which is the operation a form edit of a
	// canvas-authored rule needs. Omitting it now KEEPS it, where the full-replace
	// shape dropped it on every edit that failed to restate it.
	authoringGraph := request.AuthoringGraph.ApplyTo(authoringGraphStr(updated.AuthoringGraph))
	if request.AuthoringGraph.Set {
		if err := validateAuthoringGraph(authoringGraph); err != nil {
			return nil, err
		}
	}

	// THE SCOPE IS VALIDATED AS THE EFFECTIVE PAIR (ADR-062 S4). Either half may be
	// absent from the request, so the pairing rule has to be asked of what the rule will
	// END UP with: naming only a version on an unscoped rule is a half-set scope, and
	// normalizedRuleScope would otherwise discard it in silence.
	scopeToken := request.EntityGroupToken.ApplyTo(updated.EntityGroupToken)
	scopeVersion := request.EntityGroupVersion.ApplyTo(updated.EntityGroupVersion)
	if err := api.validateDetectionRuleScope(ctx, scopeToken, scopeVersion); err != nil {
		return nil, err
	}
	scopeToken, scopeVersion = normalizedRuleScope(scopeToken, scopeVersion)

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}

	updated.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(updated.Name)))
	updated.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(updated.Description)))
	updated.Metadata = metadataJSON
	updated.Definition = datatypes.JSON(definition)
	updated.AuthoringGraph = authoringGraphJSON(authoringGraph)
	updated.Enabled = enabled
	// Enrollment is NOT reconciled here — a draft edit is inert until published (see
	// CreateDetectionRule).
	updated.EntityGroupToken = scopeToken
	updated.EntityGroupVersion = scopeVersion
	if reparent != nil {
		updated.DeviceProfile = reparent
		updated.DeviceProfileId = reparent.ID
	}

	// Save clears the scope columns to NULL when the edit un-scopes the rule (gorm Save
	// writes zero-value pointer fields as NULL).
	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get detection rules by id.
func (api *Api) DetectionRulesById(ctx context.Context, ids []uint) ([]*DetectionRule, error) {
	return rdb.FindByIds[DetectionRule](api.RDB.DB(ctx).Preload("DeviceProfile"), ids)
}

// Get detection rules by token.
func (api *Api) DetectionRulesByToken(ctx context.Context, tokens []string) ([]*DetectionRule, error) {
	found := make([]*DetectionRule, 0)
	result := api.RDB.DB(ctx).Preload("DeviceProfile").Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for detection rules that meet criteria.
func (api *Api) DetectionRules(ctx context.Context,
	criteria DetectionRuleSearchCriteria) (*DetectionRuleSearchResults, error) {
	results := make([]DetectionRule, 0)
	db, pag := api.RDB.ListOf(ctx, &DetectionRule{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceProfile != nil {
			result = result.Where("device_profile_id = (?)",
				api.RDB.DB(ctx).Model(&DeviceProfile{}).Select("id").Where("token = ?", criteria.DeviceProfile))
		}
		return result.Preload("DeviceProfile")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &DetectionRuleSearchResults{Results: results, Pagination: pag}, nil
}

// DetectionRulesByDeviceProfile loads all detection rules declared on a device profile
// without pagination — the draft loader used to build a publish snapshot (ADR-045 slice c).
func (api *Api) DetectionRulesByDeviceProfile(ctx context.Context, profileId uint) ([]*DetectionRule, error) {
	found := make([]*DetectionRule, 0)
	result := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}
