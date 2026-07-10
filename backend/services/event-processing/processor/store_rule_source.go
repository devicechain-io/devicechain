// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// StoreRuleSource rebuilds the rule set at startup from the durable DetectRule projection
// (ADR-051 slice 4b-3) — NOT from the fact stream. The fact stream has finite retention, so
// it cannot be the source of truth on its own; the live consumer persists every fact into the
// projection before acking it, and this source replays the projection so a rule survives a
// restart however long ago it was published.
type StoreRuleSource struct {
	store *model.DetectRuleStore
}

// NewStoreRuleSource builds the source over the durable rule projection.
func NewStoreRuleSource(store *model.DetectRuleStore) *StoreRuleSource {
	return &StoreRuleSource{store: store}
}

// Load reads every persisted rule and compiles it into the scoped rule set. A store error
// fails startup (fail-closed) rather than starting with a partial rule set that would miss
// detections. Rules are compiled through the SAME path as the live consumer
// (runtime.CompilePublishedRules), so a rule loads identically whether it arrives live or is
// rebuilt here; a persisted rule that no longer compiles is skipped (logged), never fatal.
func (s *StoreRuleSource) Load(ctx context.Context) ([]runtime.ScopedRule, error) {
	rows, err := s.store.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	// Group rows by (tenant, profile-version) so the shared compile path composes ids and
	// scopes exactly as the live fact path does.
	type scope struct{ tenant, pvt string }
	grouped := map[scope][]dmmodel.PublishedDetectionRule{}
	for _, r := range rows {
		k := scope{r.Tenant, r.ProfileVersionToken}
		grouped[k] = append(grouped[k], dmmodel.PublishedDetectionRule{Token: r.RuleToken, Definition: r.Definition})
	}
	var scoped []runtime.ScopedRule
	compileFailures := 0
	for k, defs := range grouped {
		s, failed := runtime.CompilePublishedRules(k.tenant, k.pvt, defs)
		scoped = append(scoped, s...)
		compileFailures += failed
	}
	log.Info().Int("rules", len(scoped)).Int("compileFailures", compileFailures).
		Msg("Rebuilt DETECT rule set from the durable rule projection.")
	return scoped, nil
}

// decodePublishedRuleFact unmarshals one published-rule fact into its owning tenant (from the
// per-tenant subject) and payload. ok is false when the message is unusable (no parseable
// tenant, or an unmarshalable payload) — dropped, not fatal. The tenant travels on the
// subject, never the payload.
func decodePublishedRuleFact(ctx context.Context, msg messaging.Message) (string, *dmmodel.DetectionRulesPublishedEvent, bool) {
	_, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping published-rule fact with no parseable tenant in subject %q", msg.Subject)
		return "", nil, false
	}
	ev, err := dmproto.UnmarshalDetectionRulesPublishedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping published-rule fact that could not be parsed from subject %q", msg.Subject)
		return "", nil, false
	}
	return tenant, ev, true
}

// factRuleRows maps a published-rule fact to durable projection rows, composing each rule's
// runtime id with the SAME helper the compile path uses so the persisted id and the running
// id can never drift. Rules with an empty version or rule token are dropped (a malformed/
// forged fact; the compile path drops them too).
func factRuleRows(tenant string, ev *dmmodel.DetectionRulesPublishedEvent) []model.DetectRule {
	rows := make([]model.DetectRule, 0, len(ev.Rules))
	seen := make(map[string]int, len(ev.Rules))
	for _, r := range ev.Rules {
		if ev.ProfileVersionToken == "" || r.Token == "" {
			continue
		}
		id := runtime.PublishedRuleID(tenant, ev.ProfileVersionToken, r.Token)
		row := model.DetectRule{
			RuleId:              id,
			Tenant:              tenant,
			ProfileVersionToken: ev.ProfileVersionToken,
			RuleToken:           r.Token,
			Definition:          r.Definition,
		}
		// Dedup within the batch (last-wins): a single INSERT ... ON CONFLICT cannot touch the
		// same row twice, so a forged/buggy fact repeating a token would otherwise wedge the
		// whole batch upsert (and its good rules) into a redelivery loop. Authoring forbids
		// duplicate tokens (4b-1 per-tenant unique index), so this only guards a malformed fact.
		if idx, dup := seen[id]; dup {
			rows[idx] = row
			continue
		}
		seen[id] = len(rows)
		rows = append(rows, row)
	}
	return rows
}

// profileActiveFromFact maps a published-rule fact to its active-version projection row (ADR-051
// slice 4c-2b): it splits the immutable "{profileToken}@{version}" token into its STABLE profile
// token and records that token as active, carrying the version token and the publish time (the
// dead-man grace base). ok is false for a token that does not split into a non-empty profile
// token (an empty or "@"-less version token — a malformed/forged fact; factRuleRows drops such
// rules too, so the two projections stay symmetric). The profile-token grammar excludes "@"
// (ADR-042), so a single Cut is unambiguous.
func profileActiveFromFact(tenant string, ev *dmmodel.DetectionRulesPublishedEvent) (*model.ProfileActive, bool) {
	profileToken, version, ok := strings.Cut(ev.ProfileVersionToken, "@")
	if !ok || profileToken == "" || version == "" {
		return nil, false
	}
	return &model.ProfileActive{
		Tenant:             tenant,
		ProfileToken:       profileToken,
		ActiveVersionToken: ev.ProfileVersionToken,
		PublishedAt:        ev.PublishedAt,
	}, true
}
