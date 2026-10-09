// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The negated-attribute-guard advisory must reach the author on every door that compiles a rule,
// without changing whether the rule is accepted.

const (
	fallbackRule = `{"name":"hot","type":"threshold","severity":"major","when":{"cel":"!(\"lim\" in attr) && \"t\" in m && m[\"t\"] > 80.0"},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"hot"}}]}`
	guardedRule  = `{"name":"hot","type":"threshold","severity":"major","when":{"cel":"(\"lim\" in attr) && \"t\" in m && m[\"t\"] > attr[\"lim\"]"},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"hot"}}]}`
)

func TestValidateDetectionRules_CarriesTheNegatedGuardWarning(t *testing.T) {
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{
		Rules: []detectionRuleInput{
			{Token: "guarded", Definition: guardedRule},
			{Token: "fallback", Definition: fallbackRule},
		},
	})
	require.NoError(t, err)
	assert.True(t, res.Valid(), "a warning must never make the batch invalid")
	require.Len(t, res.Warnings(), 1)
	w := res.Warnings()[0]
	assert.Equal(t, int32(1), w.Index())
	assert.Equal(t, "fallback", w.Token())
	assert.Equal(t, "negatedAttributeGuard", w.Code())
	assert.Equal(t, []string{"lim"}, w.Params())
	assert.Contains(t, w.Message(), `"lim"`)
}

func TestCompileCanvas_CarriesTheNegatedGuardWarning(t *testing.T) {
	r := &SchemaResolver{}
	graph := `{"schemaVersion":1,
		"nodes":[
			{"id":"s","type":"source","config":{"scope":{"kind":"profile","profileToken":"thermostat"}}},
			{"id":"c","type":"threshold","config":{"name":"hot","severity":"critical","when":{"cel":"!(\"lim\" in attr) && \"t\" in m && m[\"t\"] > 80.0"}}},
			{"id":"a","type":"action","config":{"action":"raiseAlarm","alarmKey":"hot"}}
		],
		"edges":[{"from":"s:out","to":"c:in"},{"from":"c:signal","to":"a:in"}]}`
	res, err := r.CompileCanvas(authedContext(auth.DeviceRead), struct {
		Graph        string
		ProfileToken string
	}{Graph: graph, ProfileToken: "thermostat"})
	require.NoError(t, err)
	require.True(t, res.Ok(), "%+v", res.Diagnostics())
	require.Len(t, res.Diagnostics(), 1)
	d := res.Diagnostics()[0]
	assert.Equal(t, "warning", d.Severity())
	require.NotNil(t, d.NodeId())
	assert.Equal(t, "c", *d.NodeId(), "anchored on the condition node")
	require.NotNil(t, d.Code())
	assert.Equal(t, "negatedAttributeGuard", *d.Code())
	assert.Equal(t, []string{"lim"}, d.Params())
}

func TestDraftRule_CarriesTheNegatedGuardWarning(t *testing.T) {
	res, err := wiredResolver(fallbackRule).DraftDetectionRuleFromText(draftCtx(), draftInput(draftRuleFromTextInput{Text: "x", ProfileToken: "p"}))
	require.NoError(t, err)
	require.True(t, res.Ok())
	require.Len(t, res.Warnings(), 1)
	w := res.Warnings()[0]
	require.NotNil(t, w.Code())
	assert.Equal(t, "negatedAttributeGuard", *w.Code())
	assert.Equal(t, []string{"lim"}, w.Params())

	clean, err := wiredResolver(guardedRule).DraftDetectionRuleFromText(draftCtx(), draftInput(draftRuleFromTextInput{Text: "x", ProfileToken: "p"}))
	require.NoError(t, err)
	require.True(t, clean.Ok())
	assert.Empty(t, clean.Warnings())
}

// ── previewRule ────────────────────────────────────────────────────────────

type emptyReplay struct{ first time.Time }

type emptyReader struct{}

func (emptyReader) Read(context.Context) (messaging.Message, error) {
	return messaging.Message{}, io.EOF
}
func (emptyReader) Close() error { return nil }

func (e emptyReplay) NewReplayReaderFromTime(string, time.Time) (messaging.ReplayReader, time.Time, error) {
	return emptyReader{}, e.first, nil
}

// publishedProfiles returns an active-version store holding a published version for tenant acme /
// profile p, or an empty one.
func publishedProfiles(t *testing.T, published bool) *model.ProfileActiveStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.ProfileActive{}))
	s := model.NewProfileActiveStore(&rdb.RdbManager{Database: db})
	if published {
		require.NoError(t, s.Upsert(context.Background(), &model.ProfileActive{
			Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: time.Now(),
		}))
	}
	return s
}

func warningCodes(res *PreviewResultResolver) []string {
	var out []string
	for _, d := range res.Diagnostics() {
		if d.Code() != nil {
			out = append(out, *d.Code())
		}
	}
	return out
}

func previewWith(t *testing.T, r *SchemaResolver, def string) *PreviewResultResolver {
	t.Helper()
	res, err := r.PreviewRule(previewCtx(auth.DeviceRead, auth.LocationRead), previewInput(def, "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"))
	require.NoError(t, err)
	return res
}

const previewFallbackRule = `{"id":"draft","name":"hot","type":"threshold","when":{"cel":"!(\"lim\" in attr) && \"t\" in m && m[\"t\"] > 80.0"}}`

func TestPreviewRule_SuccessfulRunCarriesTheWarning(t *testing.T) {
	r := &SchemaResolver{Profiles: publishedProfiles(t, true), Replay: emptyReplay{first: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}}
	res := previewWith(t, r, previewFallbackRule)
	require.True(t, res.Ok())
	assert.Nil(t, res.Degraded(), "a complete run, not a degraded one")
	assert.Equal(t, []string{"negatedAttributeGuard"}, warningCodes(res))

	clean := previewWith(t, r, plainDraft)
	require.True(t, clean.Ok())
	assert.Empty(t, clean.Diagnostics())
}

func TestPreviewRule_DegradedRunsStillCarryTheWarning(t *testing.T) {
	// Unpublished profile: degraded, and the author still hears about the rule.
	r := &SchemaResolver{Profiles: publishedProfiles(t, false), Replay: emptyReplay{}}
	res := previewWith(t, r, previewFallbackRule)
	require.True(t, res.Ok())
	require.NotNil(t, res.Degraded())
	assert.Equal(t, []string{"negatedAttributeGuard"}, warningCodes(res))

	// No fence archive: the rule below tests containment AND the negated guard.
	r2 := &SchemaResolver{Profiles: publishedProfiles(t, true)}
	res2 := previewWith(t, r2, `{"id":"draft","name":"x","type":"threshold","when":{"cel":"!(\"lim\" in attr) && geo.inFence(\"yard\")"}}`)
	require.NotNil(t, res2.Degraded())
	assert.Equal(t, []string{"negatedAttributeGuard"}, warningCodes(res2))
}

func TestPreviewRule_ConcurrencyGateStillCarriesTheWarning(t *testing.T) {
	held := 0
	for globalPreviewGate.acquire("acme") {
		held++
	}
	defer func() {
		for ; held > 0; held-- {
			globalPreviewGate.release("acme")
		}
	}()
	r := &SchemaResolver{Profiles: publishedProfiles(t, true), Replay: emptyReplay{}}
	res := previewWith(t, r, previewFallbackRule)
	require.NotNil(t, res.Degraded())
	assert.Contains(t, *res.Degraded(), "maximum number of previews")
	assert.Equal(t, []string{"negatedAttributeGuard"}, warningCodes(res))
}
